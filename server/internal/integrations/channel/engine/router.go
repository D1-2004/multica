package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Router is the channel-agnostic inbound pipeline — the generalization of the
// Feishu-only lark.Dispatcher (+ the Hub's handleEvent outbound seam). It is
// the single shared channel.InboundHandler the Supervisor injects into every
// Channel: a Channel translates its platform payload into a
// channel.InboundMessage and calls Handle, which routes by ChannelType to that
// platform's registered resolver set and runs the same ordered pipeline for
// every platform — installation route → two-phase dedup → group @bot filter →
// identity + membership → ensure session → append+mark → /issue → durable
// debounced run trigger + detached media binding — then drives the detached
// outbound replier + typing indicator.
//
// The core contains no platform specifics: everything platform-shaped lives
// behind the resolver interfaces (a feishu ResolverSet is the first
// implementation). Adding a platform is "register a ResolverSet", not "edit
// the Router".
type Router struct {
	mu   sync.RWMutex
	sets map[channel.Type]ResolverSet

	issues IssueCreator
	tasks  TaskEnqueuer
	reader SessionReader

	// bus is optional (nil is valid). When wired, a committed inbound message
	// broadcasts chat:message — the same event the web send path publishes
	// from its handler. Every channel funnels through this Router, so this is
	// the one place that needs the wiring.
	bus *events.Bus

	batcher *pendingBatcher

	replyTimeout time.Duration
	mediaTimeout time.Duration
	mediaCtx     context.Context
	mediaCancel  context.CancelFunc
	mediaSem     chan struct{}
	replyWg      sync.WaitGroup
	mediaWg      sync.WaitGroup

	mediaQueueMu sync.Mutex
	mediaQueues  map[string]*mediaQueueEntry
	stopping     bool

	pendingFreshMu sync.Mutex
	pendingFresh   map[string]bool

	busyNoticeMu sync.Mutex
	busyNoticed  map[string]time.Time

	coordinator *inboundcoord.Coordinator
	associator  SceneAssociator

	logger *slog.Logger
}

// SceneAssociator writes the inbound conversation onto the Issue graph so a
// later recall by openConversationId can find the matter without the model.
type SceneAssociator interface {
	AssociateIssueConversation(ctx context.Context, in assoc.AssociateInput) error
}

// Config tunes the Router. Zero values default.
type RouterConfig struct {
	// ReplyTimeout caps a single detached OutboundReplier.Reply / typing
	// call. It runs off the connector ACK path, so it must stay strictly
	// under the platform ACK deadline (Lark: 3s). Defaults to 2.5s.
	ReplyTimeout time.Duration
	// MediaTimeout caps detached best-effort media download, upload, and
	// attachment binding for one message. The budget starts at append time
	// (it must match the persisted fire_at fallback), so it also spans any
	// wait behind earlier media in the same session and for a global
	// concurrency slot. Defaults to 45s.
	MediaTimeout time.Duration
	// MediaConcurrency caps concurrent media resolutions across all
	// sessions, bounding burst memory (unknown-length uploads buffer up to
	// the 100 MiB resource cap each) and platform download pressure.
	// Per-session ordering is unaffected. Defaults to 8.
	MediaConcurrency int
	Logger           *slog.Logger
}

// NewRouter builds a Router around the shared (platform-agnostic) services:
// the IssueCreator + TaskEnqueuer that /issue and chat runs go through, and a
// SessionReader for the debounced flush. Register a platform's ResolverSet
// with Register before Handle is called.
// SetEventBus wires the optional bus after construction so the constructor
// signature (and every test that calls it) stays untouched. Nil-safe.
func (r *Router) SetEventBus(bus *events.Bus) {
	if r != nil {
		r.bus = bus
	}
}

func (r *Router) SetInboundCoordinator(coordinator *inboundcoord.Coordinator) {
	if r != nil {
		r.coordinator = coordinator
	}
}

func (r *Router) SetSceneAssociator(associator SceneAssociator) {
	if r != nil {
		r.associator = associator
	}
}

