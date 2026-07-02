package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// --- decode -----------------------------------------------------------------

func cardActionEnvelope(t *testing.T, value map[string]any) []byte {
	t.Helper()
	env := map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_id":   "evt_1",
			"event_type": cardActionEventType,
			"app_id":     "cli_test",
			"tenant_key": "tenant_1",
		},
		"event": map[string]any{
			"operator": map[string]any{"open_id": "ou_operator"},
			"token":    "delayed-token",
			"action": map[string]any{
				"tag":   "button",
				"value": value,
			},
			"context": map[string]any{
				"open_message_id": "om_card",
				"open_chat_id":    "oc_chat",
			},
		},
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

func TestDecodeCardActionPayload(t *testing.T) {
	payload := cardActionEnvelope(t, map[string]any{
		"action":  runCardCancelAction,
		"task_id": "11111111-1111-1111-1111-111111111111",
	})
	act, ok, err := DecodeCardActionPayload(payload)
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	if act.OperatorOpenID != "ou_operator" || act.OpenChatID != "oc_chat" || act.OpenMessageID != "om_card" {
		t.Fatalf("decoded routing fields wrong: %+v", act)
	}
	if act.EventID != "evt_1" || act.Token != "delayed-token" || act.Tag != "button" {
		t.Fatalf("decoded metadata wrong: %+v", act)
	}
	if got, _ := act.Value["action"].(string); got != runCardCancelAction {
		t.Fatalf("value.action = %q", got)
	}
}

func TestDecodeCardActionPayloadIgnoresOtherEvents(t *testing.T) {
	msg := []byte(`{"schema":"2.0","header":{"event_type":"im.message.receive_v1"},"event":{}}`)
	if _, ok, err := DecodeCardActionPayload(msg); ok || err != nil {
		t.Fatalf("message event must not decode as card action: ok=%v err=%v", ok, err)
	}
	if _, ok, err := DecodeCardActionPayload([]byte(`{"code":200}`)); ok || err != nil {
		t.Fatalf("heartbeat must not decode as card action: ok=%v err=%v", ok, err)
	}
	if _, ok, err := DecodeCardActionPayload(nil); ok || err != nil {
		t.Fatalf("empty payload: ok=%v err=%v", ok, err)
	}
}

// --- handler fakes ----------------------------------------------------------

type fakeCanceller struct {
	mu     sync.Mutex
	calls  []pgtype.UUID
	result db.AgentTaskQueue
	err    error
}

func (f *fakeCanceller) CancelTask(_ context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, taskID)
	if f.err != nil {
		return nil, f.err
	}
	out := f.result
	return &out, nil
}

type fakeActionQueries struct {
	store    *fakeRunCardStore
	bindings map[string]UserBinding    // keyed by installationID + "/" + open_id
	members  map[string]bool           // keyed by workspaceID + "/" + userID
	sessions map[string]db.ChatSession // keyed by chat_session_id
}

func (f *fakeActionQueries) GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error) {
	return f.store.GetAgentTask(ctx, id)
}

func (f *fakeActionQueries) GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error) {
	return f.store.GetIssue(ctx, id)
}

func (f *fakeActionQueries) GetLarkUserBindingByOpenID(_ context.Context, arg GetUserBindingByOpenIDParams) (UserBinding, error) {
	b, ok := f.bindings[uuidString(arg.InstallationID)+"/"+arg.ChannelUserID]
	if !ok {
		return UserBinding{}, pgx.ErrNoRows
	}
	return b, nil
}

func (f *fakeActionQueries) IsWorkspaceMember(_ context.Context, workspaceID, userID pgtype.UUID) (bool, error) {
	return f.members[uuidString(workspaceID)+"/"+uuidString(userID)], nil
}

func (f *fakeActionQueries) GetChatSession(_ context.Context, id pgtype.UUID) (db.ChatSession, error) {
	s, ok := f.sessions[uuidString(id)]
	if !ok {
		return db.ChatSession{}, pgx.ErrNoRows
	}
	return s, nil
}

type recordingReplier struct {
	mu      sync.Mutex
	replies []DispatchResult
}

func (r *recordingReplier) Reply(_ context.Context, _ Installation, _ InboundMessage, res DispatchResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, res)
}

type actionFixture struct {
	*runCardFixture
	queries   *fakeActionQueries
	canceller *fakeCanceller
	replier   *recordingReplier
	handler   *RunCardActionHandler
	inst      Installation
}

