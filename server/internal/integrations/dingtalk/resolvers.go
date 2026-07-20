package dingtalk

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/orgemphsf"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// This file is the DingTalk ResolverSet: the platform-specific seams the
// channel-agnostic engine.Router runs the inbound pipeline through. It
// mirrors the Slack ResolverSet and is built entirely on the generic
// channel_* queries (no new query, no schema change) plus the shared
// engine.ChatSession.

// originDingTalkChat is the issue.origin_type label for issues created via
// the DingTalk /issue command (migration 132 widens the CHECK).
const originDingTalkChat = "dingtalk_chat"

// NewDingTalkResolverSet assembles the DingTalk ResolverSet over the
// generated queries + a tx starter (for the shared session service). The
// replier delivers the outbound binding-prompt / status / issue-created
// notices; typing drives the "processing" emotion on ingested messages;
// auto resolves unbound org members through the corp directory. Each is
// optional — pass nil to disable.
func NewDingTalkResolverSet(q *db.Queries, tx engine.TxStarter, replier engine.OutboundReplier, typing engine.TypingNotifier, auto *AutoBinder, employees RobotEmployeeResolver) engine.ResolverSet {
	return engine.ResolverSet{
		Installation: &installationResolver{q: q},
		Identity:     &identityResolver{q: q, auto: auto},
		TaskContext:  &robotTaskContextResolver{q: q, employees: employees},
		Dedup:        &deduper{q: q},
		Session: &sessionBinder{session: engine.NewChatSession(q, tx, TypeDingtalk, engine.SessionTitles{
			Group:    "DingTalk group chat",
			Direct:   "DingTalk direct message",
			Fallback: "DingTalk chat",
		})},
		Audit:                  &auditor{q: q},
		Replier:                replier,
		Typing:                 typing,
		Unbind:                 &unbinder{q: q},
		OriginType:             originDingTalkChat,
		DurableRuns:            true,
		GroupSessionsPerSender: true,
	}
}

// RobotEmployeeResolver maps the current bot-message sender to the backend
// identity Agent Identity expects. Implementations must resolve each message;
// no Agent-bound identity or cached user profile is accepted here.
type RobotEmployeeResolver interface {
	ResolveEmployeeByCorpID(ctx context.Context, corpID, staffID string) (orgemphsf.Employee, error)
}

type robotTaskContextQueries interface {
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	GetAgentRuntime(ctx context.Context, id pgtype.UUID) (db.AgentRuntime, error)
}

type robotTaskContextResolver struct {
	q         robotTaskContextQueries
	employees RobotEmployeeResolver
}

