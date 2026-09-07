package dingtalkresponse

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func sandboxFixture() (ActionInput, protocol.DingTalkSendReceipt) {
	in := inputFixture()
	in.TaskID = uuid.NewString()
	in.IssueID = uuid.NewString()
	return in, protocol.DingTalkSendReceipt{ClientActionID: "client-action", PayloadHash: "payload-hash", State: "pending", OpenConversationID: in.ConversationID}
}

func sandboxState(t *testing.T, s *Service, in ActionInput) string {
	t.Helper()
	state, err := s.SandboxResponseState(context.Background(), in.WorkspaceID, in.AgentID, in.IssueID, in.TaskID, in.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func processSandbox(t *testing.T, s *Service) {
	t.Helper()
	worked, err := s.processSandboxOne(context.Background())
	if err != nil || !worked {
		t.Fatalf("sandbox process: %v %v", worked, err)
	}
}

func TestSandboxObservedDeliveryNeedsAuthoritativeQuery(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	s := NewService(pool, p, nil)
	in, receipt := sandboxFixture()
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	if sandboxState(t, s, in) != "pending" {
		t.Fatal("pending intent does not block duplicate wrap-up")
	}
	receipt.State = "delivered"
	receipt.OpenTaskID = "provider-task"
	receipt.OpenMessageID = "untrusted-message"
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	if sandboxState(t, s, in) != "provider_accepted" {
		t.Fatal("client delivery observation was treated as proof")
	}
	processSandbox(t, s)
	if sandboxState(t, s, in) != "delivered" || p.sends.Load() != 0 || p.queries.Load() != 1 {
		t.Fatalf("state=%s sends=%d queries=%d", sandboxState(t, s, in), p.sends.Load(), p.queries.Load())
	}
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT provider_message_id FROM sandbox_send_receipt`).Scan(&id); err != nil || id != "sent-message" {
		t.Fatalf("verified ID=%s err=%v", id, err)
	}
	receipt.State = "pending"
	receipt.OpenTaskID = ""
	receipt.OpenMessageID = ""
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	if sandboxState(t, s, in) != "delivered" {
		t.Fatal("late pending downgraded authoritative delivery")
	}
}

func TestSandboxUnknownTargetBlocksButOtherTargetDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name, cid, recipient string
		isGroup              bool
		want                 string
	}{
		{"unresolved", "", "", true, "pending"},
		{"other_group", "different-cid", "", true, ""},
		{"other_person", "", "other", false, ""},
		{"original_dm", "", "sender", false, "pending"},
		{"dm_from_group", "", "sender", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := responsePool(t)
			s := NewService(pool, &fakeProvider{}, nil)
			in, receipt := sandboxFixture()
			in.IsGroup = tc.isGroup
			receipt.OpenConversationID = tc.cid
			receipt.RecipientOpenDingTalkID = tc.recipient
			if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
				t.Fatal(err)
			}
			if got := sandboxState(t, s, in); got != tc.want {
				t.Fatalf("state=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestSandboxLedgerRejectsIntentAndProviderIDRebinding(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, &fakeProvider{}, nil)
	in, receipt := sandboxFixture()
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	changed := receipt
	changed.PayloadHash = "different"
	if err := s.RecordSandboxReceipt(context.Background(), in, changed); err == nil {
		t.Fatal("accepted changed payload")
	}
	changed = receipt
	changed.State = "accepted"
	changed.OpenTaskID = "provider-task"
	if err := s.RecordSandboxReceipt(context.Background(), in, changed); err != nil {
		t.Fatal(err)
	}
	changed.OpenTaskID = "other-task"
	if err := s.RecordSandboxReceipt(context.Background(), in, changed); err == nil {
		t.Fatal("rebound provider task")
	}
	changed = receipt
	changed.OpenConversationID = "other-cid"
	if err := s.RecordSandboxReceipt(context.Background(), in, changed); err == nil {
		t.Fatal("rebound conversation")
	}
	in.DWSUID = "456"
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err == nil {
		t.Fatal("rebound trusted identity")
	}
}

func TestSandboxQueryFailureDoesNotBecomeDelivery(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{query: func(context.Context, ActionInput, string) (dwsclient.SendStatus, error) {
		return dwsclient.SendStatus{State: "failed", ErrorCode: "rejected"}, nil
	}}
	s := NewService(pool, p, nil)
	in, receipt := sandboxFixture()
	receipt.State = "accepted"
	receipt.OpenTaskID = "provider-task"
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	processSandbox(t, s)
	if got := sandboxState(t, s, in); got != "failed" {
		t.Fatalf("state=%s", got)
	}
}

func TestSandboxAbandonedPendingTurnsUnknownWithoutSending(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	s := NewService(pool, p, nil)
	in, receipt := sandboxFixture()
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE sandbox_send_receipt SET created_at=now()-interval '16 minutes'`); err != nil {
		t.Fatal(err)
	}
	processSandbox(t, s)
	if sandboxState(t, s, in) != "unknown" || p.sends.Load() != 0 || p.queries.Load() != 0 {
		t.Fatal("abandoned intent was sent or treated as delivered")
	}
	if worked, err := s.processSandboxOne(context.Background()); worked || err != nil {
		t.Fatalf("unqueryable unknown keeps draining busy: %v %v", worked, err)
	}
}

func TestSandboxStateDoesNotCrossTaskOrIssue(t *testing.T) {
	pool := responsePool(t)
	s := NewService(pool, &fakeProvider{}, nil)
	in, receipt := sandboxFixture()
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	other := in
	other.TaskID = uuid.NewString()
	if got := sandboxState(t, s, other); got != "" {
		t.Fatal("cross-task receipt leaked")
	}
	other = in
	other.IssueID = uuid.NewString()
	if got := sandboxState(t, s, other); got != "" {
		t.Fatal("cross-issue receipt leaked")
	}
}

func TestSandboxDeliveryHookRetriesAfterRestartWithoutProviderCall(t *testing.T) {
	pool := responsePool(t)
	p := &fakeProvider{}
	s := NewService(pool, p, nil)
	in, receipt := sandboxFixture()
	receipt.State = "accepted"
	receipt.OpenTaskID = "provider-task"
	calls := 0
	hook := func(_ context.Context, got ActionInput, cid, messageID string) error {
		calls++
		if got.WorkspaceID != in.WorkspaceID || got.TaskID != in.TaskID || cid != in.ConversationID || messageID != "sent-message" {
			t.Fatalf("hook identity or evidence changed: %+v %s %s", got, cid, messageID)
		}
		if calls == 1 {
			return errors.New("association temporarily unavailable")
		}
		return nil
	}
	s.OnSandboxDelivered = hook
	if err := s.RecordSandboxReceipt(context.Background(), in, receipt); err != nil {
		t.Fatal(err)
	}
	processSandbox(t, s)
	if sandboxState(t, s, in) != "delivered" || calls != 1 || p.queries.Load() != 1 {
		t.Fatalf("first attempt state=%s calls=%d queries=%d", sandboxState(t, s, in), calls, p.queries.Load())
	}
	var completed bool
	if err := pool.QueryRow(context.Background(), `SELECT delivery_hook_completed_at IS NOT NULL FROM sandbox_send_receipt`).Scan(&completed); err != nil || completed {
		t.Fatalf("failed hook acknowledged: %v %v", completed, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE sandbox_send_receipt SET next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(pool, p, nil)
	restarted.OnSandboxDelivered = hook
	processSandbox(t, restarted)
	if calls != 2 || p.sends.Load() != 0 || p.queries.Load() != 1 {
		t.Fatalf("retry repeated provider: hooks=%d sends=%d queries=%d", calls, p.sends.Load(), p.queries.Load())
	}
	if err := pool.QueryRow(context.Background(), `SELECT delivery_hook_completed_at IS NOT NULL AND next_attempt_at IS NULL FROM sandbox_send_receipt`).Scan(&completed); err != nil || !completed {
		t.Fatalf("hook success not durable: %v %v", completed, err)
	}
}