func newActionFixture(t *testing.T) *actionFixture {
	t.Helper()
	base := newRunCardFixture(t)
	installationID := "66666666-6666-6666-6666-666666666666"
	inst := base.store.installations[installationID]

	operatorUserID := uuidFromString(t, "77777777-7777-7777-7777-777777777777")
	fx := &actionFixture{
		runCardFixture: base,
		queries: &fakeActionQueries{
			store: base.store,
			bindings: map[string]UserBinding{
				installationID + "/ou_operator": {
					WorkspaceID:   inst.WorkspaceID,
					MulticaUserID: operatorUserID,
					ChannelUserID: "ou_operator",
				},
			},
			members: map[string]bool{
				uuidString(inst.WorkspaceID) + "/" + uuidString(operatorUserID): true,
			},
		},
		canceller: &fakeCanceller{},
		replier:   &recordingReplier{},
		inst:      inst,
	}
	// Cancelling flips the fake store's task to cancelled so the
	// refresh renders the terminal card, mirroring the real
	// TaskService side effect.
	fx.canceller.result = base.store.tasks[base.taskID]
	handler, err := NewRunCardActionHandler(RunCardActionHandlerConfig{
		Queries:   fx.queries,
		Tasks:     &cancellerWithSideEffect{inner: fx.canceller, fx: fx},
		Publisher: base.pub,
		Replier:   fx.replier,
		Logger:    newDiscardLogger(),
		Now:       func() time.Time { return fx.now },
		spawn:     func(fn func()) { fn() },
	})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	fx.handler = handler
	return fx
}

// cancellerWithSideEffect mimics TaskService.CancelTask flipping the
// row to cancelled before the publisher's refresh re-reads it.
type cancellerWithSideEffect struct {
	inner *fakeCanceller
	fx    *actionFixture
}

func (c *cancellerWithSideEffect) CancelTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	out, err := c.inner.CancelTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	c.fx.store.mu.Lock()
	task := c.fx.store.tasks[uuidString(taskID)]
	task.Status = "cancelled"
	task.CompletedAt = pgtype.Timestamptz{Time: c.fx.now, Valid: true}
	c.fx.store.tasks[uuidString(taskID)] = task
	c.fx.store.mu.Unlock()
	return out, nil
}

func (fx *actionFixture) cancelAction(eventID string) CardAction {
	return CardAction{
		EventID:        eventID,
		OperatorOpenID: "ou_operator",
		Tag:            "button",
		OpenChatID:     "oc_test_chat",
		OpenMessageID:  "om_run_card",
		Value: map[string]any{
			"action":  runCardCancelAction,
			"task_id": fx.taskID,
		},
	}
}

// --- handler tests ----------------------------------------------------------

func TestCardActionCancelsAndRefreshes(t *testing.T) {
	fx := newActionFixture(t)
	// Seed the card row as if the run card was already sent.
	fx.pub.handleEvent(fx.event("task:queued"))
	if len(fx.client.sends) != 1 {
		t.Fatalf("precondition: card send expected")
	}

	fx.handler.HandleCardAction(context.Background(), fx.inst, fx.cancelAction("evt_cancel_1"))

	fx.canceller.mu.Lock()
	calls := len(fx.canceller.calls)
	fx.canceller.mu.Unlock()
	if calls != 1 {
		t.Fatalf("cancel calls = %d, want 1", calls)
	}
	// The refresh re-renders from the (now cancelled) task row.
	if len(fx.client.patches) == 0 {
		t.Fatalf("refresh after cancel must patch the card")
	}
	last := fx.client.patches[len(fx.client.patches)-1].CardJSON
	if !strings.Contains(last, "已取消") || strings.Contains(last, runCardCancelAction) {
		t.Fatalf("refreshed card should be terminal without a cancel button: %s", last)
	}
}

func TestCardActionUnboundOperatorGetsBindingPrompt(t *testing.T) {
	fx := newActionFixture(t)
	act := fx.cancelAction("evt_unbound")
	act.OperatorOpenID = "ou_stranger"

	fx.handler.HandleCardAction(context.Background(), fx.inst, act)

	fx.canceller.mu.Lock()
	calls := len(fx.canceller.calls)
	fx.canceller.mu.Unlock()
	if calls != 0 {
		t.Fatalf("unbound operator must not cancel tasks")
	}
	fx.replier.mu.Lock()
	defer fx.replier.mu.Unlock()
	if len(fx.replier.replies) != 1 || fx.replier.replies[0].Outcome != OutcomeNeedsBinding {
		t.Fatalf("unbound operator should receive the binding prompt, got %+v", fx.replier.replies)
	}
	if fx.replier.replies[0].SenderOpenID != "ou_stranger" {
		t.Fatalf("binding prompt should target the tapper")
	}
}

func TestCardActionRejectsRevokedMember(t *testing.T) {
	fx := newActionFixture(t)
	// Binding row survives, but the user was removed from the
	// workspace: the membership re-check must block the cancel.
	fx.queries.members = map[string]bool{}

	fx.handler.HandleCardAction(context.Background(), fx.inst, fx.cancelAction("evt_revoked"))

	fx.canceller.mu.Lock()
	defer fx.canceller.mu.Unlock()
	if len(fx.canceller.calls) != 0 {
		t.Fatalf("revoked member must not cancel tasks")
	}
	fx.replier.mu.Lock()
	defer fx.replier.mu.Unlock()
	if len(fx.replier.replies) != 0 {
		t.Fatalf("revoked member should be dropped silently, got %+v", fx.replier.replies)
	}
}

