package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/deploymentfence"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type progressFixture struct {
	employeeNoticeFixture
	b     employeeProgressBinding
	model *employeeWakeTestModel
}

func progressDatabase(t *testing.T) progressFixture {
	t.Helper()
	f := employeeNoticeDatabase(t, "running", false, false)
	b, err := loadEmployeeProgressBinding(context.Background(), testPool, f.queueID)
	if err != nil {
		t.Fatal(err)
	}
	model := &employeeWakeTestModel{respond: func(int) (string, map[string]any) {
		return wakeToolCall("progress-reply", "reply", map[string]any{"reply": "后台报告已经核对12笔，正在继续检查缺项；这件事尚未完成。"})
	}}
	f.h.EmployeeSceneWorker.model = model
	svc := dingtalkresponse.NewService(testPool, &employeeNoticeProvider{}, nil)
	svc.BeforeSend = f.h.BeforeEmployeeRunNoticeSend
	f.h.DingTalkResponses = svc
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_progress_report WHERE agent_id=$1::uuid`, f.agentID)
	})
	return progressFixture{f, b, model}
}
func (f progressFixture) report(id, summary string) (employeeProgressReceipt, error) {
	return f.h.recordEmployeeProgress(context.Background(), f.queueID, testWorkspaceID, f.agentID, testUserID, id, summary)
}
func (f progressFixture) mcp(t *testing.T, args any) *httptest.ResponseRecorder {
	t.Helper()
	r := mcpRequest(t, "tools/call", "progress-rpc", map[string]any{"name": employeeProgressMCPTool, "arguments": args})
	r.Header.Set("X-Task-ID", f.queueID)
	r.Header.Set("X-Workspace-ID", testWorkspaceID)
	r.Header.Set("X-Agent-ID", f.agentID)
	r.Header.Set("X-User-ID", testUserID)
	w := httptest.NewRecorder()
	f.h.EmployeeProgressMCP(w, r)
	return w
}
func drainProgressActions(t *testing.T, f progressFixture) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go f.h.DingTalkResponses.Run(ctx)
	defer func() {
		cancel()
		if !f.h.DingTalkResponses.WaitWithTimeout(context.Background(), 5*time.Second) {
			t.Error("response worker did not stop")
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE agent_id=$1::uuid AND state IN ('pending','provider_accepted','unknown')`, f.agentID); n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("outbox did not drain")
}