func NewRouter(issues IssueCreator, tasks TaskEnqueuer, reader SessionReader, cfg RouterConfig) *Router {
	if cfg.ReplyTimeout == 0 {
		cfg.ReplyTimeout = 2500 * time.Millisecond
	}
	if cfg.MediaTimeout == 0 {
		cfg.MediaTimeout = DefaultMediaTimeout
	}
	if cfg.MediaConcurrency == 0 {
		cfg.MediaConcurrency = 8
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	mediaCtx, mediaCancel := context.WithCancel(context.Background())
	return &Router{
		sets:         make(map[channel.Type]ResolverSet),
		issues:       issues,
		tasks:        tasks,
		reader:       reader,
		replyTimeout: cfg.ReplyTimeout,
		mediaTimeout: cfg.MediaTimeout,
		mediaCtx:     mediaCtx,
		mediaCancel:  mediaCancel,
		mediaSem:     make(chan struct{}, cfg.MediaConcurrency),
		logger:       cfg.Logger,
		mediaQueues:  make(map[string]*mediaQueueEntry),
		pendingFresh: make(map[string]bool),
		busyNoticed:  make(map[string]time.Time),
	}
}

// DefaultMediaTimeout is the default RouterConfig.MediaTimeout. Exported so
// the channel-media settle invariant test can assert the reconciler's settle
// delay dwarfs every pipeline budget.
const DefaultMediaTimeout = 45 * time.Second

type mediaQueueEntry struct {
	tail chan struct{}
}

// Register binds a platform's ResolverSet under t. Call at boot, before Run.
// Registering an empty Type or a set missing a required resolver is ignored.
func (r *Router) Register(t channel.Type, set ResolverSet) {
	if t == "" || set.Installation == nil || set.Identity == nil || set.Dedup == nil || set.Session == nil || set.Audit == nil {
		r.logger.Warn("channel router: ignoring incomplete resolver set", "channel_type", string(t))
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sets[t] = set
}

// EnableRunBatching installs the debouncer in front of the per-session run
// trigger. Call once at boot. A non-positive window uses
// DefaultChatRunBatchWindow. Without it, runs fire inline (used by tests).
func (r *Router) EnableRunBatching(window time.Duration) {
	r.batcher = newPendingBatcher(window)
}

// Drain cancels detached media processing, flushes debounced run triggers, and
// joins media/reply goroutines until ctx ends. It returns whether everything
// completed. Call on shutdown AFTER the Supervisor has stopped delivering
// events; timed-out media retains its durable placeholder fallback.
func (r *Router) Drain(ctx context.Context) bool {
	r.mediaQueueMu.Lock()
	r.stopping = true
	r.mediaCancel()
	r.mediaQueueMu.Unlock()

	done := make(chan struct{})
	go func() {
		if r.batcher != nil {
			r.batcher.FlushAll()
		}
		r.mediaWg.Wait()
		r.replyWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// ErrNoResolverSet is returned by Handle when a message arrives for a channel
// type that has no registered ResolverSet — a boot/registration bug. It is an
// infrastructure error so the adapter surfaces it rather than silently
// dropping.
var ErrNoResolverSet = errors.New("channel router: no resolver set for channel type")

// Handle is the shared channel.InboundHandler. It runs the pipeline and then
// drives the detached outbound side; it returns a non-nil error only for
// infrastructure failures (the adapter reconnects). Product outcomes (dropped,
// needs-binding, …) are not errors.
func (r *Router) Handle(ctx context.Context, msg channel.InboundMessage) error {
	_, err := r.HandleResult(ctx, msg)
	return err
}

// HandleResult runs the same inbound pipeline as Handle and returns the
// committed routing result to synchronous HTTP adapters.
func (r *Router) HandleResult(ctx context.Context, msg channel.InboundMessage) (Result, error) {
	return r.HandleResultWithOptions(ctx, msg, HandleOptions{})
}

// HandleOptions changes only the independently selected execution policies for
// one inbound message. Direct channel adapters use the zero value.
type HandleOptions struct {
	InstallationOverride   *ResolvedInstallation
	IdentityOverride       *ResolvedIdentity
	ChatSessionOverride    *pgtype.UUID
	CreateUnboundSession   bool
	SuppressServerOutbound bool
	DisableControlCommands bool
	ForceFreshSession      bool
}

// HandleResultWithOptions runs the inbound pipeline with caller-selected
// identity and outbound policies.
func (r *Router) HandleResultWithOptions(ctx context.Context, msg channel.InboundMessage, options HandleOptions) (Result, error) {
	// Preserve the user's original normalized text before any shared command
	// rewrites. Session binders pass this source to command classifiers while
	// Text remains the agent-readable body.
	if msg.CommandText == "" {
		msg.CommandText = msg.Text
	}

	// /new is a channel-wide product command, not an adapter capability. Parse
	// the original command source here even when an adapter already set
	// ForceFresh, so bare-command classification stays identical across
	// platforms. Only rewrite Text when the adapter has not already stripped
	// the directive; Feishu enriches that stripped body before it reaches us.
	if body, ok := ParseFreshSessionCommand(msg.CommandText); ok && !options.DisableControlCommands {
		adapterAlreadyStripped := msg.ForceFresh
		msg.ForceFresh = true
		if !adapterAlreadyStripped {
			msg.Text = body
		}
	}

	r.mu.RLock()
	set, ok := r.sets[msg.Source.ChannelType]
	r.mu.RUnlock()
	if !ok {
		r.logger.Error("channel router: no resolver set",
			"event", "channel_inbound_resolver_missing",
			"channel_type", string(msg.Source.ChannelType),
			"message_id_hash", inboundTraceHash(msg.MessageID),
			"event_id_hash", inboundTraceHash(msg.EventID),
		)
		return Result{}, ErrNoResolverSet
	}

	res, inst, err := r.dispatch(ctx, set, msg, options)
	if err != nil {
		r.logger.Error("channel router: dispatch error",
			"event", "channel_inbound_dispatch_failed",
			"channel_type", string(msg.Source.ChannelType),
			"installation_id", uuidString(inst.ID),
			"message_id_hash", inboundTraceHash(msg.MessageID),
			"event_id_hash", inboundTraceHash(msg.EventID),
			"error", err,
		)
		return Result{}, err
	}
	r.logger.Info("channel router: dispatch outcome",
		"event", "channel_inbound_dispatched",
		"channel_type", string(msg.Source.ChannelType),
		"installation_id", uuidString(inst.ID),
		"chat_session_id", uuidString(res.ChatSessionID),
		"message_id_hash", inboundTraceHash(msg.MessageID),
		"event_id_hash", inboundTraceHash(msg.EventID),
		"outcome", string(res.Outcome),
		"drop_reason", string(res.DropReason),
	)

	// Typing indicator on ingest, detached so the reaction HTTP call never
	// blocks the connector ACK path.
	if !options.SuppressServerOutbound && res.Outcome == OutcomeIngested && res.runScheduled && set.Typing != nil {
		go func() {
			tctx, cancel := context.WithTimeout(context.Background(), r.replyTimeout)
			defer cancel()
			set.Typing.OnIngested(tctx, inst, msg, res.ChatSessionID, res.TaskID)
		}()
	}
	if !options.SuppressServerOutbound {
		r.scheduleReply(set, inst, msg, res)
	}
	return res, nil
}

// dispatch runs the pipeline and returns the typed result plus the resolved
// installation (needed by the outbound side). Mirrors lark.Dispatcher.Handle.
func (r *Router) dispatch(ctx context.Context, set ResolverSet, msg channel.InboundMessage, options HandleOptions) (Result, ResolvedInstallation, error) {
	// 1. Route to installation. The adapter maps the platform routing key
	//    (carried on the message) to its installation row. These drop
	//    branches run BEFORE the dedup claim because they have no valid
	//    installation to attach a claim to.
	var inst ResolvedInstallation
	if options.InstallationOverride != nil {
		inst = *options.InstallationOverride
		if !inst.ID.Valid || !inst.WorkspaceID.Valid || !inst.AgentID.Valid || !inst.InstallerUserID.Valid {
			return Result{}, ResolvedInstallation{}, errors.New("channel router: installation override is incomplete")
		}
		if options.IdentityOverride == nil {
			return Result{}, ResolvedInstallation{}, errors.New("channel router: installation override requires identity override")
		}
		if !options.IdentityOverride.PrincipalUserID.Valid {
			return Result{}, ResolvedInstallation{}, errors.New("channel router: installation override identity has no principal")
		}
		if options.IdentityOverride.PrincipalUserID != inst.InstallerUserID {
			return Result{}, ResolvedInstallation{}, errors.New("channel router: installation override identity does not match installer")
		}
	} else {
		resolved, err := set.Installation.ResolveInstallation(ctx, msg)
		if err != nil {
			if errors.Is(err, ErrInstallationNotFound) {
				_ = set.Audit.RecordDrop(ctx, pgtype.UUID{}, msg, DropReasonInvalidEvent)
				return Result{Outcome: OutcomeDropped, DropReason: DropReasonInvalidEvent}, ResolvedInstallation{}, nil
			}
			return Result{}, ResolvedInstallation{}, fmt.Errorf("resolve installation: %w", err)
		}
		inst = resolved
	}
	if !inst.Active {
		return r.drop(ctx, set, msg, inst.ID, DropReasonRevokedInstallation), inst, nil
	}

	// 2. Two-phase dedup claim with owner fencing — before group filter and
	//    identity so a reconnect replay cannot re-trigger a binding prompt,
	//    re-write a drop audit, or re-touch the session. Empty MessageID
	//    means there is no key to dedup by; skip the claim.
	var claimToken pgtype.UUID
	claimed := false
	if msg.MessageID != "" {
		token, err := set.Dedup.Claim(ctx, inst.ID, msg.MessageID)
		if err != nil {
			if errors.Is(err, ErrDuplicate) {
				return r.drop(ctx, set, msg, inst.ID, DropReasonDuplicate), inst, nil
			}
			return Result{}, inst, fmt.Errorf("dedup claim: %w", err)
		}
		claimToken = token
		claimed = true
	}

	res, finalize, err := r.processClaimed(ctx, set, msg, inst, claimToken, options)

	if claimed {
		if finalizeErr := r.applyFinalize(ctx, set, inst.ID, msg.MessageID, claimToken, finalize); finalizeErr != nil {
			wrapped := fmt.Errorf("finalize inbound dedup: %w", finalizeErr)
			if err != nil {
				return res, inst, errors.Join(err, wrapped)
			}
			return res, inst, wrapped
		}
	}

	// ErrClaimLost: another worker holds the claim. Surface as duplicate.
	if errors.Is(err, ErrClaimLost) {
		return r.drop(ctx, set, msg, inst.ID, DropReasonDuplicate), inst, nil
	}
	return res, inst, err
}

// dedupFinalize tells dispatch how to land the claim row after processClaimed.
type dedupFinalize int

const (
	finalizeNone dedupFinalize = iota
	finalizeMark
	finalizeRelease
)

// processClaimed runs the post-dedup pipeline. Mirrors
// lark.Dispatcher.processClaimed; see its boundary contract per step.
func (r *Router) processClaimed(ctx context.Context, set ResolverSet, msg channel.InboundMessage, inst ResolvedInstallation, claimToken pgtype.UUID, options HandleOptions) (Result, dedupFinalize, error) {
	if options.DisableControlCommands {
		msg.ForceFresh = options.ForceFreshSession
	}
	trace := chattrace.New(string(msg.Source.ChannelType))
	if msg.TraceID != "" || msg.TraceChannel != "" || msg.TraceStartedAtUnixMS != 0 {
		var traceErr error
		trace, traceErr = chattrace.From(msg.TraceID, msg.TraceChannel, msg.TraceStartedAtUnixMS)
		if traceErr != nil {
			return Result{}, finalizeRelease, fmt.Errorf("validate inbound chat trace: %w", traceErr)
		}
	}
	chattrace.LogStage(r.logger, trace, "channel_message_received", "started",
		"installation_id", uuidString(inst.ID),
	)
	// 3. Group-mention filter (group chats only), before identity so an
	//    unbound user's idle group chatter never spams a binding card.
	if msg.Source.ChatType == channel.ChatTypeGroup && !msg.AddressedToBot {
		return r.drop(ctx, set, msg, inst.ID, DropReasonNotAddressedInGroup), finalizeMark, nil
	}

	// 3b. /unbind command — resolved BEFORE identity on purpose: identity
	//     resolution may auto-bind the sender through a directory match,
	//     and auto-binding someone who is asking to be unbound would be
	//     absurd. The command consumes the message: no session write, no
	//     run trigger, dedup marked processed.
	if !options.DisableControlCommands && set.Unbind != nil && ParseUnbindCommand(msg.Text) {
		existed, err := set.Unbind.UnbindSender(ctx, inst, msg)
		if err != nil {
			return Result{}, finalizeRelease, fmt.Errorf("unbind sender: %w", err)
		}
		return Result{
			Outcome:        OutcomeUnbound,
			InstallationID: inst.ID,
			Sender:         msg.Source.SenderID,
			UnbindExisted:  existed,
		}, finalizeMark, nil
	}

	// 4. Resolve the Multica authorization identity. Direct channel ingress maps
	//    the platform sender and re-verifies workspace membership. Authenticated
	//    internal dispatch may instead provide a trusted endpoint principal.
	identity := ResolvedIdentity{}
	if options.IdentityOverride != nil {
		identity = *options.IdentityOverride
		if !identity.PrincipalUserID.Valid {
			return Result{}, finalizeRelease, errors.New("channel router: identity override has no principal")
		}
	} else {
		resolvedIdentity, resolveErr := set.Identity.ResolveSender(ctx, inst, msg)
		if resolveErr != nil {
			switch {
			case errors.Is(resolveErr, ErrSenderUnbound):
				_ = set.Audit.RecordDrop(ctx, inst.ID, msg, DropReasonUnboundUser)
				return Result{
					Outcome:        OutcomeNeedsBinding,
					DropReason:     DropReasonUnboundUser,
					InstallationID: inst.ID,
					Sender:         msg.Source.SenderID,
				}, finalizeMark, nil
			case errors.Is(resolveErr, ErrSenderNotMember):
				return r.drop(ctx, set, msg, inst.ID, DropReasonNonWorkspaceMember), finalizeMark, nil
			default:
				return Result{}, finalizeRelease, fmt.Errorf("resolve sender: %w", resolveErr)
			}
		}
		identity = resolvedIdentity
	}

	// 5. Resolve the chat_session. Direct shared group ingress uses the installer;
	//    authenticated dispatch and sender-isolated sessions use their resolved
	//    principal.
	sessionCreator := identity.PrincipalUserID
	if options.IdentityOverride == nil && msg.Source.ChatType == channel.ChatTypeGroup && !set.GroupSessionsPerSender {
		sessionCreator = inst.InstallerUserID
	}
	sessionParams := EnsureSessionParams{
		Installation: inst,
		Sender:       sessionCreator,
		Message:      msg,
	}
	var sessionID pgtype.UUID
	var err error
	switch {
	case options.ChatSessionOverride != nil:
		sessionID = *options.ChatSessionOverride
	case options.CreateUnboundSession:
		creator, ok := set.Session.(UnboundSessionCreator)
		if !ok {
			return Result{}, finalizeRelease, errors.New("channel router: session binder cannot create an unbound session")
		}
		sessionID, err = creator.CreateUnboundSession(ctx, sessionParams)
	default:
		sessionID, err = set.Session.EnsureSession(ctx, sessionParams)
	}
	if err != nil {
		// Single tx; an error rolled it back, nothing landed. Release.
		return Result{}, finalizeRelease, fmt.Errorf("ensure chat session: %w", err)
	}
	freshBody, isFreshCommand := ParseFreshSessionCommand(msg.CommandText)
	if msg.ForceFresh && isFreshCommand && strings.TrimSpace(freshBody) == "" {
		finalize := finalizeMark
		if set.DurableRuns {
			if set.PendingFresh == nil {
				return Result{}, finalizeRelease, fmt.Errorf("durable channel missing pending fresh session store")
			}
			markedInTx, err := set.PendingFresh.PersistPendingFreshSession(ctx, PendingFreshSessionParams{
				SessionID:      sessionID,
				InstallationID: inst.ID,
				MessageID:      msg.MessageID,
				ClaimToken:     claimToken,
			})
			if err != nil {
				if errors.Is(err, ErrClaimLost) {
					return Result{}, finalizeNone, err
				}
				return Result{}, finalizeRelease, fmt.Errorf("persist pending fresh session: %w", err)
			}
			if markedInTx {
				finalize = finalizeNone
			}
		} else {
			r.markPendingFresh(keyForSession(sessionID))
		}
		return Result{
			Outcome:        OutcomeFreshSession,
			InstallationID: inst.ID,
			ChatSessionID:  sessionID,
			Sender:         msg.Source.SenderID,
		}, finalize, nil
	}
	var taskContext []byte
	if set.TaskContext != nil {
		taskContext, err = set.TaskContext.ResolveTaskContext(ctx, inst, msg)
		if err != nil {
			if errors.Is(err, ErrTaskContextRejected) {
				return r.drop(ctx, set, msg, inst.ID, DropReasonTaskContextRejected), finalizeMark, nil
			}
			return Result{}, finalizeRelease, fmt.Errorf("resolve chat task context: %w", err)
		}
	}
	taskContext, err = chattrace.Merge(taskContext, trace)
	if err != nil {
		return Result{}, finalizeRelease, fmt.Errorf("persist inbound chat trace: %w", err)
	}

	var issueCommand *IssueCommand
	issueCommandRequested := false
	if !options.DisableControlCommands {
		issueCommand, issueCommandRequested = ParseIssueCommand(msg.CommandText)
	}
	coordDecision := inboundcoord.Decision{Action: inboundcoord.ActionContinue}
	coordinatorIssue := false
	skipSandboxPrepare := false
	var coordAgentID pgtype.UUID
	if r.coordinator != nil && !issueCommandRequested && msg.Source.ChannelType == "dingtalk" {
		session, sessionErr := r.reader.GetChatSession(ctx, sessionID)
		if sessionErr != nil {
			return Result{}, finalizeRelease, fmt.Errorf("load chat session for coordinator: %w", sessionErr)
		}
		coordAgentID = session.AgentID
		turn := r.coordinator.TurnFromChatSession(
			ctx,
			session,
			coordinatorSource(options, taskContext, msg.Source.ChannelType),
			msg.AddressedToBot || msg.Source.ChatType == channel.ChatTypeP2P,
			string(msg.Source.ChatType),
			"",
			coordinatorSenderName(taskContext, msg.Source.SenderID),
			msg.Text,
		)
		turn.ConversationID = strings.TrimSpace(msg.Source.ChatID)
		turn.PersonID = strings.TrimSpace(msg.Source.SenderID)
		turn.EvidenceID = strings.TrimSpace(msg.MessageID)
		turn.Kind = string(msg.Source.ChatType)
		turn.UserID = identity.PrincipalUserID
		turn.IssueDispatchContext = taskContext
		turn.DWSUID, turn.DWSOrgID = coordinatorDWSIdentity(taskContext)
		turn.IdentityNote = inboundcoord.IdentityNote(turn.Source, turn.ConversationID, turn.PersonID)
		// The coordinator turn shares the inbound chat trace so its Langfuse
		// trace and the task it may start are one tree.
		turn.TraceID = trace.TraceID
		coordDecision = r.coordinator.Decide(ctx, turn)
		switch coordDecision.Action {
		case inboundcoord.ActionRetry:
			return Result{}, finalizeRelease, service.ErrIssueDispatchPending
		case inboundcoord.ActionIssue:
			taskContext, err = inboundcoord.IndependentIssueTaskContext(
				taskContext,
				inboundcoord.CoordinatorIssueTriggerCreate,
			)
			if err != nil {
				return Result{}, finalizeRelease, fmt.Errorf("prepare coordinator issue task context: %w", err)
			}
			coordinatorIssue = true
			issueCommand = &IssueCommand{
				Title:       inboundcoord.IssueTitle(coordDecision, msg.Text),
				Description: inboundcoord.IssueDescription(coordDecision, msg.Text),
			}
			issueCommandRequested = true
		case inboundcoord.ActionReply, inboundcoord.ActionSilence:
			skipSandboxPrepare = true
		}
		if len(coordDecision.Steps) > 0 {
			// The task (Issue or chat continuation) records which coordinator
			// turn looked at it and repeats its trace tags; both share the
			// inbound chat trace already.
			taskContext = inboundcoord.StampCoordinatorTrace(taskContext, coordDecision, trace.Channel, time.UnixMilli(trace.StartedAtUnixMS))
		}
	}
	issueNeedsUsage := issueCommandRequested && issueCommand.Title == "" && !set.DurableRuns
	hasMedia := set.Media != nil && set.Media.HasMedia(msg)
	resolveMedia := !issueNeedsUsage && hasMedia
	localMediaDeadline := time.Now().Add(r.mediaTimeout)
	mediaPendingSeconds := 0.0
	if resolveMedia {
		mediaPendingSeconds = r.mediaTimeout.Seconds()
		if issueCommandRequested {
			issueCommand.Description = issueDescriptionFromCommandBody(msg.Text, msg.CommandText, issueCommand.Description)
		}
	}

	var preparedTask *service.PreparedChannelChatTask
	durableOutcome := OutcomeIngested
	durableFresh := false
	var durableIssueResult *service.IssueCreateResult
	var durableIssueErr error
	if set.DurableRuns && issueCommandRequested {
		resolvedCommand, err := r.resolveDurableIssueCommand(ctx, sessionID, *issueCommand)
		if err != nil {
			return Result{}, finalizeRelease, fmt.Errorf("resolve durable issue command: %w", err)
		}
		prefix := r.issuePrefix(ctx, inst.WorkspaceID)
		var assignedRunFireAt time.Time
		if resolveMedia {
			assignedRunFireAt = localMediaDeadline.Add(mediaFinalizeTimeout)
		}
		issueRes, _, err := r.createOrRecoverDurableIssue(
			ctx, inst, set.OriginType, identity.PrincipalUserID, msg.MessageID,
			resolvedCommand, taskContext, prefix, assignedRunFireAt,
		)
		if err != nil && !(errors.Is(err, service.ErrActiveDuplicate) && issueRes.DuplicateIssue != nil) {
			return Result{}, finalizeRelease, fmt.Errorf("create durable issue command: %w", err)
		}
		durableIssueResult = &issueRes
		durableIssueErr = err
	}
	if set.DurableRuns && !issueCommandRequested && !skipSandboxPrepare {
		session, err := r.reader.GetChatSession(ctx, sessionID)
		if err != nil {
			return Result{}, finalizeRelease, fmt.Errorf("load chat session for durable task: %w", err)
		}
		durableFresh = msg.ForceFresh
		prepared, err := r.tasks.PrepareChannelChatTask(ctx, session, service.ChatTaskIdentity{
			PrincipalUserID: identity.PrincipalUserID,
			InitiatorUserID: identity.InitiatorUserID,
		}, durableFresh, taskContext)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrChatTaskAgentNoRuntime):
				durableOutcome = OutcomeAgentOffline
			case errors.Is(err, service.ErrChatTaskAgentArchived):
				durableOutcome = OutcomeAgentArchived
			default:
				return Result{}, finalizeRelease, fmt.Errorf("prepare durable chat task: %w", err)
			}
		} else {
			if msg.DisableRunBatching {
				prepared.DebounceSeconds = 0
			}
			preparedTask = &prepared
		}
	}

	// 6. Append message + task (for durable channels) + in-tx dedup Mark.
	appendRes, err := set.Session.AppendMessage(ctx, AppendParams{
		SessionID:              sessionID,
		WorkspaceID:            inst.WorkspaceID,
		Sender:                 identity.PrincipalUserID,
		InstallationID:         inst.ID,
		Installation:           inst,
		Message:                msg,
		ClaimToken:             claimToken,
		SkipBindingReplyTarget: options.CreateUnboundSession || options.ChatSessionOverride != nil,
		ForceFreshSession:      set.DurableRuns && durableFresh,
		PreparedTask:           preparedTask,
		DisableIssueCommand:    options.DisableControlCommands,
		MediaPendingSeconds:    mediaPendingSeconds,
	})
	if err == nil {
		r.publishInboundMessage(inst.WorkspaceID, sessionID, identity.PrincipalUserID, appendRes)
	}
	if err != nil {
		if errors.Is(err, ErrClaimLost) {
			return Result{}, finalizeNone, err
		}
		return Result{}, finalizeRelease, fmt.Errorf("append user message: %w", err)
	}

	// Post-append paths must NOT Release (chat_message + Mark already
	// committed). Mark-again is a no-op, so finalizeNone — unless the binder
	// did not Mark in-tx (defensive), then fall back to a post-pipeline Mark.
	postAppendFinalize := finalizeNone
	if !appendRes.DedupMarked {
		postAppendFinalize = finalizeMark
	}

	res := Result{
		Outcome:        durableOutcome,
		InstallationID: inst.ID,
		ChatSessionID:  sessionID,
		TaskID:         appendRes.TaskID,
		Sender:         msg.Source.SenderID,
	}
	var mediaIssue db.Issue
	var deferredIssueTaskID pgtype.UUID

	// 7. /issue command, if present. chat_message is already durable; all
	//    error returns from here signal finalizeNone (or the defensive Mark).
	if appendRes.IssueCommand != nil || durableIssueResult != nil {
		command := appendRes.IssueCommand
		if command == nil {
			command = issueCommand
		}
		if command == nil {
			return Result{}, postAppendFinalize, errors.New("issue command result has no command")
		}
		if command.Title == "" && durableIssueResult == nil {
			res.Outcome = OutcomeIssueUsage
			res.IssueUsageHadMedia = hasMedia
			return res, postAppendFinalize, nil
		}
		if resolveMedia {
			// CommandText intentionally omits adapter-generated media placeholders so
			// image-before-command layouts still classify as /issue. Restore the
			// description from the full normalized body after classification so the
			// created issue retains the inline positions that detached media binding
			// will materialize. Text-only commands retain their existing parser output.
			command.Description = issueDescriptionFromCommandBody(
				msg.Text,
				msg.CommandText,
				command.Description,
			)
		}
		// One lookup feeds both the broadcast payload's identifier and the
		// chat reply's.
		prefix := r.issuePrefix(ctx, inst.WorkspaceID)
		var assignedRunFireAt time.Time
		if resolveMedia {
			// The generic deferred-task sweeper is the crash fallback. Leave room
			// after the media deadline for the bounded attachment finalizer so it
			// cannot race an issue agent reading the newly-created issue.
			assignedRunFireAt = localMediaDeadline.Add(mediaFinalizeTimeout)
		}
		var issueRes service.IssueCreateResult
		if durableIssueResult != nil {
			issueRes = *durableIssueResult
			err = durableIssueErr
		} else {
			issueRes, err = r.createIssue(ctx, inst, set.OriginType, identity.PrincipalUserID, sessionID, *command, taskContext, prefix, assignedRunFireAt)
		}
		if errors.Is(err, service.ErrActiveDuplicate) && issueRes.DuplicateIssue != nil {
			duplicate := *issueRes.DuplicateIssue
			res.IssueID = duplicate.ID
			res.IssueNumber = duplicate.Number
			res.IssueTitle = duplicate.Title
			res.IssueIdentifier = service.IssueIdentifier(prefix, duplicate.Number)
			res.IssueDuplicate = true
			r.associateIssueConversation(ctx, inst, msg, duplicate, pgtype.UUID{})
			// A duplicate is a terminal product outcome, not an infrastructure
			// failure and not a chat prompt. Finalize the durable chat message's
			// media state without resolving it. There is no new issue to consume
			// the media, so downloading and persisting attachments would only
			// create unused resources.
			if resolveMedia {
				r.enqueueMediaFinalization(set, inst, identity, appendRes.MessageID, msg, sessionID, localMediaDeadline)
			}
			return res, postAppendFinalize, nil
		}
		if err != nil {
			r.logger.Error("channel issue command failed",
				"event", "channel_issue_command_create_failed",
				"channel_type", string(msg.Source.ChannelType),
				"installation_id", uuidString(inst.ID),
				"chat_session_id", uuidString(sessionID),
				"message_id_hash", inboundTraceHash(msg.MessageID),
				"error", err,
			)
			return Result{}, postAppendFinalize, fmt.Errorf("create issue from command: %w", err)
		}
		res.IssueID = issueRes.Issue.ID
		res.CoordinatorIssue = coordinatorIssue
		mediaIssue = issueRes.Issue
		deferredIssueTaskID = issueRes.AssignedTaskID
		res.IssueNumber = issueRes.Issue.Number
		res.IssueTitle = issueRes.Issue.Title
		// Same renderer the broadcast payload uses, so a degraded prefix can't
		// show the chat "#42" while the realtime list shows "-42".
		res.IssueIdentifier = service.IssueIdentifier(prefix, issueRes.Issue.Number)
		res.ReplyText = coordDecision.UserText
		if issueRes.EnqueuedTask != nil {
			res.TaskID = issueRes.EnqueuedTask.ID
		}
		runID := issueRes.AssignedTaskID
		if issueRes.EnqueuedTask != nil {
			runID = issueRes.EnqueuedTask.ID
		} else if res.TaskID.Valid {
			runID = res.TaskID
		}
		r.associateIssueConversation(ctx, inst, msg, issueRes.Issue, runID)
		if coordDecision.UserText != "" {
			if persistErr := r.persistCoordinatorAssistant(ctx, inst.WorkspaceID, sessionID, coordAgentID, coordDecision); persistErr != nil {
				r.logger.Warn("channel router: persist coordinator issue ack failed",
					"chat_session_id", uuidString(sessionID),
					"error", persistErr,
				)
			}
		}
		// IssueService.Create already enqueues the assigned agent's issue task.
		// Scheduling the command as a chat run too makes the agent execute the
		// same /issue input again. A synchronous issue command is terminal.
		if resolveMedia {
			r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{
				String: command.Description,
				Valid:  true,
			}, msg.CommandText, deferredIssueTaskID, localMediaDeadline)
		}
		return res, postAppendFinalize, nil
	}

	if skipSandboxPrepare && coordDecision.Action == inboundcoord.ActionReply {
		if err := r.persistCoordinatorAssistant(ctx, inst.WorkspaceID, sessionID, coordAgentID, coordDecision); err != nil {
			r.logger.Warn("channel router: persist coordinator reply failed",
				"chat_session_id", uuidString(sessionID),
				"error", err,
			)
		}
		res.Outcome = OutcomeCoordinatorReply
		res.ReplyText = coordDecision.UserText
		res.runScheduled = false
		if resolveMedia {
			r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{}, "", deferredIssueTaskID, localMediaDeadline)
		}
		return res, postAppendFinalize, nil
	}
	if skipSandboxPrepare && coordDecision.Action == inboundcoord.ActionSilence {
		res.Outcome = OutcomeCoordinatorSilence
		res.runScheduled = false
		if resolveMedia {
			r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{}, "", deferredIssueTaskID, localMediaDeadline)
		}
		return res, postAppendFinalize, nil
	}

	if set.DurableRuns {
		res.runScheduled = preparedTask != nil
		if resolveMedia {
			r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{}, "", deferredIssueTaskID, localMediaDeadline)
		}
		return res, postAppendFinalize, nil
	}

	// 8. Debounce the run trigger. The synchronous outcome is OutcomeIngested
	//    with no TaskID — the task row is created at flush. The resolved task
	//    identity keeps its authorization principal separate from attribution.
	//    THIS message's sender (the task initiator), deliberately not the
	//    session creator (group sessions are creator=installer). Latest sender
	//    in a window wins (MUL-2645).
	//
	//    SkipAgentRun lets an adapter opt this message out of the agent turn —
	//    used by wecom for standalone /issue commands where the engine has
	//    already done the meaningful work (created the issue, sent the
	//    "✅ Created #N" reply via OutboundReplier) and an agent reply would
	//    just quote the slash command back. The chat_message is still durable
	//    and the OutboundReplier still fires — only the debounced run trigger
	//    (and therefore the typing indicator) is suppressed.
	if !msg.SkipAgentRun {
		r.scheduleRun(set, inst, msg, sessionID, service.ChatTaskIdentity{
			PrincipalUserID: identity.PrincipalUserID,
			InitiatorUserID: identity.InitiatorUserID,
		}, taskContext, options.SuppressServerOutbound)
		res.runScheduled = true
	}
	if resolveMedia {
		r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{}, "", deferredIssueTaskID, localMediaDeadline)
	}
	return res, postAppendFinalize, nil
}

