package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// cardActionEventType is the v2 callback event Lark pushes when a user
// taps a callback-type button on an interactive card. It arrives on
// the SAME long connection as im.message.receive_v1 when the app's
// callback subscription mode is set to long connection in the
// developer console (事件与回调 → 回调配置 → 使用长连接接收回调 +
// card.action.trigger).
const cardActionEventType = "card.action.trigger"

// CardAction is the decoded card.action.trigger callback: who tapped
// which button on which card. Value carries the JSON object the card
// renderer embedded in the button's callback behavior.
type CardAction struct {
	EventID        string
	AppID          string
	TenantKey      string
	OperatorOpenID OpenID
	Tag            string
	Value          map[string]any
	OpenMessageID  string
	OpenChatID     string
	// Token is Lark's delayed-update token (30 min, 2 uses). Unused
	// today — the run-card flow patches by message_id instead — but
	// decoded so a future toast/inline-update path doesn't need a
	// protocol change.
	Token string
}

// DecodeCardActionPayload parses a long-conn data-frame payload as a
// card.action.trigger callback. Same contract shape as
// LarkJSONFrameDecoder.Decode: (action, true, nil) on a card action,
// (zero, false, nil) for any other event type, error only on malformed
// JSON that claimed to be a card action.
func DecodeCardActionPayload(payload []byte) (CardAction, bool, error) {
	if len(payload) == 0 {
		return CardAction{}, false, nil
	}
	var env larkEventEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return CardAction{}, false, nil
	}
	if env.Header.EventType != cardActionEventType {
		return CardAction{}, false, nil
	}
	if env.Event == nil {
		return CardAction{}, false, errors.New("card action with empty event payload")
	}
	var evt larkCardActionEvent
	if err := json.Unmarshal(env.Event, &evt); err != nil {
		return CardAction{}, false, fmt.Errorf("card action event: %w", err)
	}
	return CardAction{
		EventID:        env.Header.EventID,
		AppID:          env.Header.AppID,
		TenantKey:      env.Header.TenantKey,
		OperatorOpenID: OpenID(evt.Operator.OpenID),
		Tag:            evt.Action.Tag,
		Value:          evt.Action.Value,
		OpenMessageID:  evt.Context.OpenMessageID,
		OpenChatID:     evt.Context.OpenChatID,
		Token:          evt.Token,
	}, true, nil
}

// larkCardActionEvent mirrors the documented card.action.trigger event
// body (operator / action / context).
type larkCardActionEvent struct {
	Operator struct {
		OpenID string `json:"open_id"`
	} `json:"operator"`
	Token  string `json:"token"`
	Action struct {
		Tag   string         `json:"tag"`
		Value map[string]any `json:"value"`
	} `json:"action"`
	Context struct {
		OpenMessageID string `json:"open_message_id"`
		OpenChatID    string `json:"open_chat_id"`
	} `json:"context"`
}

// CardActionSink receives decoded card actions from the WS connector.
// Implementations MUST return quickly (the connector calls from its
// read loop, inside Lark's ~3s ack window) — do real work on a
// separate goroutine.
type CardActionSink interface {
	HandleCardAction(ctx context.Context, inst Installation, act CardAction)
}

// TaskCanceller is the narrow slice of service.TaskService the card
// action handler needs. Pinned as an interface so the lark package
// does not import the service package and tests substitute a fake.
type TaskCanceller interface {
	CancelTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error)
}

// RunCardActionQueries is the narrow DB surface the handler needs.
// *ChannelStore satisfies it.
type RunCardActionQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetLarkUserBindingByOpenID(ctx context.Context, arg GetUserBindingByOpenIDParams) (UserBinding, error)
	IsWorkspaceMember(ctx context.Context, workspaceID, userID pgtype.UUID) (bool, error)
}

// RunCardActionHandlerConfig wires the handler. Replier is optional:
// when present, an unbound operator tapping "终止运行" receives the
// standard binding-prompt card instead of silence.
type RunCardActionHandlerConfig struct {
	Queries   RunCardActionQueries
	Tasks     TaskCanceller
	Publisher *RunCardPublisher
	Replier   OutcomeReplier
	Logger    *slog.Logger
	Now       func() time.Time

	// spawn is a test seam; production default is `go fn()`.
	spawn func(fn func())
}

// RunCardActionHandler consumes card.action.trigger callbacks and
// executes the run card's terminate button: verify the operator is a
// bound workspace user, cancel the task, and refresh the card so the
// tapper sees the outcome. The connector's ack is decoupled — we ack
// 200 immediately (via return) and do the work async; the visible
// feedback is the card patching to 已取消, which the publisher emits
// off the task:cancelled bus event (or the explicit refresh when the
// task was already terminal).
type RunCardActionHandler struct {
	cfg RunCardActionHandlerConfig

	mu   sync.Mutex
	seen map[string]time.Time
}

// cardActionDedupeTTL bounds the in-memory event_id dedupe window.
// Lark re-pushes a callback when the ack is late; the WS lease
// guarantees one process per installation, so in-memory is enough.
const cardActionDedupeTTL = 5 * time.Minute

