package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

// routineDeliveryFixture admits and settles an actual scheduled occurrence.
// Its only outbound action is the unique routine end outbox.
func routineDeliveryFixture(t *testing.T, status string) (*employeeRoutineHandlerFixture, pgtype.UUID, dingtalkresponse.ActionInput) {
	t.Helper()
	x := newEmployeeRoutineHandlerFixture(t, "Send the requested message to this scene.")
	ctx := context.Background()
	run, err := x.f.h.AutopilotService.DispatchAutopilotForPlan(ctx, x.ap, x.trigger.ID, "schedule", nil, time.Now().UTC().Truncate(15*time.Minute))
	if err != nil || run == nil || !run.TaskID.Valid {
		t.Fatalf("dispatch: run=%+v err=%v", run, err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status=$2,completed_at=now(),result='{"output":"已向一粟发出询问（送达状态 delivered）"}'::jsonb WHERE id=$1`, run.TaskID, status); err != nil {
		t.Fatal(err)
	}
	if n, err := x.f.h.ReconcileEmployeeRoutineRuns(ctx, 100); err != nil || n != 1 {
		t.Fatalf("settle: n=%d err=%v", n, err)
	}
	var raw []byte
	actionID := x.noticeID(run.ID, dingtalkresponse.RoutineNoticeEnd)
	if err = testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE id=$1`, actionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err = json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	in.ActionID = actionID
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM sandbox_send_receipt WHERE agent_id=$1::uuid`, x.a.ID)
	})
	return x, run.TaskID, in
}

func routineDeliveryReceipt(t *testing.T, in dingtalkresponse.ActionInput, queue pgtype.UUID, state, messageID string) string {
	t.Helper()
	id := "routine-receipt-" + uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO sandbox_send_receipt(id,workspace_id,agent_id,task_id,client_action_id,input,payload_hash,target_conversation_id,observed_state,state,provider_conversation_id,provider_message_id)
 VALUES($1,$2::uuid,$3::uuid,$4,$1,jsonb_build_object('dws_uid',$5::text,'dws_org_id',$6::text),'hash',$7,$8,$8,$7,$9)`, id, in.WorkspaceID, in.AgentID, queue, in.DWSUID, in.DWSOrgID, in.ConversationID, state, messageID); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEmployeeRoutineDeliveryGuardRequiresExactVerifiedReceipt(t *testing.T) {
	cases := []struct {
		name, queueState, receiptState, extraState, scope string
		suppressed, deferred                              bool
	}{
		{name: "same_scene_delivered", receiptState: "delivered", suppressed: true},
		{name: "model_delivered_claim_without_receipt"},
		{name: "different_conversation", receiptState: "delivered", scope: "conversation"},
		{name: "different_uid", receiptState: "delivered", scope: "uid"},
		{name: "different_org", receiptState: "delivered", scope: "org"},
		{name: "different_task", receiptState: "delivered", scope: "task"},
		{name: "delivered_without_message_id", receiptState: "delivered", scope: "empty_message"},
		{name: "pending", receiptState: "pending", deferred: true},
		{name: "provider_accepted", receiptState: "provider_accepted", deferred: true},
		{name: "unknown", receiptState: "unknown"},
		{name: "failed_send", receiptState: "failed"},
		{name: "delivered_and_pending", receiptState: "delivered", extraState: "pending", deferred: true},
		{name: "delivered_and_unknown", receiptState: "delivered", extraState: "unknown"},
		{name: "delivered_and_failed", receiptState: "delivered", extraState: "failed"},
		{name: "failed_execution_after_delivery", queueState: "failed", receiptState: "delivered"},
		{name: "cancelled_execution_after_delivery", queueState: "cancelled", receiptState: "delivered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.queueState
			if status == "" {
				status = "completed"
			}
			x, queue, in := routineDeliveryFixture(t, status)
			if tc.receiptState != "" {
				receiptInput, receiptQueue, message := in, queue, "verified-message"
				switch tc.scope {
				case "conversation":
					receiptInput.ConversationID = "another-conversation"
				case "uid":
					receiptInput.DWSUID = "another-identity"
				case "org":
					receiptInput.DWSOrgID = "another-org"
				case "task":
					receiptQueue = parseUUID(uuid.NewString())
				case "empty_message":
					message = ""
				}
				routineDeliveryReceipt(t, receiptInput, receiptQueue, tc.receiptState, message)
			}
			if tc.extraState != "" {
				routineDeliveryReceipt(t, in, queue, tc.extraState, "")
			}
			err := x.f.h.BeforeEmployeeRunNoticeSend(context.Background(), in)
			var suppressed *dingtalkresponse.SuppressSendError
			if errors.As(err, &suppressed) != tc.suppressed {
				t.Fatalf("suppressed=%v want=%v err=%v", suppressed, tc.suppressed, err)
			}
			if tc.deferred && (err == nil || suppressed != nil) {
				t.Fatalf("pending receipt must defer submission: %v", err)
			}
			uncertain := tc.receiptState == "unknown" || tc.receiptState == "failed" || tc.extraState == "unknown" || tc.extraState == "failed"
			if uncertain && !tc.deferred {
				// The guard rewrites stale success claims, then forces a fresh
				// outbox read before submission rather than sending old text.
				if err == nil {
					t.Fatal("uncertain delivery did not force outbox reload")
				}
				var refreshed string
				if err := testPool.QueryRow(context.Background(), `SELECT input->>'text' FROM response_action WHERE id=$1`, in.ActionID).Scan(&refreshed); err != nil {
					t.Fatal(err)
				}
				if refreshed == "" || refreshed == in.Text {
					t.Fatal("uncertain delivery kept the stale success claim", refreshed)
				}
				in.Text = refreshed
				if err := x.f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); err != nil {
					t.Fatal("refreshed necessary explanation blocked", err)
				}
			} else if !tc.deferred && !tc.suppressed && err != nil {
				t.Fatalf("necessary final blocked: %v", err)
			}
			// Replaying settlement never deletes or creates another terminal outbox.
			if _, err := x.f.h.ReconcileEmployeeRoutineRuns(context.Background(), 100); err != nil {
				t.Fatal(err)
			}
			if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND input->>'routine_run_id'=$2`, x.a.ID, in.RoutineRunID); n != 1 {
				t.Fatal("terminal outbox count", n)
			}
		})
	}
}

func TestEmployeeRoutineDeliveryLateReceiptSuppressesBeforeProviderSubmission(t *testing.T) {
	x, queue, in := routineDeliveryFixture(t, "completed")
	receipt := routineDeliveryReceipt(t, in, queue, "pending", "")
	var suppressed *dingtalkresponse.SuppressSendError
	if err := x.f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); err == nil || errors.As(err, &suppressed) {
		t.Fatalf("pending send was not deferred: %v", err)
	}
	// Receipt commits after final enqueue, before the next fresh submission.
	if _, err := testPool.Exec(context.Background(), `UPDATE sandbox_send_receipt SET state='delivered',provider_message_id='late-verified-message' WHERE id=$1`, receipt); err != nil {
		t.Fatal(err)
	}
	p := &employeeNoticeProvider{}
	svc := dingtalkresponse.NewService(testPool, p, nil)
	svc.BeforeSend = x.f.h.BeforeEmployeeRunNoticeSend
	x.f.h.DingTalkResponses = svc
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
			t.Error("response worker did not stop")
		}
	})
	waitEmployeeNoticeAction(t, in.ActionID, "cancelled")
	if p.sends.Load() != 0 || p.queries.Load() != 0 {
		t.Fatalf("duplicate provider calls: send=%d query=%d", p.sends.Load(), p.queries.Load())
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE id=$1`, in.ActionID); n != 1 {
		t.Fatal("suppressed terminal outbox was removed", n)
	}
}

// A previously submitted final cannot be retroactively suppressed: an unknown
// provider result must keep its query-only recovery path, even with a late
// sandbox receipt proving that another business message arrived.
func TestEmployeeRoutineDeliveryLateReceiptKeepsSubmittedOutboxRecovery(t *testing.T) {
	x, queue, in := routineDeliveryFixture(t, "completed")
	routineDeliveryReceipt(t, in, queue, "delivered", "late-business-message")
	if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET state='unknown',provider_task_id='already-submitted',next_attempt_at=now() WHERE id=$1`, in.ActionID); err != nil {
		t.Fatal(err)
	}
	p := &employeeNoticeProvider{}
	svc := dingtalkresponse.NewService(testPool, p, nil)
	svc.BeforeSend = x.f.h.BeforeEmployeeRunNoticeSend
	x.f.h.DingTalkResponses = svc
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
			t.Error("response worker did not stop")
		}
	})
	waitEmployeeNoticeAction(t, in.ActionID, "delivered")
	if p.sends.Load() != 0 || p.queries.Load() != 1 {
		t.Fatalf("submitted outbox recovery: send=%d query=%d", p.sends.Load(), p.queries.Load())
	}
}
