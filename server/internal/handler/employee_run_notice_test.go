package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeNoticeFixture struct {
	*dingTalkResponseFixture
	model                 *employeeTestModel
	runID, queueID, jobID string
}

func employeeNoticeDatabase(t *testing.T, state string, router bool, multi bool, configure ...func(*dingTalkResponseFixture, *employeeTestModel)) employeeNoticeFixture {
	t.Helper()
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	if !router {
		f.command.CompletionCallback = nil
		f.command.ResponsePolicy = nil
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,'123','456',$3::uuid)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	ep, err := f.h.Queries.EnsureAgentDispatchEndpoint(ctx, db.EnsureAgentDispatchEndpointParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID), ActorUserID: parseUUID(testUserID), EndpointID: "notice-" + uuid.NewString(), DispatchUrl: "https://test.invalid/dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	dc.EndpointID, dc.EndpointNamespaceID = ep.EndpointID, ep.ID
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_run_notice WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	})
	if multi {
		f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "bob-message", Text: "Bob request", SenderUID: "bob", SenderOpenDingTalkID: "bob-open"}, {OpenMsgID: "alice-message", Text: "Alice request", SenderUID: "alice", SenderOpenDingTalkID: "alice-open"}}
	}
	for _, option := range configure {
		option(f, model)
	}
	if w := employeeHTTP(t, f, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var jobID string
	var items []employeeentry.Item
	if err = testPool.QueryRow(ctx, `SELECT id::text,items FROM employee_scene_job WHERE agent_id=$1::uuid`, f.agentID).Scan(&jobID, &items); err != nil {
		t.Fatal(err)
	}
	messageID := "message-1"
	if multi {
		messageID = "bob-message"
	}
	model.dispatch, model.sourceRef = true, items[0].ReceiptID+"/"+messageID
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var runID, queueID string
	if err = testPool.QueryRow(ctx, `SELECT r.id::text,r.queue_task_id::text FROM employee_task_run r WHERE r.agent_id=$1::uuid`, f.agentID).Scan(&runID, &queueID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, queueID); err != nil {
		t.Fatal(err)
	}
	switch state {
	case "succeeded":
		_, err = f.h.TaskService.CompleteTask(ctx, parseUUID(queueID), []byte(`{"output":"真实已存结果"}`), "", "", false, "")
	case "failed":
		_, err = f.h.TaskService.FailTask(ctx, parseUUID(queueID), "真实失败原因", "", "", "agent_error", false, "")
	case "cancelled":
		_, err = f.h.TaskService.CancelTask(ctx, parseUUID(queueID))
	}
	if err != nil {
		t.Fatal(err)
	}
	return employeeNoticeFixture{f, model, runID, queueID, jobID}
}

func reconcileEmployeeNotice(t *testing.T, h *Handler) (int, error) {
	t.Helper()
	return h.ReconcileEmployeeRunNotices(context.Background(), 100)
}

func TestEmployeeRunNoticeTerminalResultIsDurableAndUnique(t *testing.T) {
	for _, state := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := employeeNoticeDatabase(t, state, false, false)
			if _, err := reconcileEmployeeNotice(t, f.h); err != nil {
				t.Fatal(err)
			}
			var actionID, noticeState, body, requester string
			if err := testPool.QueryRow(context.Background(), `SELECT action_id,state,body,requester_ref FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&actionID, &noticeState, &body, &requester); err != nil {
				t.Fatal(err)
			}
			if noticeState != "enqueued" || actionID == "" || requester == "" {
				t.Fatal(noticeState, actionID, requester)
			}
			if state == "succeeded" && !strings.Contains(body, "真实已存结果") || state == "failed" && !strings.Contains(body, "真实失败原因") || state == "cancelled" && (!strings.Contains(body, "取消请求") || strings.Contains(body, "已停止")) {
				t.Fatal("terminal result misrepresented", body)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 6)
			for range 6 {
				wg.Go(func() { restarted := *f.h; _, err := reconcileEmployeeNotice(t, &restarted); errs <- err })
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM response_action WHERE id=$1`, actionID).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			if f.model.calls != 1 {
				t.Fatal("delivery reran the foreground model", f.model.calls)
			}
		})
	}
}

func TestEmployeeRunNoticeRouterPreservesRouteAndSelectedRequester(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", true, true)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileEmployeeNotice(t, f.h); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if input["callback_url"] != f.command.CompletionCallback.ResponseURL || input["callback_target"] != testRouterTargetIdentity || input["reply_to_open_msg_id"] != "bob-message" || input["sender_open_dingtalk_id"] != "bob-open" {
		t.Fatalf("frozen Router audience lost: %+v", input)
	}
	if input["request_id"] == "" || input["task_id"] != f.queueID {
		t.Fatal(input)
	}
}

func TestEmployeeRunNoticeOnlyAcceptsArtifactsFromTheSameRun(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "foreign_run"}[mismatch], func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			f.h.EmployeeRunNoticeArtifacts = func(_ context.Context, scope employeetask.Scope, taskID, runID string) ([]EmployeeTaskArtifactRef, error) {
				if scope.AgentID != f.agentID || runID != f.runID {
					t.Error("artifact lookup escaped original run")
				}
				if mismatch {
					runID = uuid.NewString()
				}
				return []EmployeeTaskArtifactRef{{AttachmentResponse: AttachmentResponse{Filename: "真实产物.pdf", URL: "https://private.invalid/download"}, ArtifactRef: "attachment:" + uuid.NewString(), TaskID: taskID, RunID: runID, QueueTaskID: f.queueID}}, nil
			}
			_, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100)
			if mismatch {
				if err == nil {
					t.Fatal("foreign artifact was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var body string
			var count int
			if err = testPool.QueryRow(context.Background(), `SELECT body,jsonb_array_length(artifacts) FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&body, &count); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, "真实产物.pdf") || strings.Contains(body, "private.invalid") || strings.Contains(body, "已发送文件") || count != 1 {
				t.Fatal(body, count)
			}
		})
	}
}

func TestEmployeeRunNoticeArtifactLookupDoesNotHoldItsDatabaseConnection(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	config := testPool.Config().Copy()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h := *f.h
	h.DB, h.TxStarter, h.Queries = pool, pool, db.New(pool)
	h.EmployeeRunNoticeArtifacts = func(ctx context.Context, _ employeetask.Scope, _, _ string) ([]EmployeeTaskArtifactRef, error) {
		var alive bool
		err := pool.QueryRow(ctx, `SELECT true`).Scan(&alive)
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if n, err := h.ReconcileEmployeeRunNotices(ctx, 100); err != nil || n != 1 {
		t.Fatalf("artifact callback waited on notice's own connection: n=%d err=%v", n, err)
	}
}

func TestEmployeeRunNoticeRechecksAuthorityAfterArtifactLookup(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	f.h.EmployeeRunNoticeArtifacts = func(context.Context, employeetask.Scope, string, string) ([]EmployeeTaskArtifactRef, error) {
		revokeEmployeeNoticePrincipal(t)
		return nil, nil
	}
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var state string
	var hasAction bool
	if err := testPool.QueryRow(context.Background(), `SELECT state,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &hasAction); err != nil {
		t.Fatal(err)
	}
	if state != "suppressed" || hasAction {
		t.Fatal("artifact lookup bypassed a changed grant", state, hasAction)
	}
}