// enqueueMedia detaches remote media I/O from Handle while preserving message
// order within a chat session. Run scheduling is independent and durable: the
// task service defers a task to the persisted media deadline, then media
// completion promotes it early.
func (r *Router) enqueueMedia(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, issue db.Issue, issueDescriptionBase pgtype.Text, issueCommandText string, issueTaskID pgtype.UUID, deadline time.Time) {
	r.enqueueMediaJob(set, inst, identity, chatMessageID, msg, sessionID, issue, issueDescriptionBase, issueCommandText, issueTaskID, true, deadline)
}

// enqueueMediaFinalization clears the durable media-pending marker without
// invoking the platform resolver. Duplicate /issue commands use this because
// neither the existing issue nor the hidden command message should gain a new
// copy of media that no newly-created issue will consume.
func (r *Router) enqueueMediaFinalization(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, deadline time.Time) {
	r.mediaQueueMu.Lock()
	if r.stopping {
		r.mediaQueueMu.Unlock()
		return
	}
	r.mediaWg.Add(1)
	r.mediaQueueMu.Unlock()

	go func() {
		defer r.mediaWg.Done()
		// A channel_command message cannot join or gate a chat task's input
		// batch, so clearing its own pending marker need not wait behind the
		// session's ordered remote-media queue.
		r.resolveAndBindMedia(set, inst, identity, chatMessageID, msg, sessionID, db.Issue{}, pgtype.Text{}, "", pgtype.UUID{}, false, deadline)
	}()
}

