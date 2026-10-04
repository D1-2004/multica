package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func employeeNativeNoticeDatabase(t *testing.T, routerTarget string) employeeNoticeFixture {
	t.Helper()
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.h.TaskCompletionTargetIdentity = routerTarget
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,'123','456',$3::uuid)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	ep, err := f.h.Queries.EnsureAgentDispatchEndpoint(ctx, db.EnsureAgentDispatchEndpointParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID), ActorUserID: parseUUID(testUserID), EndpointID: "native-notice-" + uuid.NewString(), DispatchUrl: "https://test.invalid/dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	dc.EndpointID, dc.EndpointNamespaceID = ep.EndpointID, ep.ID
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_run_notice WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	})
	nativeID := agentmessagerouter.NativeDispatchTaskID(f.agentID, "456", f.command.Event.Data.Conversation.OpenConversationID, "message-1")
	base := "/api/v1/dispatch-tasks/" + nativeID
	f.command.CompletionCallback = &DispatchCompletionCallback{URL: base + "/execution-result", UpdateURL: base + "/execution-update", ResponseURL: base + "/response-receipt"}
	raw, err := json.Marshal(f.command)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(string(raw))).WithContext(withNativeDispatch(ctx))
	req.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	f.h.handleAgentDispatchV2(response, req, raw, dc)
	if response.Code != http.StatusAccepted {
		t.Fatalf("native admission: HTTP %d %s", response.Code, response.Body.String())
	}
	var jobID string
	var items []employeeentry.Item
	if err = testPool.QueryRow(ctx, `SELECT id::text,items FROM employee_scene_job WHERE agent_id=$1::uuid`, f.agentID).Scan(&jobID, &items); err != nil {
		t.Fatal(err)
	}
	model.dispatch, model.sourceRef = true, items[0].ReceiptID+"/message-1"
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var runID, queueID string
	if err = testPool.QueryRow(ctx, `SELECT id::text,queue_task_id::text FROM employee_task_run WHERE agent_id=$1::uuid`, f.agentID).Scan(&runID, &queueID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, queueID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.TaskService.CompleteTask(ctx, parseUUID(queueID), []byte(`{"output":"native final result"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	return employeeNoticeFixture{f, model, runID, queueID, jobID}
}

func TestEmployeeRunNoticeNativeCallbackKeepsNativeTargetAcrossRouterChanges(t *testing.T) {
	for _, tc := range []struct{ name, target string }{{"router_configured", testRouterTargetIdentity}, {"native_only", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			f := employeeNativeNoticeDatabase(t, tc.target)
			ctx := context.Background()
			if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
				t.Fatal(err)
			}
			var state, reason string
			if err := testPool.QueryRow(ctx, `SELECT state,reason FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason); err != nil {
				t.Fatal(err)
			}
			if state != "enqueued" {
				t.Fatalf("native callback was suppressed: %s %s", state, reason)
			}
			var raw []byte
			if err := testPool.QueryRow(ctx, `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var in dingtalkresponse.ActionInput
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			if in.CallbackTarget != agentmessagerouter.NativeTargetIdentity() || in.CallbackURL != f.command.CompletionCallback.ResponseURL || in.DWSEnvironment != nativeDWSEnvironment {
				t.Fatalf("native response route changed: target=%s url=%s env=%s", in.CallbackTarget, in.CallbackURL, in.DWSEnvironment)
			}
			// External Router rotation after enqueue cannot revoke a native
			// callback's independent, server-owned completion target.
			f.h.TaskCompletionTargetIdentity = "router-target:v1:sha256:" + strings.Repeat("d", 64)
			if err := f.h.BeforeEmployeeRunNoticeSend(ctx, in); err != nil {
				t.Fatal("external Router change revoked native delivery", err)
			}
			provider := &employeeNoticeProvider{}
			actionID := startEmployeeNoticeDelivery(t, f, provider)
			waitEmployeeNoticeAction(t, actionID, "delivered")
			if provider.sends.Load() != 1 || f.model.calls != 1 {
				t.Fatal(provider.sends.Load(), f.model.calls)
			}
		})
	}
}