func (r *robotTaskContextResolver) ResolveTaskContext(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) ([]byte, error) {
	startedAt := time.Now()
	log := slog.With(
		"installation_id", util.UUIDToString(inst.ID),
		"message_id_hash", dingtalkTraceHash(msg.MessageID),
		"sender_id_hash", dingtalkTraceHash(msg.Source.SenderID),
	)
	if r == nil || r.q == nil || r.employees == nil {
		log.Error("dingtalk robot DWS identity resolver unavailable",
			"event", "dingtalk_dws_identity_failed",
			"error_class", "configuration",
		)
		return nil, errors.New("DingTalk robot DWS identity resolver is not configured")
	}
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		log.Warn("dingtalk robot task context payload decode failed",
			"event", "dingtalk_task_context_failed",
			"error_class", "invalid_payload",
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		return nil, fmt.Errorf("decode DingTalk robot task context: %w", err)
	}
	taskContext := make(map[string]any)
	if raw.StreamSource != nil {
		source := protocol.DingTalkStreamSource{
			Hostname:     strings.TrimSpace(raw.StreamSource.Hostname),
			NodeID:       strings.TrimSpace(raw.StreamSource.NodeID),
			ConnectionID: strings.TrimSpace(raw.StreamSource.ConnectionID),
		}
		if source.Hostname == "" || source.NodeID == "" || source.ConnectionID == "" {
			return nil, errors.New("invalid DingTalk Stream source in task context")
		}
		taskContext[protocol.DingTalkStreamSourceJSONKey] = source
	}
	agent, err := r.q.GetAgent(ctx, inst.AgentID)
	if err != nil {
		log.Error("dingtalk robot DWS identity agent load failed",
			"event", "dingtalk_dws_identity_failed",
			"error_class", "database",
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		return nil, fmt.Errorf("load DingTalk robot agent: %w", err)
	}
	if !agent.RuntimeID.Valid {
		log.Info("dingtalk robot DWS identity skipped",
			"event", "dingtalk_dws_identity_skipped",
			"reason", "agent_without_runtime",
			"latency_ms", time.Since(startedAt).Milliseconds(),
		)
		return marshalDingTalkTaskContext(taskContext)
	}
	runtime, err := r.q.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		log.Error("dingtalk robot DWS identity runtime load failed",
			"event", "dingtalk_dws_identity_failed",
			"error_class", "database",
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		return nil, fmt.Errorf("load DingTalk robot runtime: %w", err)
	}
	if !service.FCE2BRuntimeHasCapability(runtime, "dws") {
		log.Info("dingtalk robot DWS identity skipped",
			"event", "dingtalk_dws_identity_skipped",
			"reason", "runtime_without_dws_capability",
			"latency_ms", time.Since(startedAt).Milliseconds(),
		)
		return marshalDingTalkTaskContext(taskContext)
	}
	log = log.With(
		"staff_id_hash", dingtalkTraceHash(raw.SenderStaffID),
		"corp_id_hash", dingtalkTraceHash(raw.SenderCorpID),
	)
	log.Info("dingtalk robot DWS identity resolving",
		"event", "dingtalk_dws_identity_started",
		"has_staff_id", strings.TrimSpace(raw.SenderStaffID) != "",
		"has_corp_id", strings.TrimSpace(raw.SenderCorpID) != "",
	)
	if strings.TrimSpace(raw.SenderStaffID) == "" || strings.TrimSpace(raw.SenderCorpID) == "" {
		log.Info("dingtalk robot DWS identity unavailable; continuing without DWS",
			"event", "dingtalk_dws_identity_unavailable",
			"error_class", "missing_organization_identity",
			"latency_ms", time.Since(startedAt).Milliseconds(),
		)
		taskContext[protocol.DingTalkRobotIdentityUnavailableJSONKey] = protocol.DingTalkRobotIdentityUnavailable{
			Reason: protocol.DingTalkRobotIdentityUnavailableMissingOrg,
		}
		return marshalDingTalkTaskContext(taskContext)
	}
	employee, err := r.employees.ResolveEmployeeByCorpID(ctx, raw.SenderCorpID, raw.SenderStaffID)
	if err != nil {
		var validationErr *orgemphsf.ValidationError
		if errors.As(err, &validationErr) {
			log.Warn("dingtalk robot DWS identity unavailable; continuing without DWS",
				"event", "dingtalk_dws_identity_unavailable",
				"error_class", "validation",
				"latency_ms", time.Since(startedAt).Milliseconds(),
				"error", err,
			)
			taskContext[protocol.DingTalkRobotIdentityUnavailableJSONKey] = protocol.DingTalkRobotIdentityUnavailable{
				Reason: protocol.DingTalkRobotIdentityUnavailableLookupError,
			}
			return marshalDingTalkTaskContext(taskContext)
		}
		log.Error("dingtalk robot DWS identity lookup failed; continuing without DWS",
			"event", "dingtalk_dws_identity_unavailable",
			"error_class", "employee_resolver",
			"latency_ms", time.Since(startedAt).Milliseconds(),
			"error", err,
		)
		taskContext[protocol.DingTalkRobotIdentityUnavailableJSONKey] = protocol.DingTalkRobotIdentityUnavailable{
			Reason: protocol.DingTalkRobotIdentityUnavailableLookupError,
		}
		return marshalDingTalkTaskContext(taskContext)
	}
	log.Info("dingtalk robot DWS identity resolved",
		"event", "dingtalk_dws_identity_resolved",
		"uid_hash", dingtalkTraceHash(employee.UID),
		"org_id_hash", dingtalkTraceHash(employee.OrgID),
		"latency_ms", time.Since(startedAt).Milliseconds(),
	)
	taskContext[protocol.DingTalkRobotIdentityJSONKey] = protocol.DingTalkRobotIdentity{
		UID:   employee.UID,
		OrgID: employee.OrgID,
	}
	return marshalDingTalkTaskContext(taskContext)
}

func marshalDingTalkTaskContext(taskContext map[string]any) ([]byte, error) {
	if len(taskContext) == 0 {
		return nil, nil
	}
	return json.Marshal(taskContext)
}

// dingtalkTraceHash returns a stable, non-reversible correlation key for SLS.
// Staff/corp/user/message identifiers remain queryable after an operator hashes
// the known value locally, while the raw identifiers never enter application
// logs. Sixteen hex characters are enough for operational correlation without
// turning the log field into a credential or identity store.
func dingtalkTraceHash(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:8])
}

