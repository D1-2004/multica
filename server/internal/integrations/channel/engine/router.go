package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
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
// identity + membership → ensure session → append+mark → /issue → debounced
// run trigger — then drives the detached outbound replier + typing indicator.
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
	replyWg      sync.WaitGroup

	logger *slog.Logger

	pendingFreshMu sync.Mutex
	pendingFresh   map[string]bool

	// busyNoticed rate-limits the OutcomeAgentBusy notice per chat session
	// (see markBusyNotified).
	busyNoticeMu sync.Mutex
	busyNoticed  map[string]time.Time
}

// Config tunes the Router. Zero values default.
type RouterConfig struct {
	// ReplyTimeout caps a single detached OutboundReplier.Reply / typing
	// call. It runs off the connector ACK path, so it must stay strictly
	// under the platform ACK deadline (Lark: 3s). Defaults to 2.5s.
	ReplyTimeout time.Duration
	Logger       *slog.Logger
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

func NewRouter(issues IssueCreator, tasks TaskEnqueuer, reader SessionReader, cfg RouterConfig) *Router {
	if cfg.ReplyTimeout == 0 {
		cfg.ReplyTimeout = 2500 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Router{
		sets:         make(map[channel.Type]ResolverSet),
		issues:       issues,
		tasks:        tasks,
		reader:       reader,
		replyTimeout: cfg.ReplyTimeout,
		logger:       cfg.Logger,
		pendingFresh: make(map[string]bool),
		busyNoticed:  make(map[string]time.Time),
	}
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

// Drain flushes debounced run triggers and joins in-flight reply goroutines.
// Call on shutdown AFTER the Supervisor has stopped delivering events.
func (r *Router) Drain() {
	if r.batcher != nil {
		r.batcher.FlushAll()
	}
	r.replyWg.Wait()
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
// committed routing result to synchronous HTTP adapters. Stream/Socket
// connectors should continue to call Handle when they only need an error.
func (r *Router) HandleResult(ctx context.Context, msg channel.InboundMessage) (Result, error) {
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

	res, inst, err := r.dispatch(ctx, set, msg)
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
	if res.Outcome == OutcomeIngested && set.Typing != nil {
		go func() {
			tctx, cancel := context.WithTimeout(context.Background(), r.replyTimeout)
			defer cancel()
			set.Typing.OnIngested(tctx, inst, msg, res.ChatSessionID, res.TaskID)
		}()
	}
	r.scheduleReply(set, inst, msg, res)
	return res, nil
}

// dispatch runs the pipeline and returns the typed result plus the resolved
// installation (needed by the outbound side). Mirrors lark.Dispatcher.Handle.
func (r *Router) dispatch(ctx context.Context, set ResolverSet, msg channel.InboundMessage) (Result, ResolvedInstallation, error) {
	// 1. Route to installation. The adapter maps the platform routing key
	//    (carried on the message) to its installation row. These drop
	//    branches run BEFORE the dedup claim because they have no valid
	//    installation to attach a claim to.
	inst, err := set.Installation.ResolveInstallation(ctx, msg)
	if err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			_ = set.Audit.RecordDrop(ctx, pgtype.UUID{}, msg, DropReasonInvalidEvent)
			return Result{Outcome: OutcomeDropped, DropReason: DropReasonInvalidEvent}, ResolvedInstallation{}, nil
		}
		return Result{}, ResolvedInstallation{}, fmt.Errorf("resolve installation: %w", err)
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

	res, finalize, err := r.processClaimed(ctx, set, msg, inst, claimToken)

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
func (r *Router) processClaimed(ctx context.Context, set ResolverSet, msg channel.InboundMessage, inst ResolvedInstallation, claimToken pgtype.UUID) (Result, dedupFinalize, error) {
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
	if set.Unbind != nil && ParseUnbindCommand(msg.Text) {
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

	// 4. Identity check: map the platform sender to a Multica user and
	//    re-verify workspace membership (no binding->member FK; MUL-3515 §4).
	identity, err := set.Identity.ResolveSender(ctx, inst, msg)
	if err != nil {
		switch {
		case errors.Is(err, ErrSenderUnbound):
			_ = set.Audit.RecordDrop(ctx, inst.ID, msg, DropReasonUnboundUser)
			return Result{
				Outcome:        OutcomeNeedsBinding,
				DropReason:     DropReasonUnboundUser,
				InstallationID: inst.ID,
				Sender:         msg.Source.SenderID,
			}, finalizeMark, nil
		case errors.Is(err, ErrSenderNotMember):
			return r.drop(ctx, set, msg, inst.ID, DropReasonNonWorkspaceMember), finalizeMark, nil
		default:
			return Result{}, finalizeRelease, fmt.Errorf("resolve sender: %w", err)
		}
	}

	// 5. Resolve the chat_session. Shared group sessions are created by the
	//    installer; p2p and sender-isolated group sessions by the sole human.
	sessionCreator := identity.PrincipalUserID
	if msg.Source.ChatType == channel.ChatTypeGroup && !set.GroupSessionsPerSender {
		sessionCreator = inst.InstallerUserID
	}
	sessionID, err := set.Session.EnsureSession(ctx, EnsureSessionParams{
		Installation: inst,
		Sender:       sessionCreator,
		Message:      msg,
	})
	if err != nil {
		// Single tx; an error rolled it back, nothing landed. Release.
		return Result{}, finalizeRelease, fmt.Errorf("ensure chat session: %w", err)
	}

	// 5b. A bare fresh-session directive (/new or /reset with no prompt) is
	//     consumed here: mark the session so the NEXT message starts a fresh
	//     agent session, and skip the append + run trigger — an empty prompt
	//     would burn a run on nothing (and some providers reject empty input
	//     outright). Durable channels persist the mark and dedup finalization in
	//     one transaction so any replica can consume it after a restart.
	if msg.ForceFresh && strings.TrimSpace(msg.Text) == "" {
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
				r.logger.Warn("channel router: task context rejected",
					"channel_type", string(msg.Source.ChannelType),
					"event_id", msg.EventID,
					"error", err,
				)
				return r.drop(ctx, set, msg, inst.ID, DropReasonTaskContextRejected), finalizeMark, nil
			}
			return Result{}, finalizeRelease, fmt.Errorf("resolve chat task context: %w", err)
		}
	}
	{
		var traceErr error
		taskContext, traceErr = chattrace.Merge(taskContext, trace)
		if traceErr != nil {
			return Result{}, finalizeRelease, fmt.Errorf("persist inbound chat trace: %w", traceErr)
		}
		chattrace.LogStage(r.logger, trace, "channel_task_context", "resolved",
			"installation_id", uuidString(inst.ID),
			"chat_session_id", uuidString(sessionID),
		)
	}
	r.logger.Info("channel router: chat task context resolved",
		"channel_type", string(msg.Source.ChannelType),
		"has_task_context", len(taskContext) > 0,
	)

	// DingTalk Stream must hand the callback off durably before its inbox row
	// can be finalized. Prepare the delayed task outside the append transaction
	// (the runtime overlay may perform network I/O), then let AppendMessage
	// commit task + message + dedup Mark atomically. Other channels retain the
	// existing in-memory batching path until they opt into the same contract.
	var preparedTask *service.PreparedChannelChatTask
	durableOutcome := OutcomeIngested
	durableFresh := false
	issueCommand, issueCommandRequested := ParseIssueCommand(msg.Text)
	var durableIssueResult *service.IssueCreateResult
	if set.DurableRuns && issueCommandRequested {
		resolvedCommand, err := r.resolveDurableIssueCommand(ctx, sessionID, *issueCommand)
		if err != nil {
			return Result{}, finalizeRelease, fmt.Errorf("resolve durable issue command: %w", err)
		}
		issueRes, recovered, err := r.createOrRecoverDurableIssue(
			ctx,
			inst,
			set.OriginType,
			identity.PrincipalUserID,
			msg.MessageID,
			resolvedCommand,
		)
		if err != nil {
			return Result{}, finalizeRelease, fmt.Errorf("create durable issue command: %w", err)
		}
		durableIssueResult = &issueRes
		if recovered {
			r.logger.Info("channel issue command recovered after retry",
				"event", "channel_issue_command_recovered",
				"channel_type", string(msg.Source.ChannelType),
				"installation_id", uuidString(inst.ID),
				"chat_session_id", uuidString(sessionID),
				"message_id_hash", inboundTraceHash(msg.MessageID),
				"issue_id", uuidString(issueRes.Issue.ID),
			)
		}
	}
	if set.DurableRuns && !issueCommandRequested {
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
			preparedTask = &prepared
		}
	}

	// 6. Append message + in-tx dedup Mark — the durable transition point.
	appendRes, err := set.Session.AppendMessage(ctx, AppendParams{
		SessionID:         sessionID,
		WorkspaceID:       inst.WorkspaceID,
		Sender:            identity.PrincipalUserID,
		InstallationID:    inst.ID,
		Installation:      inst,
		Message:           msg,
		ClaimToken:        claimToken,
		ForceFreshSession: set.DurableRuns && durableFresh,
		PreparedTask:      preparedTask,
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

	// 7. /issue command, if present. chat_message is already durable; all
	//    error returns from here signal finalizeNone (or the defensive Mark).
	if appendRes.IssueCommand != nil || durableIssueResult != nil {
		var issueRes service.IssueCreateResult
		if durableIssueResult != nil {
			issueRes = *durableIssueResult
		} else {
			issueRes, err = r.createIssue(ctx, inst, set.OriginType, identity.PrincipalUserID, sessionID, *appendRes.IssueCommand)
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
		res.IssueNumber = issueRes.Issue.Number
		res.IssueTitle = issueRes.Issue.Title
		if ws, werr := r.reader.GetWorkspace(ctx, inst.WorkspaceID); werr == nil && ws.IssuePrefix != "" {
			res.IssueIdentifier = fmt.Sprintf("%s-%d", ws.IssuePrefix, issueRes.Issue.Number)
		} else {
			res.IssueIdentifier = fmt.Sprintf("#%d", issueRes.Issue.Number)
		}
		issueTaskID := ""
		if issueRes.EnqueuedTask != nil {
			issueTaskID = uuidString(issueRes.EnqueuedTask.ID)
		}
		r.logger.Info("channel issue command created",
			"event", "channel_issue_command_created",
			"channel_type", string(msg.Source.ChannelType),
			"installation_id", uuidString(inst.ID),
			"chat_session_id", uuidString(sessionID),
			"message_id_hash", inboundTraceHash(msg.MessageID),
			"issue_id", uuidString(issueRes.Issue.ID),
			"task_id", issueTaskID,
		)
		// IssueService owns the assigned issue's task. Scheduling an additional
		// chat task for the command would run the agent twice.
		return res, postAppendFinalize, nil
	}

	if set.DurableRuns {
		if preparedTask != nil {
			chattrace.LogStage(r.logger, trace, "channel_task_persisted", "succeeded",
				"installation_id", uuidString(inst.ID),
				"chat_session_id", uuidString(sessionID),
				"task_id", uuidString(appendRes.TaskID),
				"message_id", uuidString(appendRes.MessageID),
			)
			r.logger.Info("channel chat task persisted with inbound message",
				"event", "channel_chat_task_persisted",
				"channel_type", string(msg.Source.ChannelType),
				"installation_id", uuidString(inst.ID),
				"chat_session_id", uuidString(sessionID),
				"message_id_hash", inboundTraceHash(msg.MessageID),
				"task_id", uuidString(appendRes.TaskID),
				"task_fire_at", appendRes.TaskFireAt.Time.UTC(),
				"outcome", string(durableOutcome),
			)
		} else {
			r.logger.Info("channel chat message persisted without runnable task",
				"event", "channel_chat_message_persisted_without_task",
				"channel_type", string(msg.Source.ChannelType),
				"installation_id", uuidString(inst.ID),
				"chat_session_id", uuidString(sessionID),
				"message_id_hash", inboundTraceHash(msg.MessageID),
				"outcome", string(durableOutcome),
			)
		}
		return res, postAppendFinalize, nil
	}

	// 8. Debounce the run trigger. The synchronous outcome is OutcomeIngested
	//    with no TaskID — the task row is created at flush. The resolved task
	//    identity keeps its authorization principal separate from attribution.
	//    THIS message's sender (the task initiator), deliberately not the
	//    session creator (group sessions are creator=installer). Latest sender
	//    in a window wins (MUL-2645).
	r.scheduleRun(set, inst, msg, sessionID, service.ChatTaskIdentity{
		PrincipalUserID: identity.PrincipalUserID,
		InitiatorUserID: identity.InitiatorUserID,
	}, taskContext)
	return res, postAppendFinalize, nil
}

// scheduleRun hands the per-session run trigger to the debouncer (or fires it
// inline when batching is disabled).
func (r *Router) scheduleRun(set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID, identity service.ChatTaskIdentity, taskContext []byte) {
	key := keyForSession(sessionID)
	fresh := msg.ForceFresh
	if r.batcher == nil {
		// Merge any pending bare-/new mark so the directive is honored even
		// when batching is disabled.
		r.flushChatRun(set, inst, msg, sessionID, identity, r.takePendingFresh(key, fresh), taskContext)
		return
	}
	if fresh {
		r.markPendingFresh(key)
	}
	flush := func() {
		r.flushChatRun(set, inst, msg, sessionID, identity, r.takePendingFresh(key, fresh), taskContext)
	}
	r.batcher.Schedule(key, flush)
}

// chatRunFlushTimeout bounds the detached flush (session reload + enqueue +
// notice), which runs on its own fresh context.
const chatRunFlushTimeout = 10 * time.Second

// flushChatRun is the debounced run-trigger: reload session, enqueue exactly
// one chat task for the window, and emit the offline/archived notice (only
// known here now) via the replier. Errors are logged, not returned.
func (r *Router) flushChatRun(set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID, identity service.ChatTaskIdentity, forceFresh bool, taskContext []byte) {
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
		r.clearTyping(ctx, set, sessionID)
		return
	}
	if _, err := r.tasks.EnqueueChatTask(ctx, session, identity, forceFresh, taskContext); err != nil {
		// No task was enqueued, so no task lifecycle event will ever publish and
		// the platform's bus-driven typing clear can never fire. Clear the
		// indicator here (before any notice) so the "processing" reaction does
		// not stick on the user's message.
		r.clearTyping(ctx, set, sessionID)
		switch {
		case errors.Is(err, service.ErrChatTaskAgentNoRuntime):
			r.emitFlushReply(ctx, set, inst, msg, sessionID, OutcomeAgentOffline)
		case errors.Is(err, service.ErrChatTaskAgentArchived):
			r.emitFlushReply(ctx, set, inst, msg, sessionID, OutcomeAgentArchived)
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
	if r.agentAtCapacity(ctx, session.AgentID) && r.markBusyNotified(sessionID) {
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

func (r *Router) createIssue(ctx context.Context, inst ResolvedInstallation, originType string, creatorUserID, originID pgtype.UUID, cmd IssueCommand) (service.IssueCreateResult, error) {
	if cmd.Title == "" {
		return service.IssueCreateResult{}, ErrEmptyIssueTitle
	}
	params := service.IssueCreateParams{
		WorkspaceID:  inst.WorkspaceID,
		Title:        cmd.Title,
		Description:  pgtype.Text{String: cmd.Description, Valid: cmd.Description != ""},
		Status:       "todo",
		Priority:     "none",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   inst.AgentID,
		CreatorType:  "member",
		CreatorID:    creatorUserID,
		OriginType:   pgtype.Text{String: originType, Valid: originType != ""},
		OriginID:     originID,
	}
	return r.issues.Create(ctx, params, service.IssueCreateOpts{})
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
	created, err := r.createIssue(ctx, inst, originType, creatorUserID, originID, cmd)
	return created, false, err
}

// durableIssueCommandOriginID is stable across retries and replicas while
// exposing neither the platform message ID nor credentials. The UUID version
// and variant bits are set to the RFC 4122 name-based shape so database/tooling
// treats the opaque digest as an ordinary UUID.
func durableIssueCommandOriginID(installationID pgtype.UUID, messageID string) pgtype.UUID {
	sum := sha256.Sum256([]byte("multica:channel-issue:v1\x00" + uuidString(installationID) + "\x00" + messageID))
	var id [16]byte
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: id, Valid: true}
}

// ErrEmptyIssueTitle is returned by createIssue when /issue has no title and
// the binder's previous-message fallback found nothing usable.
var ErrEmptyIssueTitle = errors.New("issue title is empty")

var _ channel.InboundHandler = (*Router)(nil).Handle

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