func TestEmployeeProgressMCPWakeDeliveryAndNoWorkMutation(t *testing.T) {
	f := progressDatabase(t)
	ctx := context.Background()
	var version, seq, rounds int64
	if err := testPool.QueryRow(ctx, `SELECT version,last_entry_seq,autonomous_rounds FROM employee_task WHERE id=$1::uuid`, f.b.TaskID).Scan(&version, &seq, &rounds); err != nil {
		t.Fatal(err)
	}
	w := f.mcp(t, map[string]any{"report_id": "stage-1", "summary": "已核对12笔，正在继续检查缺项。"})
	if w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if f.model.calls != 1 {
		t.Fatal("progress model calls", f.model.calls)
	}
	var input map[string]any
	if err := json.Unmarshal(f.model.requests[0], &input); err != nil {
		t.Fatal(err)
	}
	for _, raw := range input["tools"].([]any) {
		name := raw.(map[string]any)["function"].(map[string]any)["name"]
		if name != "reply" && name != "stay_quiet" {
			t.Fatal("work tool exposed", name)
		}
	}
	if !strings.Contains(string(f.model.requests[0]), "已核对12笔") || !strings.Contains(string(f.model.requests[0]), "PROGRESS DISPLAY") {
		t.Fatal("candidate/policy absent")
	}
	var gotVersion, gotSeq, gotRounds int64
	var state string
	if err := testPool.QueryRow(ctx, `SELECT version,last_entry_seq,autonomous_rounds,state FROM employee_task WHERE id=$1::uuid`, f.b.TaskID).Scan(&gotVersion, &gotSeq, &gotRounds, &state); err != nil {
		t.Fatal(err)
	}
	if version != gotVersion || seq != gotSeq || rounds != gotRounds || state != "running" {
		t.Fatal("disclosure advanced work", gotVersion, gotSeq, gotRounds, state)
	}
	if n := wakeCount(t, `SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, f.b.TaskID); n != 1 {
		t.Fatal("new execution", n)
	}
	drainProgressActions(t, f)
	var action, stateReport, decision string
	if err := testPool.QueryRow(ctx, `SELECT action_id,state,decision FROM employee_progress_report WHERE run_id=$1::uuid`, f.runID).Scan(&action, &stateReport, &decision); err != nil {
		t.Fatal(err)
	}
	if action == "" || stateReport != "decided" || decision != "display" {
		var detail string
		_ = testPool.QueryRow(ctx, `SELECT state||' / '||last_error||' / '||COALESCE(outcome::text,'') FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, f.agentID).Scan(&detail)
		t.Fatal(action, stateReport, decision, detail)
	}
	if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE id=$1 AND state='delivered'`, action); n != 1 {
		t.Fatal("not delivered", n)
	}
	var jobID string
	if err := testPool.QueryRow(ctx, `SELECT job_id::text FROM employee_progress_report WHERE run_id=$1::uuid`, f.runID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	view, err := employeeProgressCurrent(ctx, testPool, employeeentry.Job{ID: jobID, Scope: f.b.Scope})
	if err != nil || len(view.RecentDelivered) != 1 || view.RecentDelivered[0] != "后台报告已经核对12笔，正在继续检查缺项；这件事尚未完成。" {
		t.Fatal("shown history must contain delivered reply, not candidate", view, err)
	}
}

func TestEmployeeProgressQuietReplayConflictAndDuplicateSummary(t *testing.T) {
	f := progressDatabase(t)
	ctx := context.Background()
	f.model.respond = func(int) (string, map[string]any) {
		return wakeToolCall("progress-quiet", "stay_quiet", map[string]any{})
	}
	r, err := f.report("quiet-1", "还在核对，暂无新的阶段成果。")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	r2, err := f.report("quiet-1", "还在核对，暂无新的阶段成果。")
	if err != nil || !r2.Replayed || r2.ReportRef != r.ReportRef {
		t.Fatal(r2, err)
	}
	if _, err = f.report("quiet-1", "冲突的新正文"); err == nil {
		t.Fatal("conflicting report accepted")
	}
	r3, err := f.report("quiet-2", "还在核对，暂无新的阶段成果。")
	if err != nil || r3.State != "decided" {
		t.Fatal(r3, err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || worked {
		t.Fatal("quiet replay added job", worked, err)
	}
	if f.model.calls != 1 {
		t.Fatal("replayed generation", f.model.calls)
	}
	if n := wakeCount(t, `SELECT count(*) FROM employee_progress_report WHERE run_id=$1::uuid AND action_id<>''`, f.runID); n != 0 {
		t.Fatal("quiet sent", n)
	}
}

func TestEmployeeProgressConcurrentAdmissionIsOneWake(t *testing.T) {
	f := progressDatabase(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	refs := make(chan string, 4)
	for range 4 {
		wg.Go(func() {
			r, err := f.report("concurrent", "材料已整理，正在检查。")
			errs <- err
			refs <- r.ReportRef
		})
	}
	wg.Wait()
	close(errs)
	close(refs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ref := ""
	for r := range refs {
		if ref != "" && r != ref {
			t.Fatal("multiple reports", ref, r)
		}
		ref = r
	}
	if n := wakeCount(t, `SELECT count(*) FROM employee_progress_report WHERE run_id=$1::uuid`, f.runID); n != 1 {
		t.Fatal(n)
	}
	if n := wakeCount(t, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='task_wake'`, f.agentID); n != 1 {
		t.Fatal(n)
	}
}

