package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestEmployeeRunClaimCapabilityAndInput(t *testing.T) {
	if testHandler == nil {
		t.Skip("DATABASE_URL required")
	}
	ctx := context.Background()
	var agentID string
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM agent WHERE workspace_id=$1::uuid LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	previousRuntime, err := testHandler.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode=$2,metadata=$3,daemon_id=$4,owner_id=$5 WHERE id=$1`, previousRuntime.ID, previousRuntime.RuntimeMode, previousRuntime.Metadata, previousRuntime.DaemonID, previousRuntime.OwnerID)
	})
	if _, err = testPool.Exec(ctx, `UPDATE agent SET owner_id=$1::uuid WHERE id=$2::uuid`, testUserID, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode='local',daemon_id='direct-daemon',metadata='{"client_capabilities":["employee-direct-v1"]}'::jsonb WHERE id=$1`, agent.RuntimeID); err != nil {
		t.Fatal(err)
	}
	rt, err := testHandler.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := scene.Resolve(ctx, testHandler.Queries, scene.Owner{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID}, scene.DingTalkConversation("direct-claim-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{Scope: employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: agentID, TenantOrgID: "direct-claim-org", Kind: employeetask.ScopeScene, Scene: scene.RefOf(sc)}, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: testUserID, Definition: employeetask.Definition{Goal: "Return CLAIM_OK"}, Source: employeetask.Source{Namespace: "claim_test", Key: uuid.NewString()}, Input: "Return CLAIM_OK"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_entry", "employee_task_run", "employee_task"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, testWorkspaceID)
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_scene WHERE id=$1`, sc.ID)
	})
	admitted, err := testHandler.TaskService.EnqueueDirectTask(ctx, service.DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: "claim", Key: "first"}, Prompt: "Return CLAIM_OK", PrincipalID: parseUUID(testUserID), OriginatorUserID: parseUUID(testUserID)})
	if err != nil {
		t.Fatal(err)
	}
	req := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, "direct-daemon")
	resp := AgentTaskResponse{}
	failure := testHandler.applyEmployeeRunClaim(req, admitted.Task, rt, &resp)
	if failure == nil || failure.status != http.StatusServiceUnavailable || resp.DirectTaskPrompt != "" {
		t.Fatal("old daemon accepted Direct", failure)
	}
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1)
	if failure = testHandler.applyEmployeeRunClaim(req, admitted.Task, rt, &resp); failure != nil {
		t.Fatal(failure)
	}
	if !strings.HasPrefix(resp.DirectTaskPrompt, "Return CLAIM_OK\n\n") || resp.WorkspaceID != testWorkspaceID || resp.AutopilotRunID != "" || resp.QuickCreatePrompt != "" {
		t.Fatalf("bad claim %+v", resp)
	}
	raw, _ := json.Marshal(resp)
	if !json.Valid(raw) {
		t.Fatal("invalid wire")
	}
	wrong := rt
	wrong.WorkspaceID = parseUUID(uuid.NewString())
	if testHandler.applyEmployeeRunClaim(req, admitted.Task, wrong, &resp) == nil {
		t.Fatal("foreign workspace accepted")
	}
	if testHandler.applyEmployeeRunClaim(req, db.AgentTaskQueue{}, rt, &resp) != nil {
		t.Fatal("ordinary task changed")
	}

	// Claim through the real queue. An old daemon defers Direct, then can claim
	// an ordinary issue task instead of spinning forever on the same queue head.
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, rt.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{rt.ID}})
	if err != nil || claimed == nil {
		t.Fatal(claimed, err)
	}
	old := newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, "direct-daemon")
	_, _, _, _, failure = testHandler.buildClaimedTaskResponse(old, claimed, rt, "", uuidToString(rt.ID), testWorkspaceID)
	if failure == nil {
		t.Fatal("old daemon received new Direct payload")
	}
	held, err := testHandler.Queries.GetAgentTask(ctx, admitted.Task.ID)
	if err != nil || held.Status != "deferred" || !held.FireAt.Valid {
		t.Fatal(held.Status, err)
	}
	issueID, ordinaryID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1::uuid`, ordinaryID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1::uuid`, issueID)
	})
	if _, err = testPool.Exec(ctx, `INSERT INTO issue(id,workspace_id,title,creator_type,creator_id) VALUES($1::uuid,$2::uuid,'Old task fixture','member',$3::uuid)`, issueID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,issue_id,status) VALUES($1::uuid,$2,$3,$4::uuid,'queued')`, ordinaryID, agent.ID, rt.ID, issueID); err != nil {
		t.Fatal(err)
	}
	normal, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, rt.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{rt.ID}})
	if err != nil || normal == nil || uuidToString(normal.ID) != ordinaryID {
		t.Fatal("ordinary task starved", normal, err)
	}
	normalResp, _, _, _, failure := testHandler.buildClaimedTaskResponse(old, normal, rt, "", uuidToString(rt.ID), testWorkspaceID)
	if failure != nil || normalResp.IssueID != issueID || normalResp.DirectTaskPrompt != "" {
		t.Fatal("old task behavior changed", failure)
	}
	if _, err = testHandler.TaskService.CancelTask(ctx, normal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET fire_at=now()-interval '1 second' WHERE id=$1`, admitted.Task.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err = testHandler.TaskService.ClaimTaskForRuntime(ctx, rt.ID, service.TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{rt.ID}})
	if err != nil || claimed == nil || claimed.ID != admitted.Task.ID {
		t.Fatal(claimed, err)
	}
	full, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, claimed, rt, "", uuidToString(rt.ID), testWorkspaceID)
	if failure != nil || !strings.HasPrefix(full.DirectTaskPrompt, "Return CLAIM_OK\n\n") {
		t.Fatal("candidate daemon claim", failure)
	}
	queueID := uuidToString(claimed.ID)
	startReq := withURLParam(newDaemonTokenRequest(http.MethodPost, "/", nil, testWorkspaceID, "direct-daemon"), "taskId", queueID)
	w := httptest.NewRecorder()
	testHandler.StartTask(w, startReq)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	usageBody := map[string]any{"usage": []map[string]any{{"provider": "claude", "model": "fake-model", "input_tokens": 13, "output_tokens": 7}}}
	usageReq := withURLParam(newDaemonTokenRequest(http.MethodPost, "/", usageBody, testWorkspaceID, "direct-daemon"), "taskId", queueID)
	w = httptest.NewRecorder()
	testHandler.ReportTaskUsage(w, usageReq)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	completeReq := withURLParam(newDaemonTokenRequest(http.MethodPost, "/", map[string]any{"output": "CLAIM_OK", "session_id": "direct-session"}, testWorkspaceID, "direct-daemon"), "taskId", queueID)
	w = httptest.NewRecorder()
	testHandler.CompleteTask(w, completeReq)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var state, result, usageWorkspace, accountable string
	var input, output int64
	if err = testPool.QueryRow(ctx, `SELECT r.state,r.result,rt.workspace_id::text,q.accountable_user_id::text,u.input_tokens,u.output_tokens FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id JOIN agent_runtime rt ON rt.id=q.runtime_id JOIN task_usage u ON u.task_id=q.id WHERE r.id=$1::uuid`, admitted.Run.ID).Scan(&state, &result, &usageWorkspace, &accountable, &input, &output); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || result != "CLAIM_OK" || usageWorkspace != testWorkspaceID || accountable != testUserID || input != 13 || output != 7 {
		t.Fatal(state, result, usageWorkspace, accountable, input, output)
	}
}

func TestEmployeeDirectCapabilityPersistedFromTransportOnly(t *testing.T) {
	got := localDingTalkClientCapabilities("untrusted-unknown, employee-direct-v1, dws_message_policy_v1, employee-direct-v1")
	expected := []string{protocol.DWSMessagePolicyCapability, protocol.DaemonCapabilityEmployeeDirectV1}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("capabilities = %v", got)
	}
	if got = localDingTalkClientCapabilities(""); len(got) != 0 {
		t.Fatal("old daemon retained unsupported capabilities", got)
	}
}
