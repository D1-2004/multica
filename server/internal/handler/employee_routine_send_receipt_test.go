package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func routineSendHTTPFixture(t *testing.T) (*employeeRoutineHandlerFixture, *db.AutopilotRun, *dingTalkResponseFixture, string) {
	t.Helper()
	x := newEmployeeRoutineHandlerFixture(t, "Send one useful message to this scene.")
	prepareNumericRoutineSendIdentity(t, x.f, &x.a)
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_dws_native_subscription(agent_id,workspace_id,enabled_by,dws_uid,org_id) VALUES($1::uuid,$2::uuid,$3::uuid,'424242','2002')`, x.a.ID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dws_native_subscription WHERE agent_id=$1::uuid`, x.a.ID)
	})
	run, err := x.f.h.AutopilotService.DispatchAutopilotForPlan(context.Background(), x.ap, x.trigger.ID, "schedule", nil, time.Now().UTC().Truncate(15*time.Minute))
	if err != nil || run == nil || !run.TaskID.Valid {
		t.Fatalf("dispatch: run=%+v err=%v", run, err)
	}
	f := &dingTalkResponseFixture{h: x.f.h, taskID: uuidToString(run.TaskID), agentID: x.a.ID}
	token := f.taskToken(t, f.taskID, f.agentID, testWorkspaceID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM sandbox_send_receipt WHERE agent_id=$1::uuid`, x.a.ID)
	})
	return x, run, f, token
}

func TestEmployeeRoutineSendReceiptHTTPUsesCommittedOriginAndTaskToken(t *testing.T) {
	for _, name := range []string{"valid", "missing_origin", "cross_tenant_rebinding", "forged_body_scope", "wrong_task_token", "changed_prompt"} {
		t.Run(name, func(t *testing.T) {
			x, run, f, token := routineSendHTTPFixture(t)
			body := any(validReceiptBody())
			path := f.taskID
			var err error
			switch name {
			case "missing_origin":
				_, err = testPool.Exec(context.Background(), `DELETE FROM employee_routine_occurrence WHERE autopilot_run_id=$1`, run.ID)
			case "cross_tenant_rebinding":
				_, err = testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id='another-tenant' WHERE agent_id=$1::uuid`, x.a.ID)
			case "forged_body_scope":
				body = map[string]any{"clientActionId": "forged", "state": "pending", "payloadHash": strings.Repeat("a", 64), "workspaceId": uuid.NewString(), "dwsUid": "forged-identity"}
			case "wrong_task_token":
				path = uuid.NewString()
			case "changed_prompt":
				_, err = testPool.Exec(context.Background(), `UPDATE agent_task_queue SET context=jsonb_set(context,'{direct_task_prompt}','"forged prompt"') WHERE id=$1`, run.TaskID)
			}
			if err != nil {
				t.Fatal(err)
			}
			w := receiptRequest(t, f.receiptRouter(), token, path, body)
			want := http.StatusConflict
			switch name {
			case "valid":
				want = http.StatusOK
			case "forged_body_scope":
				want = http.StatusBadRequest
			case "wrong_task_token":
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatalf("HTTP %d want %d: %s", w.Code, want, w.Body.String())
			}
			count := x.count(t, `SELECT count(*) FROM sandbox_send_receipt WHERE task_id=$1`, run.TaskID)
			if name != "valid" {
				if count != 0 {
					t.Fatal("rejected request wrote receipt", count)
				}
				return
			}
			if count != 1 {
				t.Fatal("accepted receipt count", count)
			}
			var raw []byte
			if err := testPool.QueryRow(context.Background(), `SELECT input FROM sandbox_send_receipt WHERE task_id=$1`, run.TaskID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var in dingtalkresponse.ActionInput
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			task, err := f.h.Queries.GetAgentTask(context.Background(), run.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			trusted, ok, err := f.h.employeeRoutineSendInput(context.Background(), task)
			if err != nil || !ok || in.TaskID != f.taskID || in.AgentID != x.a.ID || in.WorkspaceID != testWorkspaceID || in.DWSUID != trusted.DWSUID || in.DWSOrgID != trusted.DWSOrgID || in.ConversationID != trusted.ConversationID || in.DWSEnvironment != trusted.DWSEnvironment || in.DWSEnvironment != nativeDWSEnvironment {
				t.Fatalf("receipt lost committed scope: %+v trusted=%+v err=%v", in, trusted, err)
			}
		})
	}
}