func (r *Router) enqueueMediaJob(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, issue db.Issue, issueDescriptionBase pgtype.Text, issueCommandText string, issueTaskID pgtype.UUID, resolveRemote bool, deadline time.Time) {
	key := keyForSession(sessionID)
	done := make(chan struct{})

	r.mediaQueueMu.Lock()
	if r.stopping {
		r.mediaQueueMu.Unlock()
		return
	}
	entry, ok := r.mediaQueues[key]
	var previous <-chan struct{}
	if !ok {
		entry = &mediaQueueEntry{}
		r.mediaQueues[key] = entry
	} else {
		previous = entry.tail
	}
	entry.tail = done
	r.mediaWg.Add(1)
	r.mediaQueueMu.Unlock()

	go func() {
		defer r.mediaWg.Done()
		defer close(done)
		defer r.finishMediaQueue(key, done)
		// Both queue waits are bounded by the message's own deadline, not
		// just global shutdown: in a media burst an already-expired job must
		// not keep holding its goroutine and payload until it reaches the
		// front — it skips straight to the empty finalize (marker clear +
		// promotion), which also unblocks the session's later messages.
		expiry := time.NewTimer(time.Until(deadline))
		defer expiry.Stop()
		expired := false
		if previous != nil {
			select {
			case <-previous:
			case <-r.mediaCtx.Done():
			case <-expiry.C:
				expired = true
			}
		}
		if !expired && resolveRemote {
			select {
			case r.mediaSem <- struct{}{}:
				defer func() { <-r.mediaSem }()
			case <-r.mediaCtx.Done():
				// Cancelled while queued for a slot: proceed without one.
				// ResolveMedia is skipped on the dead context and only the
				// bounded DB finalize runs, preserving prompt marker
				// clearing on shutdown.
			case <-expiry.C:
				// Expired while queued: no slot needed — resolveAndBindMedia
				// sees the dead deadline and runs only the empty finalize.
			}
		}
		r.resolveAndBindMedia(set, inst, identity, chatMessageID, msg, sessionID, issue, issueDescriptionBase, issueCommandText, issueTaskID, resolveRemote, deadline)
	}()
}