func TestCardActionChatTaskOnlyCreatorCancels(t *testing.T) {
	fx := newActionFixture(t)
	fx.makeChatTask(t)
	creatorID := uuidFromString(t, "77777777-7777-7777-7777-777777777777") // = operator's Multica user
	fx.queries.sessions = map[string]db.ChatSession{
		fx.sessionID: {
			ID:          uuidFromString(t, fx.sessionID),
			WorkspaceID: fx.inst.WorkspaceID,
			CreatorID:   creatorID,
		},
	}

	fx.handler.HandleCardAction(context.Background(), fx.inst, fx.cancelAction("evt_chat_creator"))
	fx.canceller.mu.Lock()
	calls := len(fx.canceller.calls)
	fx.canceller.mu.Unlock()
	if calls != 1 {
		t.Fatalf("session creator must be able to cancel the chat task, calls = %d", calls)
	}

	// A different creator: the tapper is bound and a member, but not
	// the session owner — mirrors CancelTaskByUser's product rule.
	fx.queries.sessions[fx.sessionID] = db.ChatSession{
		ID:          uuidFromString(t, fx.sessionID),
		WorkspaceID: fx.inst.WorkspaceID,
		CreatorID:   uuidFromString(t, "88888888-8888-8888-8888-888888888888"),
	}
	fx.handler.HandleCardAction(context.Background(), fx.inst, fx.cancelAction("evt_chat_stranger"))
	fx.canceller.mu.Lock()
	defer fx.canceller.mu.Unlock()
	if len(fx.canceller.calls) != 1 {
		t.Fatalf("non-creator must not cancel a chat task, calls = %d", len(fx.canceller.calls))
	}
}

func TestCardActionRejectsWorkspaceMismatch(t *testing.T) {
	fx := newActionFixture(t)
	fx.store.mu.Lock()
	issue := fx.store.issues[fx.issueID]
	issue.WorkspaceID = uuidFromString(t, "99999999-9999-9999-9999-999999999999")
	fx.store.issues[fx.issueID] = issue
	fx.store.mu.Unlock()

	fx.handler.HandleCardAction(context.Background(), fx.inst, fx.cancelAction("evt_mismatch"))

	fx.canceller.mu.Lock()
	defer fx.canceller.mu.Unlock()
	if len(fx.canceller.calls) != 0 {
		t.Fatalf("cross-workspace cancel must be dropped")
	}
}

func TestCardActionDeduplicatesEvents(t *testing.T) {
	fx := newActionFixture(t)
	act := fx.cancelAction("evt_dup")
	fx.handler.HandleCardAction(context.Background(), fx.inst, act)
	fx.handler.HandleCardAction(context.Background(), fx.inst, act)

	fx.canceller.mu.Lock()
	defer fx.canceller.mu.Unlock()
	if len(fx.canceller.calls) != 1 {
		t.Fatalf("duplicate event must cancel once, got %d", len(fx.canceller.calls))
	}
}

func TestCardActionIgnoresUnknownActions(t *testing.T) {
	fx := newActionFixture(t)
	act := fx.cancelAction("evt_other")
	act.Value = map[string]any{"action": "something.else"}

	fx.handler.HandleCardAction(context.Background(), fx.inst, act)

	fx.canceller.mu.Lock()
	defer fx.canceller.mu.Unlock()
	if len(fx.canceller.calls) != 0 {
		t.Fatalf("unknown action must be ignored")
	}
}

func TestCardActionInvalidTaskID(t *testing.T) {
	fx := newActionFixture(t)
	act := fx.cancelAction("evt_bad_uuid")
	act.Value["task_id"] = "not-a-uuid"

	fx.handler.HandleCardAction(context.Background(), fx.inst, act)

	fx.canceller.mu.Lock()
	defer fx.canceller.mu.Unlock()
	if len(fx.canceller.calls) != 0 {
		t.Fatalf("invalid task id must not reach the canceller")
	}
}

func TestCardActionDedupePrunesExpired(t *testing.T) {
	fx := newActionFixture(t)
	for i := 0; i < 3; i++ {
		act := fx.cancelAction(fmt.Sprintf("evt_%d", i))
		fx.handler.HandleCardAction(context.Background(), fx.inst, act)
		fx.now = fx.now.Add(cardActionDedupeTTL + time.Minute)
	}
	fx.handler.mu.Lock()
	defer fx.handler.mu.Unlock()
	if len(fx.handler.seen) != 1 {
		t.Fatalf("expired dedupe entries should be pruned, %d left", len(fx.handler.seen))
	}
}