func TestEmployeeProgressCurrentRunFenceAndIndependentFinal(t *testing.T) {
	for _, boundary := range []string{"revision", "cancel", "complete"} {
		t.Run(boundary, func(t *testing.T) {
			f := progressDatabase(t)
			ctx := context.Background()
			if _, err := f.report("pending", "这部分已整理，下一步继续检查。"); err != nil {
				t.Fatal(err)
			}
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
			var action string
			if err := testPool.QueryRow(ctx, `SELECT action_id FROM employee_progress_report WHERE run_id=$1::uuid`, f.runID).Scan(&action); err != nil {
				t.Fatal(err)
			}
			switch boundary {
			case "revision":
				if _, err := testPool.Exec(ctx, `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=$1::uuid`, f.b.TaskID); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := f.h.TaskService.CancelTask(ctx, parseUUID(f.queueID)); err != nil {
					t.Fatal(err)
				}
			case "complete":
				if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(f.queueID), []byte(`{"output":"最终结果仍应独立交付"}`), "", "", false, ""); err != nil {
					t.Fatal(err)
				}
				if _, err := reconcileEmployeeNotice(t, f.h); err != nil {
					t.Fatal(err)
				}
			}
			drainProgressActions(t, f)
			if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE id=$1 AND state='cancelled'`, action); n != 1 {
				t.Fatal("old pending progress not suppressed", n)
			}
			if boundary == "complete" {
				if n := wakeCount(t, `SELECT count(*) FROM employee_run_notice n JOIN response_action a ON a.id=n.action_id WHERE n.run_id=$1::uuid AND a.state='delivered'`, f.runID); n != 1 {
					t.Fatal("progress suppressed final", n)
				}
			}
		})
	}
}

func TestEmployeeProgressAuthorityQuotaAndReaderGate(t *testing.T) {
	f := progressDatabase(t)
	ctx := context.Background()
	if _, err := f.h.recordEmployeeProgress(ctx, f.queueID, testWorkspaceID, f.agentID, uuid.NewString(), "foreign", "x"); err == nil {
		t.Fatal("foreign principal accepted")
	}
	if _, err := f.h.recordEmployeeProgress(ctx, f.queueID, uuid.NewString(), f.agentID, testUserID, "foreign-scope", "x"); err == nil {
		t.Fatal("foreign scope accepted")
	}
	w := f.mcp(t, map[string]any{"report_id": "body-target", "summary": "x", "scene_id": uuid.NewString()})
	if !strings.Contains(w.Body.String(), "invalid progress arguments") {
		t.Fatal(w.Body.String())
	}
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return errors.New("old reader") }
	if _, err := f.report("not-ready", "x"); err == nil {
		t.Fatal("old reader admitted")
	}
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	for i := 0; i < employeeProgressLimit; i++ {
		if _, err := f.report(fmt.Sprint(i), fmt.Sprintf("阶段报告%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.report("over-quota", "新的阶段报告"); err == nil {
		t.Fatal("over-quota accepted")
	}
	if _, err := f.report("0", "阶段报告0"); err != nil {
		t.Fatal("quota blocked idempotent retry", err)
	}
}

func TestEmployeeProgressToolMountDoesNotExposeGenericWorkflow(t *testing.T) {
	f := progressDatabase(t)
	ctx := context.Background()
	queue, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.queueID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := f.h.Queries.GetAgentRuntime(ctx, queue.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	f.h.cfg.PublicURL = "https://api.test.invalid"
	data := &TaskAgentData{McpConfig: json.RawMessage(`{}`)}
	if err = f.h.injectRunnerMCP(ctx, runtime, queue, "test-task-token", data, true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data.McpConfig), employeeProgressMCPServerName) || strings.Contains(string(data.McpConfig), `"multica"`) {
		t.Fatal("unexpected Direct MCP surface", string(data.McpConfig))
	}
	if route := data.McpRelayRoutes[employeeProgressMCPServerName]; route.Path != employeeProgressMCPPath {
		t.Fatal(route)
	}
	r := mcpRequest(t, "tools/list", "list", map[string]any{})
	w := httptest.NewRecorder()
	f.h.EmployeeProgressMCP(w, r)
	response := decodeMCPResponse(t, w)
	tools := response["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != employeeProgressMCPTool {
		t.Fatal(tools)
	}
	personal := personalMCPRequest(t, "tools/list", "personal", map[string]any{})
	w = httptest.NewRecorder()
	f.h.EmployeeProgressMCP(w, personal)
	if w.Code != http.StatusForbidden {
		t.Fatal("personal endpoint access", w.Code)
	}
	if !service.IsEmployeeDirectTask(queue) {
		t.Fatal("fixture is not Direct")
	}
}

func TestEmployeeProgressRuntimeOwnerTokenIsNotBusinessPrincipal(t *testing.T) {
	f := progressDatabase(t)
	ctx := context.Background()
	owner, err := f.h.Queries.CreateUser(ctx, db.CreateUserParams{Name: "Separate Runtime Owner", Email: uuid.NewString() + "@test.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE agent_runtime SET owner_id=$2::uuid WHERE id=(SELECT runtime_id FROM agent_task_queue WHERE id=$1::uuid)`, f.queueID, testUserID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE user_id=$1`, owner.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1`, owner.ID)
	})
	if _, err = testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$2 WHERE id=(SELECT runtime_id FROM agent_task_queue WHERE id=$1::uuid)`, f.queueID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now(),started_at=NULL WHERE id=$1::uuid`, f.queueID); err != nil {
		t.Fatal(err)
	}
	queue, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.queueID))
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.TaskService.FinalizeTaskClaim(ctx, queue, db.CreateTaskTokenParams{TokenHash: auth.HashToken(token), TaskID: queue.ID, AgentID: queue.AgentID, WorkspaceID: parseUUID(testWorkspaceID), UserID: owner.ID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, f.queueID); err != nil {
		t.Fatal(err)
	}
	r := mcpRequest(t, "tools/call", "owner", map[string]any{"name": employeeProgressMCPTool, "arguments": map[string]any{"report_id": "different-owner", "summary": "核对已经开始。"}})
	r.Header.Set("Authorization", "Bearer "+token)
	// Auth must replace these spoofed scope headers with the claim's token row.
	r.Header.Set("X-Task-ID", uuid.NewString())
	r.Header.Set("X-Workspace-ID", uuid.NewString())
	r.Header.Set("X-User-ID", uuid.NewString())
	w := httptest.NewRecorder()
	middleware.Auth(f.h.Queries, nil, nil)(http.HandlerFunc(f.h.EmployeeProgressMCP)).ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"isError":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var principal string
	if err = testPool.QueryRow(ctx, `SELECT j.principal_id::text FROM employee_progress_report p JOIN employee_scene_job j ON j.id=p.job_id WHERE p.run_id=$1::uuid`, f.runID).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	if principal != testUserID || principal == uuidToString(owner.ID) {
		t.Fatal("execution owner replaced business principal", principal)
	}
}