func TestEmployeeRoutineSendReceiptClaimRequiresManagedCapabilities(t *testing.T) {
	for _, caps := range []string{protocol.DaemonCapabilityEmployeeDirectV1 + "," + protocol.DWSMessagePolicyCapability, protocol.DaemonCapabilityEmployeeDirectV1, protocol.DWSMessagePolicyCapability, ""} {
		t.Run(caps, func(t *testing.T) {
			x, run, _, _ := routineSendHTTPFixture(t)
			var task *db.AgentTaskQueue
			deadline := time.Now().Add(2 * time.Second)
			for task == nil && time.Now().Before(deadline) {
				var err error
				task, err = x.f.h.TaskService.ClaimTaskForRuntime(context.Background(), x.runtime.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{x.runtime.ID}})
				if err != nil {
					t.Fatal(err)
				}
				if task == nil {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if task == nil || task.ID != run.TaskID {
				t.Fatalf("routine claim unavailable: task=%+v state=%+v", task, x.queueState(t, run.TaskID))
			}
			req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID)
			req.Header.Set("X-Client-Capabilities", caps)
			resp, _, _, _, failure := x.f.h.buildClaimedTaskResponse(req, task, x.runtime, "", uuidToString(x.runtime.ID), testWorkspaceID)
			capable := strings.Contains(caps, protocol.DWSMessagePolicyCapability) && strings.Contains(caps, protocol.DaemonCapabilityEmployeeDirectV1)
			if capable {
				if failure != nil || resp.DingTalkMessagePolicy == nil || !resp.DingTalkMessagePolicy.PlatformManagedLifecycle {
					t.Fatalf("capable claim lost receipt policy: failure=%+v policy=%+v", failure, resp.DingTalkMessagePolicy)
				}
			} else if failure == nil {
				t.Fatal("incapable daemon received routine without durable send intent policy")
			}
		})
	}
}