var (
	_ engine.InstallationResolver = (*installationResolver)(nil)
	_ engine.IdentityResolver     = (*identityResolver)(nil)
	_ engine.Deduper              = (*deduper)(nil)
	_ engine.SessionBinder        = (*sessionBinder)(nil)
	_ engine.Auditor              = (*auditor)(nil)
	_ engine.SenderUnbinder       = (*unbinder)(nil)
)

// dingtalkBindingConfig is the opaque outbound routing persisted on the
// chat-session binding's config. For a DM the robot API addresses the
// recipient by staff id (not by conversation id), so the sender's staff id
// is captured at session creation. For a group the binding key is composite
// (conversation + sender) while replies still target the real conversation,
// so that openConversationId is persisted separately.
type dingtalkBindingConfig struct {
	SenderStaffID      string `json:"sender_staff_id,omitempty"`
	OpenConversationID string `json:"open_conversation_id,omitempty"`
}

// dingtalkSessionRouting derives the session-isolation key and the outbound
// routing config from one inbound message. A DM has one human and remains
// keyed by conversation. A group is keyed by conversation + sender so two
// people mentioning the same robot cannot share a deferred task, FC/E2B
// sandbox, or Hermes continuation session.
func dingtalkSessionRouting(msg channel.InboundMessage) (bindingKey string, config []byte) {
	var cfg dingtalkBindingConfig
	if msg.Source.ChatType == channel.ChatTypeP2P {
		if raw, err := decodeDingTalkRaw(msg); err == nil {
			cfg.SenderStaffID = raw.SenderStaffID
		}
		out, _ := json.Marshal(cfg)
		return msg.Source.ChatID, out
	}
	cfg.OpenConversationID = msg.Source.ChatID
	senderHash := sha256.Sum256([]byte(msg.Source.SenderID))
	bindingKey = fmt.Sprintf("%s:sender:v1:%x", msg.Source.ChatID, senderHash)
	out, _ := json.Marshal(cfg)
	return bindingKey, out
}

func nullText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// ---- installation routing ----

type installationQueries interface {
	GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

type installationResolver struct{ q installationQueries }

func (r *installationResolver) ResolveInstallation(ctx context.Context, msg channel.InboundMessage) (engine.ResolvedInstallation, error) {
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		return engine.ResolvedInstallation{}, err
	}
	// Durable callbacks are fenced to the installation row that admitted them.
	// Never follow app_id to a replacement row: a revoked bot can be reclaimed
	// by another workspace while an older callback is queued.
	installationID, parseErr := util.ParseUUID(raw.InstallationID)
	if parseErr != nil {
		return engine.ResolvedInstallation{}, fmt.Errorf("invalid admitted installation id: %w", parseErr)
	}
	inst, err := r.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          installationID,
		ChannelType: string(TypeDingtalk),
	})
	if err == nil {
		var cfg dingtalkInstallConfig
		if decodeErr := json.Unmarshal(inst.Config, &cfg); decodeErr != nil {
			return engine.ResolvedInstallation{}, fmt.Errorf("decode admitted installation config: %w", decodeErr)
		}
		if cfg.AppID != raw.ClientID {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
		return engine.ResolvedInstallation{}, err
	}
	return engine.ResolvedInstallation{
		ID:              inst.ID,
		WorkspaceID:     inst.WorkspaceID,
		AgentID:         inst.AgentID,
		InstallerUserID: inst.InstallerUserID,
		Active:          inst.Status == "active",
		Platform:        inst,
	}, nil
}

// ---- identity ----

// identityQueries is the narrow DB surface the resolver needs. *db.Queries
// satisfies it.
type identityQueries interface {
	GetChannelUserBindingByUserID(ctx context.Context, arg db.GetChannelUserBindingByUserIDParams) (db.ChannelUserBinding, error)
	GetMemberByUserAndWorkspace(ctx context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

type identityResolver struct {
	q identityQueries
	// auto resolves unbound senders through the corp directory; nil keeps
	// the explicit bind-prompt flow as the only path.
	auto *AutoBinder
}

func (r *identityResolver) ResolveSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (engine.ResolvedIdentity, error) {
	binding, err := r.q.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  msg.Source.SenderID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if r.auto != nil {
				// A directory match keeps the sender's own identity; the
				// customer-mode fallthrough only overrides the failures.
				id, autoErr := r.auto.Resolve(ctx, inst, msg)
				return r.applyAllowUnbound(inst, id, autoErr)
			}
			return r.applyAllowUnbound(inst, engine.ResolvedIdentity{}, engine.ErrSenderUnbound)
		}
		return engine.ResolvedIdentity{}, err
	}
	// Binding existence no longer proves membership (no FK); re-check.
	if _, err := r.q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      binding.MulticaUserID,
		WorkspaceID: inst.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return r.applyAllowUnbound(inst, engine.ResolvedIdentity{}, engine.ErrSenderNotMember)
		}
		return engine.ResolvedIdentity{}, err
	}
	return engine.ResolvedIdentity{UserID: binding.MulticaUserID}, nil
}