const mediaFinalizeTimeout = 5 * time.Second

func (r *Router) resolveAndBindMedia(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, issue db.Issue, issueDescriptionBase pgtype.Text, issueCommandText string, issueTaskID pgtype.UUID, resolveRemote bool, deadline time.Time) {
	ctx, cancel := context.WithDeadline(r.mediaCtx, deadline)
	defer cancel()

	resolved := msg
	if !resolveRemote {
		resolved.MediaRefs = nil
	} else if ctx.Err() == nil {
		// Skipped entirely when the budget expired while queued (or on
		// shutdown): resolving on a dead context would only churn through
		// intent writes that immediately fail.
		resolved = set.Media.ResolveMedia(ctx, inst, identity, sessionID, chatMessageID, msg)
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), mediaFinalizeTimeout)
	defer finalizeCancel()
	if err := ctx.Err(); resolveRemote && err != nil {
		// Refs resolved before the deadline already sit in object storage but
		// will not gain an attachment row. Nothing is deleted here — their
		// intent-ledger rows were written before the uploads, and the
		// reconciler reclaims unreferenced objects after the settle delay.
		resolved.MediaRefs = nil
		r.logger.Warn("channel router: media resolution incomplete; using placeholder",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"message_id", msg.MessageID,
			"error", err)
	}
	bindErr := set.Session.BindMedia(finalizeCtx, BindMediaParams{
		MessageID:            chatMessageID,
		SessionID:            sessionID,
		WorkspaceID:          inst.WorkspaceID,
		Sender:               identity.PrincipalUserID,
		IssueID:              issue.ID,
		IssueDescriptionBase: issueDescriptionBase,
		IssueCommandText:     issueCommandText,
		Body:                 resolved.Text,
		MediaRefs:            resolved.MediaRefs,
	})
	if bindErr != nil {
		// Never delete inline: the attachments may or may not have landed
		// (an ambiguous commit), but the intent rows are deleted in the SAME
		// transaction, so the ledger already reflects whichever outcome is
		// durable and the reconciler settles the objects.
		r.logger.Warn("channel router: media attachment binding failed",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"message_id", msg.MessageID,
			"err", bindErr)
	}
	if bindErr == nil && issue.ID.Valid && len(resolved.MediaRefs) > 0 {
		r.issues.PublishAttachmentsChanged(finalizeCtx, issue, identity.PrincipalUserID)
	}
	if issueTaskID.Valid {
		if err := r.tasks.PromoteDeferredChannelIssueTask(finalizeCtx, issueTaskID); err != nil {
			r.logger.Warn("channel router: media-ready issue task promotion failed",
				"channel_type", string(msg.Source.ChannelType),
				"event_id", msg.EventID,
				"message_id", msg.MessageID,
				"task_id", util.UUIDToString(issueTaskID),
				"err", err)
		}
	}
	if err := r.tasks.PromoteChannelChatTasksIfMediaReady(finalizeCtx, sessionID); err != nil {
		r.logger.Warn("channel router: media-ready task promotion failed",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"message_id", msg.MessageID,
			"err", err)
	}
}

