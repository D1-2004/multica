package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The human API interrupts the running Direct execution; the successor is
// claimable only after the daemon proves exit, and it resumes the replaced
// execution's provider session and workdir with the correction as input.
func TestSteerEmployeeTaskHumanAPIResumesPredecessorSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("DATABASE_URL required")
	}
	ctx := context.Background()
	// A dedicated agent and runtime keep other tests' leftover executions from
	// consuming this agent's concurrency.
	runtimeID := createClaimReclaimRuntime(t, ctx, "steer-runtime-"+uuid.NewString())
	agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "steer-agent-"+uuid.NewString())
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode='local',status='online',daemon_id='steer-daemon',metadata='{"client_capabilities":["employee-direct-v1"]}'::jsonb WHERE id=$1::uuid`, runtimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET runtime_mode='local' WHERE id=$1::uuid`, agentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id=$1::uuid`, agentID) })
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := testHandler.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := scene.Resolve(ctx, testHandler.Queries, scene.Owner{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID}, scene.DingTalkConversation("steer-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{Scope: employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: agentID, TenantOrgID: "steer-org", Kind: employeetask.ScopeScene, Scene: scene.RefOf(sc)}, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: testUserID, Definition: employeetask.Definition{Goal: "Write the quarterly summary"}, Source: employeetask.Source{Namespace: "steer_test", Key: uuid.NewString()}, Input: "Write the quarterly summary"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE context->>'employee_task_id'=$1`, task.ID)
		for _, table := range []string{"employee_task_entry", "employee_task_run", "employee_task"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE task_id=$1::uuid OR (id=$1::uuid AND $2='employee_task')`, task.ID, table)
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_scene WHERE id=$1`, sc.ID)
	})
	admitted, err := testHandler.TaskService.EnqueueDirectTask(ctx, service.DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: "steer", Key: "first"}, Prompt: "Write the quarterly summary", PrincipalID: parseUUID(testUserID), OriginatorUserID: parseUUID(testUserID)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, rt.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{rt.ID}})
	if err != nil || claimed == nil || claimed.ID != admitted.Task.ID {
		t.Fatal(claimed, err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),session_id='provider-session-1',work_dir='/workspaces/steer/workdir' WHERE id=$1`, admitted.Task.ID); err != nil {
		t.Fatal(err)
	}

	post := func(id, key, content string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/employee-tasks/"+id+"/steer", strings.NewReader(`{"content":`+jsonString(content)+`}`))
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		req = withURLParams(req, "id", id)
		w := httptest.NewRecorder()
		testHandler.SteerEmployeeTask(w, req)
		return w
	}
	queueRef := uuidToString(admitted.Task.ID)
	if w := post(queueRef, "", "missing key"); w.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d %s", w.Code, w.Body.String())
	}
	if w := post(uuid.NewString(), "k", "unknown task"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown task: %d %s", w.Code, w.Body.String())
	}
	first := post(queueRef, "fix-1", "Only cover Q3, and cite the signed contracts")
	if first.Code != http.StatusAccepted {
		t.Fatalf("steer: %d %s", first.Code, first.Body.String())
	}
	var accepted EmployeeTaskSteerResponse
	if err = json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.TaskID != task.ID || accepted.Outcome != string(employeetask.SteerInterrupted) || accepted.InterruptedQueueTaskID != queueRef || !accepted.SuccessorAwaitsExitProof || !accepted.Capabilities.Steer || accepted.Capabilities.LiveSteer {
		t.Fatalf("response: %+v", accepted)
	}
	replay := post(task.ID, "fix-1", "Only cover Q3, and cite the signed contracts")
	var again EmployeeTaskSteerResponse
	_ = json.Unmarshal(replay.Body.Bytes(), &again)
	if replay.Code != http.StatusAccepted || !again.Replayed || again.QueueTaskID != accepted.QueueTaskID {
		t.Fatalf("replay by EmployeeTask ID: %d %s", replay.Code, replay.Body.String())
	}
	if next, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, rt.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{rt.ID}}); err != nil || next != nil {
		t.Fatalf("successor claimed before exit proof: %+v %v", next, err)
	}
	ack := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+queueRef+"/cancel-ack", map[string]any{"process_group_stopped": true}, testWorkspaceID, "steer-daemon")
	ack = withURLParams(ack, "taskId", queueRef)
	ackW := httptest.NewRecorder()
	testHandler.AckTaskCancelled(ackW, ack)
	if ackW.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", ackW.Code, ackW.Body.String())
	}
	next, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, rt.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{rt.ID}})
	if err != nil || next == nil || uuidToString(next.ID) != accepted.QueueTaskID {
		t.Fatalf("successor after exit proof: %+v %v", next, err)
	}
	claimReq := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, "steer-daemon")
	claimReq.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1)
	resp, _, _, _, failure := testHandler.buildClaimedTaskResponse(claimReq, next, rt, "", uuidToString(rt.ID), testWorkspaceID)
	if failure != nil {
		t.Fatal(failure)
	}
	if resp.PriorSessionID != "provider-session-1" || resp.PriorWorkDir != "/workspaces/steer/workdir" {
		t.Fatalf("successor does not resume the replaced session: session=%q workdir=%q", resp.PriorSessionID, resp.PriorWorkDir)
	}
	if !strings.Contains(resp.DirectTaskPrompt, "Only cover Q3, and cite the signed contracts") || !strings.Contains(resp.DirectTaskPrompt, "Write the quarterly summary") {
		t.Fatalf("successor prompt:\n%s", resp.DirectTaskPrompt)
	}
}

// A Run cancelled by steer has a successor that reports the outcome, so the
// requester never receives a cancellation notice for it.
func TestEmployeeRunNoticeSuppressedForSteeredRun(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	task := employeeNoticeTask(t, f)
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET status='online' WHERE id=(SELECT runtime_id FROM agent_task_queue WHERE id=$1::uuid)`, f.queueID); err != nil {
		t.Fatal(err)
	}
	got, err := service.EmployeeTaskControl{Tasks: f.h.TaskService}.Steer(ctx, service.EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "notice_steer", Key: uuid.NewString()}, ActorRef: task.RequesterRef, Content: "改为只统计签约客户", SameRequester: true})
	if err != nil || got.Outcome != employeetask.SteerInterrupted {
		t.Fatalf("steer: %+v %v", got, err)
	}
	if _, err = reconcileEmployeeNotice(t, f.h); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	var hasAction bool
	if err = testPool.QueryRow(ctx, `SELECT state,reason,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason, &hasAction); err != nil {
		t.Fatal(err)
	}
	if state != "suppressed" || reason != "steered" || hasAction {
		t.Fatalf("steered run notified the requester: state=%s reason=%s action=%v", state, reason, hasAction)
	}
}