func TestEmployeeRoutineSendReceiptHTTPVerifiedQuerySuppressesFinal(t *testing.T) {
	x, run, f, token := routineSendHTTPFixture(t)
	task, err := f.h.Queries.GetAgentTask(context.Background(), run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	trusted, ok, err := f.h.employeeRoutineSendInput(context.Background(), task)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	p := &employeeNoticeProvider{}
	svc := dingtalkresponse.NewService(testPool, p, nil)
	svc.BeforeSend = f.h.BeforeEmployeeRunNoticeSend
	f.h.DingTalkResponses = svc
	body := validReceiptBody()
	body.OpenConversationID = trusted.ConversationID
	if w := receiptRequest(t, f.receiptRouter(), token, f.taskID, body); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body.State, body.OpenTaskID, body.OpenMessageID = "delivered", "sandbox-provider-task", "client-unverified-id"
	if w := receiptRequest(t, f.receiptRouter(), token, f.taskID, body); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var state, message string
	if err := testPool.QueryRow(context.Background(), `SELECT state,provider_message_id FROM sandbox_send_receipt WHERE task_id=$1`, run.TaskID).Scan(&state, &message); err != nil {
		t.Fatal(err)
	}
	if state != "provider_accepted" || message != "" {
		t.Fatalf("client delivery claim became verified: %s %q", state, message)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed',completed_at=now(),result='{"output":"已发出（delivered）"}' WHERE id=$1`, run.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeRoutineRuns(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	actionID := x.noticeID(run.ID, dingtalkresponse.RoutineNoticeEnd)
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT input FROM response_action WHERE id=$1`, actionID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	in.ActionID = actionID
	var suppressed *dingtalkresponse.SuppressSendError
	if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); err == nil || errors.As(err, &suppressed) {
		t.Fatalf("unverified client claim did not defer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !svc.WaitWithTimeout(context.Background(), 5*time.Second) {
			t.Error("response worker did not stop")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := testPool.QueryRow(context.Background(), `SELECT state,provider_message_id FROM sandbox_send_receipt WHERE task_id=$1`, run.TaskID).Scan(&state, &message); err != nil {
			t.Fatal(err)
		}
		if state == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider verification timed out", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if message != "notice-provider-message" || p.queries.Load() < 1 {
		t.Fatal("provider verification was not authoritative", message, p.queries.Load())
	}
	if err := f.h.BeforeEmployeeRunNoticeSend(context.Background(), in); !errors.As(err, &suppressed) {
		t.Fatalf("verified same scene delivery did not suppress: %v", err)
	}
	// The first worker pass can defer before the concurrent receipt query commits.
	if _, err := testPool.Exec(context.Background(), `UPDATE response_action SET next_attempt_at=now() WHERE id=$1 AND state='pending'`, actionID); err != nil {
		t.Fatal(err)
	}
	svc.Notify()
	waitEmployeeNoticeAction(t, actionID, "cancelled")
	if p.sends.Load() != 0 {
		t.Fatal("redundant final reached provider", p.sends.Load())
	}
}

func TestEmployeeRoutineSendReceiptMixedReplicaDefersAcceptedOccurrence(t *testing.T) {
	x, run, _, _ := routineSendHTTPFixture(t)
	// The occurrence was admitted while all readers were ready. A rolling
	// downgrade before claim must retain that accepted work for a later retry.
	x.f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return errors.New("one live replica lacks routine receipt admission") }
	var task *db.AgentTaskQueue
	deadline := time.Now().Add(2 * time.Second)
	for task == nil && time.Now().Before(deadline) {
		var err error
		task, err = x.f.h.TaskService.ClaimTaskForRuntime(context.Background(), x.runtime.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{x.runtime.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if task == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if task == nil {
		t.Fatal("accepted occurrence unavailable for claim")
	}
	req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, x.daemonID)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1+","+protocol.DWSMessagePolicyCapability)
	_, _, _, _, failure := x.f.h.buildClaimedTaskResponse(req, task, x.runtime, "", uuidToString(x.runtime.ID), testWorkspaceID)
	if failure == nil {
		t.Fatal("mixed readers received a routine send producer")
	}
	if state := x.queueState(t, run.TaskID); state.Status != "deferred" {
		t.Fatal("mixed readers discarded the occurrence", state)
	}
	var runState, taskState string
	if err := testPool.QueryRow(context.Background(), `SELECT r.state,t.state FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE r.queue_task_id=$1`, run.TaskID).Scan(&runState, &taskState); err != nil {
		t.Fatal(err)
	}
	if runState != "running" || taskState != "running" {
		t.Fatal("mixed readers terminated accepted work", runState, taskState)
	}
	if n := x.count(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid`, x.a.ID); n != 0 {
		t.Fatal("mixed readers announced a false terminal result", n)
	}
}

// Existing claim fixtures must use the same numeric identity contract as DWS.
func prepareNumericRoutineSendIdentity(t *testing.T, f *ctxcapFixture, a *contextCapAgent) {
	t.Helper()
	// The shared fixture uses descriptive identities. Claim policy requires
	// real DWS numeric identifiers; update the full tenant directory before
	// admitting the occurrence, rather than forging task routing fields.
	for _, statement := range []string{
		`UPDATE agent_dingtalk_identity SET org_id='2002',dws_uid='424242' WHERE agent_id=$1::uuid`,
		`UPDATE agent_scene SET tenant_org_id='2002' WHERE agent_id=$1::uuid`,
		`UPDATE context_scope_routine SET tenant_org_id='2002' WHERE agent_id=$1::uuid`,
	} {
		if _, err := testPool.Exec(context.Background(), statement, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	a.OrgID, a.IdentityOrgID = "2002", "2002"
}