func (r *Router) finishMediaQueue(key string, done chan struct{}) {
	r.mediaQueueMu.Lock()
	defer r.mediaQueueMu.Unlock()
	entry, ok := r.mediaQueues[key]
	if !ok || entry.tail != done {
		return
	}
	delete(r.mediaQueues, key)
}

func (r *Router) markPendingFresh(key string) {
	r.pendingFreshMu.Lock()
	defer r.pendingFreshMu.Unlock()
	r.pendingFresh[key] = true
}

func (r *Router) takePendingFresh(key string, fallback bool) bool {
	r.pendingFreshMu.Lock()
	defer r.pendingFreshMu.Unlock()
	fresh := fallback || r.pendingFresh[key]
	delete(r.pendingFresh, key)
	return fresh
}

// scheduleRun hands the per-session run trigger to the debouncer (or fires it
// inline when batching is disabled).
func (r *Router) scheduleRun(set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID, identity service.ChatTaskIdentity, taskContext []byte, suppressServerOutbound bool) {
	key := keyForSession(sessionID)
	fresh := msg.ForceFresh
	if r.batcher == nil {
		// Merge any pending bare-/new mark so the directive is honored even
		// when batching is disabled.
		r.flushChatRun(set, inst, msg, sessionID, identity, r.takePendingFresh(key, fresh), taskContext, suppressServerOutbound)
		return
	}
	if fresh {
		r.markPendingFresh(key)
	}
	flush := func() {
		r.flushChatRun(set, inst, msg, sessionID, identity, r.takePendingFresh(key, fresh), taskContext, suppressServerOutbound)
	}
	r.batcher.Schedule(key, flush)
}

// chatRunFlushTimeout bounds the detached flush (session reload + enqueue +
// notice), which runs on its own fresh context.
const chatRunFlushTimeout = 10 * time.Second

// flushChatRun is the debounced run-trigger: reload session, enqueue exactly
// one chat task for the window, and emit the offline/archived notice (only
// known here now) via the replier. Errors are logged, not returned.
func (r *Router) flushChatRun(set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID, identity service.ChatTaskIdentity, forceFresh bool, taskContext []byte, suppressServerOutbound bool) {
	ctx, cancel := context.WithTimeout(context.Background(), chatRunFlushTimeout)
	defer cancel()

	session, err := r.reader.GetChatSession(ctx, sessionID)
	if err != nil {
		r.logger.Error("channel router: flush reload chat session failed",
			"event", "channel_chat_run_reload_failed",
			"installation_id", uuidString(inst.ID),
			"chat_session_id", uuidString(sessionID),
			"message_id_hash", inboundTraceHash(msg.MessageID),
			"error", err,
		)
		if !suppressServerOutbound {
			r.clearTyping(ctx, set, sessionID)
		}
		return
	}
	if _, err := r.tasks.EnqueueChatTask(ctx, session, identity, forceFresh, taskContext); err != nil {
		// No task was enqueued, so no task lifecycle event will ever publish and
		// the platform's bus-driven typing clear can never fire. Clear the
		// indicator here (before any notice) so the "processing" reaction does
		// not stick on the user's message.
		if !suppressServerOutbound {
			r.clearTyping(ctx, set, sessionID)
		}
		switch {
		case errors.Is(err, service.ErrChatTaskAgentNoRuntime):
			if !suppressServerOutbound {
				r.emitFlushReply(ctx, set, inst, msg, sessionID, OutcomeAgentOffline)
			}
		case errors.Is(err, service.ErrChatTaskAgentArchived):
			if !suppressServerOutbound {
				r.emitFlushReply(ctx, set, inst, msg, sessionID, OutcomeAgentArchived)
			}
		default:
			r.logger.Error("channel router: flush enqueue chat task failed",
				"event", "channel_chat_run_enqueue_failed",
				"installation_id", uuidString(inst.ID),
				"chat_session_id", uuidString(sessionID),
				"message_id_hash", inboundTraceHash(msg.MessageID),
				"error", err,
			)
		}
		return
	}
	r.logger.Info("channel chat run enqueued",
		"event", "channel_chat_run_enqueued",
		"installation_id", uuidString(inst.ID),
		"chat_session_id", uuidString(sessionID),
		"message_id_hash", inboundTraceHash(msg.MessageID),
		"has_task_context", len(taskContext) > 0,
	)

	// The run is enqueued; if the agent is already at max_concurrent_tasks
	// the reply will not start until a slot frees, which can read as "the
	// bot ignored me". Send a rate-limited "queued" notice so the wait is
	// explained. Best-effort: a failed capacity read just skips the notice.
	if !suppressServerOutbound && r.agentAtCapacity(ctx, session.AgentID) && r.markBusyNotified(sessionID) {
		r.emitFlushReply(ctx, set, inst, msg, sessionID, OutcomeAgentBusy)
	}
}

// agentAtCapacity reports whether the agent has no free task slot right now.
func (r *Router) agentAtCapacity(ctx context.Context, agentID pgtype.UUID) bool {
	agent, err := r.reader.GetAgent(ctx, agentID)
	if err != nil {
		return false
	}
	if agent.MaxConcurrentTasks <= 0 {
		return false
	}
	running, err := r.reader.CountRunningTasks(ctx, agentID)
	if err != nil {
		return false
	}
	return running >= int64(agent.MaxConcurrentTasks)
}

// busyNoticeCooldown bounds how often one chat session gets the "queued"
// notice: a burst of messages while the agent is busy should read as one
// wait, not one notice per debounce window.
const busyNoticeCooldown = 90 * time.Second

// markBusyNotified reports whether a busy notice may fire for the session
// now, and records the send. Entries are pruned opportunistically.
func (r *Router) markBusyNotified(sessionID pgtype.UUID) bool {
	now := time.Now()
	key := keyForSession(sessionID)
	r.busyNoticeMu.Lock()
	defer r.busyNoticeMu.Unlock()
	if last, ok := r.busyNoticed[key]; ok && now.Sub(last) < busyNoticeCooldown {
		return false
	}
	for k, at := range r.busyNoticed {
		if now.Sub(at) >= busyNoticeCooldown {
			delete(r.busyNoticed, k)
		}
	}
	r.busyNoticed[key] = now
	return true
}