// applyAllowUnbound is the "connect an agent to external customers"
// fallthrough. When identity resolution ends in the two product-outcome
// sentinels (ErrSenderUnbound / ErrSenderNotMember) AND the installation
// opted into allow_unbound, the sender is served as the installer instead
// of being bounced to the bind prompt. Every other error (and the success
// case) passes through untouched, so a bound member always keeps their own
// identity and audit trail. The installer is by construction a workspace
// member, so this cannot smuggle in a non-member identity.
func (r *identityResolver) applyAllowUnbound(inst engine.ResolvedInstallation, id engine.ResolvedIdentity, err error) (engine.ResolvedIdentity, error) {
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, engine.ErrSenderUnbound) && !errors.Is(err, engine.ErrSenderNotMember) {
		return engine.ResolvedIdentity{}, err
	}
	if !installationAllowsUnbound(inst) || !inst.InstallerUserID.Valid {
		return engine.ResolvedIdentity{}, err
	}
	return engine.ResolvedIdentity{UserID: inst.InstallerUserID}, nil
}

// installationAllowsUnbound reports whether the installation config carries
// allow_unbound=true. Reads the raw config off the already-loaded platform
// row so no extra query is needed on the inbound path.
func installationAllowsUnbound(inst engine.ResolvedInstallation) bool {
	row, ok := inst.Platform.(db.ChannelInstallation)
	if !ok || len(row.Config) == 0 {
		return false
	}
	var cfg dingtalkInstallConfig
	if err := json.Unmarshal(row.Config, &cfg); err != nil {
		return false
	}
	return cfg.AllowUnbound
}

// ---- dedup ----

type deduper struct{ q *db.Queries }

func (r *deduper) Claim(ctx context.Context, installationID pgtype.UUID, messageID string) (pgtype.UUID, error) {
	claim, err := r.q.ClaimChannelInboundDedup(ctx, db.ClaimChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrDuplicate
		}
		return pgtype.UUID{}, err
	}
	return claim.ClaimToken, nil
}