func TestEmployeeProgressPendingRechecksOriginAndMixedReaders(t *testing.T) {
	for _, boundary := range []string{"origin", "reader", "identity"} {
		t.Run(boundary, func(t *testing.T) {
			f := progressDatabase(t)
			ctx := context.Background()
			if _, err := f.report("before-revoke", "当前阶段需要继续核对。"); err != nil {
				t.Fatal(err)
			}
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
			var action string
			var raw []byte
			if err := testPool.QueryRow(ctx, `SELECT p.action_id,a.input FROM employee_progress_report p JOIN response_action a ON a.id=p.action_id WHERE p.run_id=$1::uuid`, f.runID).Scan(&action, &raw); err != nil {
				t.Fatal(err)
			}
			var in dingtalkresponse.ActionInput
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatal(err)
			}
			in.ActionID = action
			if boundary == "identity" {
				if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET dws_uid='replacement-identity' WHERE agent_id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
				err := f.h.BeforeEmployeeRunNoticeSend(ctx, in)
				var suppressed *dingtalkresponse.SuppressSendError
				if !errors.As(err, &suppressed) || suppressed.Reason != "identity_changed" {
					t.Fatal("changed identity did not persistently suppress", err)
				}
			} else if boundary == "origin" {
				if _, err := testPool.Exec(ctx, `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
				err := f.h.BeforeEmployeeRunNoticeSend(ctx, in)
				var suppressed *dingtalkresponse.SuppressSendError
				if !errors.As(err, &suppressed) {
					t.Fatal("revoked origin could send", err)
				}
			} else {
				current, old := uuid.NewString(), uuid.NewString()
				fence, err := deploymentfence.New(ctx, testPool, current, "progress "+EmployeeLoopReplicaMarker)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = testPool.Exec(context.Background(), `DELETE FROM deployment_fence_replica_ack WHERE instance_id=ANY($1::text[])`, []string{current, old})
				})
				if _, err = testPool.Exec(ctx, `INSERT INTO deployment_fence_replica_ack(instance_id,build_id,state,revision,last_seen_at) VALUES($1,'old [employee-loop:18]','normal',1,now())`, old); err != nil {
					t.Fatal(err)
				}
				f.h.EmployeeSceneWorker.ReplicaReady = func(ctx context.Context) error {
					ready, err := fence.AllLiveReplicasSupport(ctx, EmployeeLoopReplicaMarker)
					if err != nil {
						return err
					}
					if !ready {
						return errors.New("mixed readers")
					}
					return nil
				}
				if err = f.h.BeforeEmployeeRunNoticeSend(ctx, in); err == nil {
					t.Fatal("mixed 18/19 reader could send")
				}
				if _, err = testPool.Exec(ctx, `UPDATE deployment_fence_replica_ack SET build_id=$2 WHERE instance_id=$1`, old, "upgraded "+EmployeeLoopReplicaMarker); err != nil {
					t.Fatal(err)
				}
				if err = f.h.BeforeEmployeeRunNoticeSend(ctx, in); err != nil {
					t.Fatal("all readers did not recover", err)
				}
			}
			if n := wakeCount(t, `SELECT count(*) FROM response_action WHERE id=$1 AND state='pending'`, action); n != 1 {
				t.Fatal("pre-submit gate touched delivery", n)
			}
		})
	}
}