// clearTyping asks the platform to drop the "processing" indicator for a session
// whose flush produced no task run. A nil TypingNotifier (platform without the
// feature) is a no-op.
func (r *Router) clearTyping(ctx context.Context, set ResolverSet, sessionID pgtype.UUID) {
	if set.Typing != nil {
		set.Typing.OnSettled(ctx, sessionID)
	}
}

// emitFlushReply delivers an offline/archived notice for a flushed run.
func (r *Router) emitFlushReply(ctx context.Context, set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID, outcome Outcome) {
	if set.Replier == nil {
		return
	}
	set.Replier.Reply(ctx, inst, msg, Result{
		Outcome:        outcome,
		InstallationID: inst.ID,
		ChatSessionID:  sessionID,
		Sender:         msg.Source.SenderID,
	})
}

// scheduleReply detaches the OutboundReplier from the ACK critical path. The
// reply goroutine uses a fresh context with a ReplyTimeout deadline so it is
// independent of the inbound emit ctx (which the adapter cancels when its
// receive loop exits). A nil replier short-circuits — no goroutine.
func (r *Router) scheduleReply(set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, res Result) {
	if set.Replier == nil {
		return
	}
	r.replyWg.Add(1)
	go func() {
		defer r.replyWg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), r.replyTimeout)
		defer cancel()
		set.Replier.Reply(ctx, inst, msg, res)
		if ctx.Err() == context.DeadlineExceeded {
			r.logger.Warn("channel router: outbound reply timed out",
				"event_id", msg.EventID, "outcome", string(res.Outcome),
				"timeout", r.replyTimeout.String())
		}
	}()
}

// keyForSession is the batcher key. chat_session_id is globally unique.
func keyForSession(sessionID pgtype.UUID) string {
	return string(sessionID.Bytes[:])
}

type coordinatorChatWriter interface {
	CreateChatMessage(context.Context, db.CreateChatMessageParams) (db.ChatMessage, error)
	TouchChatSession(context.Context, pgtype.UUID) error
}

func (r *Router) persistCoordinatorAssistant(ctx context.Context, workspaceID, sessionID, agentID pgtype.UUID, decision inboundcoord.Decision) error {
	writer, ok := r.reader.(coordinatorChatWriter)
	text := strings.TrimSpace(decision.UserText)
	if !ok || text == "" || !sessionID.Valid {
		return nil
	}
	trace := decision.Trace()
	msg, err := writer.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: sessionID,
		Role:          "assistant",
		Content:       text,
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindCoordinator, Valid: true},
		ElapsedMs:     pgtype.Int8{Int64: decision.ElapsedMs, Valid: decision.ElapsedMs > 0},
		SourcePayload: decision.TraceJSON(),
	})
	if err != nil {
		return err
	}
	if err := writer.TouchChatSession(ctx, sessionID); err != nil {
		return err
	}
	if r.bus == nil || !workspaceID.Valid || !msg.ID.Valid {
		return nil
	}
	session := util.UUIDToString(sessionID)
	createdAt := ""
	if msg.CreatedAt.Valid {
		createdAt = msg.CreatedAt.Time.Format(time.RFC3339Nano)
	}
	r.bus.Publish(events.Event{
		Type:          protocol.EventChatMessage,
		WorkspaceID:   util.UUIDToString(workspaceID),
		ActorType:     "agent",
		ActorID:       util.UUIDToString(agentID),
		ChatSessionID: session,
		Payload: protocol.ChatMessagePayload{
			ChatSessionID: session,
			MessageID:     util.UUIDToString(msg.ID),
			Role:          "assistant",
			Content:       text,
			CreatedAt:     createdAt,
			MessageKind:   protocol.ChatMessageKindCoordinator,
			ElapsedMs:     decision.ElapsedMs,
			Coordinator:   &trace,
		},
	})
	return nil
}

func coordinatorSource(options HandleOptions, taskContext []byte, channelType channel.Type) inboundcoord.Source {
	if len(taskContext) > 0 {
		var payload struct {
			DispatchSource struct {
				Type string `json:"type"`
			} `json:"dispatch_source"`
		}
		if json.Unmarshal(taskContext, &payload) == nil {
			switch payload.DispatchSource.Type {
			case "digital_employee":
				return inboundcoord.SourceDigitalEmployee
			case "robot":
				return inboundcoord.SourceRobot
			}
		}
	}
	if options.IdentityOverride != nil {
		return inboundcoord.SourceDigitalEmployee
	}
	if channelType == "dingtalk" {
		return inboundcoord.SourceRobot
	}
	return inboundcoord.SourceRobot
}

func coordinatorDWSIdentity(taskContext []byte) (string, string) {
	if len(taskContext) == 0 {
		return "", ""
	}
	var payload struct {
		ExternalIdentity struct {
			DWS struct {
				UID   string `json:"uid"`
				OrgID string `json:"orgId"`
			} `json:"dws"`
		} `json:"external_identity"`
	}
	if json.Unmarshal(taskContext, &payload) != nil {
		return "", ""
	}
	uid := strings.TrimSpace(payload.ExternalIdentity.DWS.UID)
	orgID := strings.TrimSpace(payload.ExternalIdentity.DWS.OrgID)
	if uid == "" || orgID == "" {
		return "", ""
	}
	return uid, orgID
}

// coordinatorSenderName is the sender's display name from the dispatch event
// carried in the task context, so the coordinator turn (and its Langfuse
// trace) names the person rather than the DingTalk open id; the id is the
// fallback when the event carries no name.
func coordinatorSenderName(taskContext []byte, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(taskContext) == 0 {
		return fallback
	}
	var payload struct {
		EventData struct {
			Sender struct {
				DisplayName string `json:"displayName"`
			} `json:"sender"`
		} `json:"dispatch_event_data"`
	}
	if json.Unmarshal(taskContext, &payload) != nil {
		return fallback
	}
	if name := strings.TrimSpace(payload.EventData.Sender.DisplayName); name != "" {
		return name
	}
	return fallback
}

// ErrDedupFinalize marks a failed post-pipeline dedup transition. Callers must
// retry it only after the 60-second stale-claim window; an immediate retry
// would observe the still-live claim as a duplicate and could incorrectly
// finalize the durable inbox row without ever re-running the pipeline.
var ErrDedupFinalize = errors.New("channel router: dedup finalize failed")

// applyFinalize flips the in-flight claim row to its terminal state,
// token-fenced. A storage failure is part of the durable processing outcome:
// swallowing it would let the inbox declare success while the claim remains
// live and suppresses the retry as a duplicate.
func (r *Router) applyFinalize(ctx context.Context, set ResolverSet, instID pgtype.UUID, messageID string, claimToken pgtype.UUID, action dedupFinalize) error {
	var err error
	switch action {
	case finalizeMark:
		err = set.Dedup.Mark(ctx, instID, messageID, claimToken)
	case finalizeRelease:
		err = set.Dedup.Release(ctx, instID, messageID, claimToken)
	case finalizeNone:
	}
	if err != nil {
		return errors.Join(ErrDedupFinalize, err)
	}
	return nil
}

func (r *Router) drop(ctx context.Context, set ResolverSet, msg channel.InboundMessage, instID pgtype.UUID, reason DropReason) Result {
	_ = set.Audit.RecordDrop(ctx, instID, msg, reason)
	return Result{Outcome: OutcomeDropped, DropReason: reason, InstallationID: instID}
}