// NewRunCardActionHandler validates config and returns the handler.
func NewRunCardActionHandler(cfg RunCardActionHandlerConfig) (*RunCardActionHandler, error) {
	if cfg.Queries == nil || cfg.Tasks == nil || cfg.Publisher == nil {
		return nil, errors.New("lark card action handler: Queries, Tasks and Publisher are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.spawn == nil {
		cfg.spawn = func(fn func()) { go fn() }
	}
	return &RunCardActionHandler{
		cfg:  cfg,
		seen: make(map[string]time.Time),
	}, nil
}

// HandleCardAction implements CardActionSink. Fast path: discriminate
// + dedupe under a mutex, then hand off to a bounded goroutine.
func (h *RunCardActionHandler) HandleCardAction(_ context.Context, inst Installation, act CardAction) {
	action, _ := act.Value["action"].(string)
	if action != runCardCancelAction {
		h.cfg.Logger.Debug("lark card action: unhandled action", "action", action, "tag", act.Tag)
		return
	}
	if act.EventID != "" && !h.claimEvent(act.EventID) {
		return
	}
	h.cfg.spawn(func() {
		// Detached context: the WS connection dropping mid-cancel must
		// not abort the cancel the user already asked for.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h.process(ctx, inst, act)
	})
}

// claimEvent returns true the first time an event_id is seen inside
// the TTL window, pruning expired entries opportunistically.
func (h *RunCardActionHandler) claimEvent(eventID string) bool {
	now := h.cfg.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, at := range h.seen {
		if now.Sub(at) > cardActionDedupeTTL {
			delete(h.seen, id)
		}
	}
	if _, dup := h.seen[eventID]; dup {
		return false
	}
	h.seen[eventID] = now
	return true
}

func (h *RunCardActionHandler) process(ctx context.Context, inst Installation, act CardAction) {
	log := h.cfg.Logger
	rawTaskID, _ := act.Value["task_id"].(string)
	taskID, err := util.ParseUUID(rawTaskID)
	if err != nil {
		log.Warn("lark card action: invalid task_id in button value",
			"task_id", rawTaskID, "operator", string(act.OperatorOpenID))
		return
	}

	// Permission gate: the tapper must be a bound workspace user on
	// this installation — the same bar the inbound /issue path
	// enforces (binding lookup AND a live membership re-check; a
	// binding row alone is not membership proof since MUL-3515
	// dropped the channel_* FKs). Unbound tappers get the standard
	// binding prompt (p2p) when a replier is wired; revoked members
	// are dropped silently.
	binding, berr := h.cfg.Queries.GetLarkUserBindingByOpenID(ctx, GetUserBindingByOpenIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  string(act.OperatorOpenID),
	})
	if berr != nil {
		if errors.Is(berr, pgx.ErrNoRows) {
			log.Info("lark card action: unbound operator; prompting to bind",
				"operator", string(act.OperatorOpenID), "task_id", rawTaskID)
			if h.cfg.Replier != nil {
				h.cfg.Replier.Reply(ctx, inst, InboundMessage{ChatID: ChatID(act.OpenChatID)}, DispatchResult{
					Outcome:        OutcomeNeedsBinding,
					InstallationID: inst.ID,
					SenderOpenID:   act.OperatorOpenID,
				})
			}
			return
		}
		log.Warn("lark card action: binding lookup failed", "error", berr)
		return
	}
	isMember, merr := h.cfg.Queries.IsWorkspaceMember(ctx, inst.WorkspaceID, binding.MulticaUserID)
	if merr != nil {
		log.Warn("lark card action: membership check failed", "task_id", rawTaskID, "error", merr)
		return
	}
	if !isMember {
		log.Warn("lark card action: operator is no longer a workspace member; dropping cancel",
			"operator", string(act.OperatorOpenID), "task_id", rawTaskID)
		return
	}

	// Scope gate: the task must belong to this installation's
	// workspace. The button value is authored by us, but validate
	// anyway — the value survives forwards and Lark-side storage.
	task, terr := h.cfg.Queries.GetAgentTask(ctx, taskID)
	if terr != nil {
		log.Warn("lark card action: load task failed", "task_id", rawTaskID, "error", terr)
		return
	}
	if !task.IssueID.Valid {
		log.Warn("lark card action: task has no issue", "task_id", rawTaskID)
		return
	}
	issue, ierr := h.cfg.Queries.GetIssue(ctx, task.IssueID)
	if ierr != nil {
		log.Warn("lark card action: load issue failed", "task_id", rawTaskID, "error", ierr)
		return
	}
	if issue.WorkspaceID != inst.WorkspaceID {
		log.Warn("lark card action: workspace mismatch; dropping cancel",
			"task_id", rawTaskID,
			"issue_workspace", uuidString(issue.WorkspaceID),
			"installation_workspace", uuidString(inst.WorkspaceID))
		return
	}

	if _, cerr := h.cfg.Tasks.CancelTask(ctx, taskID); cerr != nil {
		log.Warn("lark card action: cancel task failed", "task_id", rawTaskID, "error", cerr)
		// Fall through to the refresh: re-rendering from DB shows the
		// user the truth either way.
	} else {
		log.Info("lark card action: task cancel requested",
			"task_id", rawTaskID, "operator", string(act.OperatorOpenID))
	}

	// The task:cancelled bus event patches the card for live tasks;
	// the explicit refresh covers already-terminal tasks (no event
	// fires) and doubles as a safety net if the event was missed.
	h.cfg.Publisher.RefreshCard(rawTaskID)
}