func (r *deduper) Mark(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

func (r *deduper) Release(ctx context.Context, installationID pgtype.UUID, messageID string, claimToken pgtype.UUID) error {
	_, err := r.q.ReleaseChannelInboundDedup(ctx, db.ReleaseChannelInboundDedupParams{
		InstallationID: installationID,
		MessageID:      messageID,
		ClaimToken:     claimToken,
	})
	return err
}

// ---- session bind / append ----

type sessionBinder struct{ session *engine.ChatSession }

func (r *sessionBinder) EnsureSession(ctx context.Context, p engine.EnsureSessionParams) (pgtype.UUID, error) {
	bindingKey, config := dingtalkSessionRouting(p.Message)
	return r.session.EnsureSession(ctx, engine.EnsureSessionInput{
		WorkspaceID:    p.Installation.WorkspaceID,
		AgentID:        p.Installation.AgentID,
		InstallationID: p.Installation.ID,
		Sender:         p.Sender,
		BindingKey:     bindingKey,
		BindingConfig:  config,
		ChatType:       p.Message.Source.ChatType,
		// Group sessions take the group's real name so the Multica
		// session list (and the agent's thread name) says WHICH group,
		// not a generic "DingTalk group chat". DMs keep the static title.
		Title: dingtalkSessionTitle(p.Message),
	})
}

func (r *sessionBinder) AppendMessage(ctx context.Context, p engine.AppendParams) (engine.AppendResult, error) {
	return r.session.AppendUserMessage(ctx, engine.AppendInput{
		SessionID:      p.SessionID,
		WorkspaceID:    p.WorkspaceID,
		Sender:         p.Sender,
		InstallationID: p.InstallationID,
		Body:           dingtalkMessageBody(p.Message),
		// CommandText is the user's OWN typed text: the /issue parser must
		// see the bare message, not the speaker-labelled body.
		CommandText:  p.Message.Text,
		MessageID:    p.Message.MessageID,
		ThreadID:     p.Message.Source.ThreadID,
		ClaimToken:   p.ClaimToken,
		PreparedTask: p.PreparedTask,
	})
}

// dingtalkSessionTitle derives the chat_session title override: the group's
// conversation title when present. Empty (DMs, or a callback without a
// title) falls back to the engine's static SessionTitles.
func dingtalkSessionTitle(msg channel.InboundMessage) string {
	if msg.Source.ChatType != channel.ChatTypeGroup {
		return ""
	}
	raw, err := decodeDingTalkRaw(msg)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(raw.ConversationTitle)
}

// dingtalkMessageBody is the stored (and prompted) form of one inbound
// message. Group messages are prefixed with the speaker and the group name
// — "[张三 @ 项目群]: 内容" — because the agent's chat prompt is nothing but
// the concatenated message bodies: without the label it cannot tell WHO is
// talking or WHERE when several members address it in one window. DMs stay
// bare (a p2p session has exactly one human; mirrors the lark enricher).
// The DingTalk callback carries both fields for free — no extra API call.
func dingtalkMessageBody(msg channel.InboundMessage) string {
	text := msg.Text
	if text == "" || msg.Source.ChatType != channel.ChatTypeGroup {
		return text
	}
	raw, err := decodeDingTalkRaw(msg)
	if err != nil || raw.SenderNick == "" {
		return text
	}
	if title := strings.TrimSpace(raw.ConversationTitle); title != "" {
		return "[" + raw.SenderNick + " @ " + title + "]: " + text
	}
	return "[" + raw.SenderNick + "]: " + text
}

// ---- unbind ----

// unbindQueries is the narrow DB surface the unbinder needs. *db.Queries
// satisfies it.
type unbindQueries interface {
	DeleteChannelUserBinding(ctx context.Context, arg db.DeleteChannelUserBindingParams) (int64, error)
}

// unbinder implements the /unbind command: delete the sender's own binding
// on this installation. The key is the same platform sender id the identity
// lookup uses, so the reach is exactly "the identity you are speaking from".
type unbinder struct{ q unbindQueries }

func (r *unbinder) UnbindSender(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage) (bool, error) {
	deleted, err := r.q.DeleteChannelUserBinding(ctx, db.DeleteChannelUserBindingParams{
		InstallationID: inst.ID,
		ChannelUserID:  msg.Source.SenderID,
	})
	if err != nil {
		return false, err
	}
	return deleted > 0, nil
}

// ---- typing indicator ----

// dingtalkTypingNotifier adapts TypingIndicatorManager to the engine's
// TypingNotifier seam. Mirrors lark's feishuTypingNotifier.
type dingtalkTypingNotifier struct{ mgr *TypingIndicatorManager }

// NewTypingNotifier wraps the manager for the ResolverSet.
func NewTypingNotifier(mgr *TypingIndicatorManager) engine.TypingNotifier {
	return &dingtalkTypingNotifier{mgr: mgr}
}

func (n *dingtalkTypingNotifier) OnIngested(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, sessionID, taskID pgtype.UUID) {
	instRow, ok := inst.Platform.(db.ChannelInstallation)
	if !ok {
		return
	}
	raw, _ := decodeDingTalkRaw(msg) // best-effort; a decode miss just skips the age guard
	n.mgr.Add(ctx, instRow, sessionID, taskID, EmotionTarget{
		OpenConversationID: msg.Source.ChatID,
		OpenMsgID:          msg.MessageID,
	}, raw.CreateAt)
}

// OnSettled clears the emotion when the run trigger enqueued no task
// (agent offline / archived, or an enqueue failure) — the bus-driven
// clear on chat-done / task-failed never fires for those.
func (n *dingtalkTypingNotifier) OnSettled(ctx context.Context, sessionID pgtype.UUID) {
	n.mgr.Clear(ctx, sessionID)
}

// ---- audit ----

type auditor struct{ q *db.Queries }

func (r *auditor) RecordDrop(ctx context.Context, instID pgtype.UUID, msg channel.InboundMessage, reason engine.DropReason) error {
	raw, _ := decodeDingTalkRaw(msg) // best-effort; a decode miss still audits the drop
	return r.q.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ChannelType:      string(TypeDingtalk),
		EventType:        raw.Msgtype,
		DropReason:       string(reason),
		InstallationID:   instID,
		ChannelChatID:    nullText(msg.Source.ChatID),
		ChannelEventID:   nullText(msg.EventID),
		ChannelMessageID: nullText(msg.MessageID),
	})
}