func (r *Router) createIssue(ctx context.Context, inst ResolvedInstallation, originType string, creatorUserID, originID pgtype.UUID, cmd IssueCommand, taskContext []byte, issuePrefix string, assignedRunFireAt time.Time) (service.IssueCreateResult, error) {
	if cmd.Title == "" {
		return service.IssueCreateResult{}, ErrEmptyIssueTitle
	}
	identityContextToken, err := taskIdentityContextToken(taskContext)
	if err != nil {
		return service.IssueCreateResult{}, fmt.Errorf("resolve task identity ContextToken: %w", err)
	}
	params := service.IssueCreateParams{
		WorkspaceID:               inst.WorkspaceID,
		Title:                     cmd.Title,
		Description:               pgtype.Text{String: cmd.Description, Valid: cmd.Description != ""},
		Status:                    "todo",
		Priority:                  "none",
		AssigneeType:              pgtype.Text{String: "agent", Valid: true},
		AssigneeID:                inst.AgentID,
		CreatorType:               "member",
		CreatorID:                 creatorUserID,
		OriginType:                pgtype.Text{String: originType, Valid: originType != ""},
		OriginID:                  originID,
		AgentIdentityContextToken: identityContextToken,
		DispatchContext:           append([]byte(nil), taskContext...),
	}
	// Without a BroadcastPayload the service emits its minimal
	// {"issue_id": ...} stub, and every issue:created consumer that reads the
	// "issue" key drops the event — most visibly the subscriber listener, which
	// needs id + creator_id and so never subscribed the person who typed
	// /issue to their own issue. IssueToMap is the shared renderer for events
	// published outside the HTTP handler; it stays key-compatible with the
	// IssueResponse that handler path broadcasts, so clients see one issue
	// shape regardless of which entry point created the issue.
	opts := service.IssueCreateOpts{
		AssignedAgentRunFireAt: assignedRunFireAt,
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
			return map[string]any{"issue": service.IssueToMap(issue, issuePrefix)}
		},
	}
	return r.issues.Create(ctx, params, opts)
}

func (r *Router) resolveDurableIssueCommand(ctx context.Context, sessionID pgtype.UUID, cmd IssueCommand) (IssueCommand, error) {
	if cmd.Title != "" {
		return cmd, nil
	}
	reader, ok := r.reader.(PreviousUserMessageReader)
	if !ok {
		return IssueCommand{}, errors.New("previous-message reader is not configured")
	}
	prev, err := reader.GetMostRecentUserChatMessage(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IssueCommand{}, ErrEmptyIssueTitle
		}
		return IssueCommand{}, fmt.Errorf("previous message lookup: %w", err)
	}
	cmd.Title = titleFromPreviousMessage(prev.Content)
	if cmd.Title == "" {
		return IssueCommand{}, ErrEmptyIssueTitle
	}
	return cmd, nil
}

func (r *Router) createOrRecoverDurableIssue(
	ctx context.Context,
	inst ResolvedInstallation,
	originType string,
	creatorUserID pgtype.UUID,
	messageID string,
	cmd IssueCommand,
	taskContext []byte,
	issuePrefix string,
	assignedRunFireAt time.Time,
) (service.IssueCreateResult, bool, error) {
	if strings.TrimSpace(messageID) == "" {
		return service.IssueCreateResult{}, false, errors.New("durable issue command has no message id")
	}
	reader, ok := r.reader.(IssueOriginReader)
	if !ok {
		return service.IssueCreateResult{}, false, errors.New("issue-origin reader is not configured")
	}
	originID := durableIssueCommandOriginID(inst.ID, messageID)
	existing, err := reader.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: inst.WorkspaceID,
		OriginType:  pgtype.Text{String: originType, Valid: originType != ""},
		OriginID:    originID,
	})
	if err == nil {
		return service.IssueCreateResult{Issue: existing}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return service.IssueCreateResult{}, false, fmt.Errorf("lookup issue by durable origin: %w", err)
	}
	created, err := r.createIssue(ctx, inst, originType, creatorUserID, originID, cmd, taskContext, issuePrefix, assignedRunFireAt)
	return created, false, err
}

func taskIdentityContextToken(taskContext []byte) (string, error) {
	if len(taskContext) == 0 {
		return "", nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(taskContext, &payload); err != nil {
		return "", fmt.Errorf("decode task context: %w", err)
	}
	raw, present := payload[protocol.AgentIdentityContextTokenJSONKey]
	rawExpiresAt, expiresAtPresent := payload[protocol.AgentIdentityContextTokenExpiresAtJSONKey]
	if !present && !expiresAtPresent {
		return "", nil
	}
	if !present {
		return "", errors.New("Agent Identity ContextToken expiry is present without a ContextToken")
	}
	if !expiresAtPresent {
		return "", errors.New("Agent Identity ContextToken expiry is missing")
	}
	var token string
	if err := json.Unmarshal(raw, &token); err != nil {
		return "", fmt.Errorf("decode Agent Identity ContextToken: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("Agent Identity ContextToken is empty")
	}
	var expiresAt int64
	if err := json.Unmarshal(rawExpiresAt, &expiresAt); err != nil {
		return "", fmt.Errorf("decode Agent Identity ContextToken expiry: %w", err)
	}
	if expiresAt <= 0 {
		return "", errors.New("Agent Identity ContextToken expiry is invalid")
	}
	return token, nil
}

func durableIssueCommandOriginID(installationID pgtype.UUID, messageID string) pgtype.UUID {
	sum := sha256.Sum256([]byte("multica:channel-issue:v1\x00" + uuidString(installationID) + "\x00" + messageID))
	var id [16]byte
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: id, Valid: true}
}

// issuePrefix reads the workspace's issue key (the "MUL" in MUL-42). A read
// failure is not worth failing issue creation over, so it degrades to empty
// and only the rendered identifier suffers.
func (r *Router) issuePrefix(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := r.reader.GetWorkspace(ctx, workspaceID)
	if err != nil {
		r.logger.Warn("channel engine: workspace lookup for issue prefix failed",
			"workspace_id", util.UUIDToString(workspaceID), "error", err)
		return ""
	}
	return ws.IssuePrefix
}

// ErrEmptyIssueTitle is a defensive invariant error. Router handles a
// user-authored empty title as OutcomeIssueUsage before calling createIssue.
var ErrEmptyIssueTitle = errors.New("issue title is empty")

var _ channel.InboundHandler = (*Router)(nil).Handle

func (r *Router) associateIssueConversation(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage, issue db.Issue, runID pgtype.UUID) {
	if r == nil || r.associator == nil || !issue.ID.Valid {
		return
	}
	cid := strings.TrimSpace(msg.Source.ChatID)
	if cid == "" {
		return
	}
	if err := r.associator.AssociateIssueConversation(ctx, assoc.AssociateInput{
		WorkspaceID:    uuidString(inst.WorkspaceID),
		AgentID:        uuidString(inst.AgentID),
		IssueID:        uuidString(issue.ID),
		IssueTitle:     issue.Title,
		Purpose:        issue.Title,
		RunID:          uuidString(runID),
		ConversationID: cid,
		EvidenceID:     strings.TrimSpace(msg.MessageID),
		PersonID:       strings.TrimSpace(msg.Source.SenderID),
		Kind:           string(msg.Source.ChatType),
	}); err != nil {
		r.logger.Error("channel engine: associate issue conversation failed",
			"event", "channel_issue_scene_associate_failed",
			"issue_id", uuidString(issue.ID),
			"conversation_id", cid,
			"error", err,
		)
		return
	}
	r.logger.Info("channel engine: associated issue conversation",
		"event", "channel_issue_scene_associated",
		"issue_id", uuidString(issue.ID),
		"conversation_id", cid,
	)
}

func inboundTraceHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// publishInboundMessage broadcasts a committed inbound message as chat:message,
// mirroring what handler.SendChatMessage does for the web send path. Without
// it the message is durable but silent: a web client watching the same chat
// only sees it on reload, and never at all when the follow-up task fails to
// enqueue (no chat:done ever lands either).
//
// Best-effort and nil-safe — a missing broadcast must never fail an ingest
// that has already committed.
func (r *Router) publishInboundMessage(workspaceID, sessionID, sender pgtype.UUID, res AppendResult) {
	if r == nil || r.bus == nil || !res.MessageID.Valid || !workspaceID.Valid {
		return
	}
	session := util.UUIDToString(sessionID)
	r.bus.Publish(events.Event{
		Type:          protocol.EventChatMessage,
		WorkspaceID:   util.UUIDToString(workspaceID),
		ActorType:     "member",
		ActorID:       util.UUIDToString(sender),
		ChatSessionID: session,
		Payload: protocol.ChatMessagePayload{
			ChatSessionID: session,
			MessageID:     util.UUIDToString(res.MessageID),
			Role:          "user",
			Content:       res.Content,
			CreatedAt:     res.CreatedAt.Time.Format(time.RFC3339Nano),
		},
	})
}
