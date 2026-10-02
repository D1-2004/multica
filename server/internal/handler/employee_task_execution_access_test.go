package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type employeeExecutionFixture struct {
	directAccessFixture
	runtime db.AgentRuntime
}

func employeeExecutionDatabase(t *testing.T) employeeExecutionFixture {
	t.Helper()
	f := employeeDirectAccessFixture(t)
	ctx := context.Background()
	before, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(testRuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode='local',provider='codex',daemon_id=$2,owner_id=$3::uuid WHERE id=$1::uuid`, testRuntimeID, "direct-executor-"+uuid.NewString(), testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode=$2,provider=$3,daemon_id=$4,owner_id=$5,metadata=$6 WHERE id=$1`, before.ID, before.RuntimeMode, before.Provider, before.DaemonID, before.OwnerID, before.Metadata)
	})
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Only one Direct row and one ordinary task remain eligible on this agent.
	if _, err = testPool.Exec(ctx, `DELETE FROM task_message WHERE task_id=$1::uuid;`, f.owned); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1::uuid`, f.owned); err != nil {
		t.Fatal(err)
	}
	sc, err := scene.Resolve(ctx, testHandler.Queries, scene.Owner{WorkspaceID: before.WorkspaceID, AgentID: parseUUID(f.agent)}, scene.DingTalkConversation("direct-executor-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	store := employeetask.NewStore(testPool)
	task, err := store.Create(ctx, employeetask.CreateParams{Scope: employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agent, TenantOrgID: "direct-executor-org", Kind: employeetask.ScopeScene, Scene: scene.RefOf(sc)}, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "external", Definition: employeetask.Definition{Goal: "PRIVATE_EXECUTION_PROMPT"}, Source: employeetask.Source{Namespace: "executor-test", Key: uuid.NewString()}, Input: "PRIVATE_EXECUTION_PROMPT"})
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, _ := json.Marshal(service.DirectTaskContext{Type: service.DirectTaskContextType, WorkspaceID: testWorkspaceID, EmployeeTaskID: task.ID, Prompt: "PRIVATE_EXECUTION_PROMPT", PrincipalID: testUserID})
	if _, err = testPool.Exec(ctx, `UPDATE agent_task_queue SET context=$2,trigger_evidence_ref_id=$3::uuid WHERE id=$1::uuid`, f.external, contextJSON, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.StartRun(ctx, task.Scope, task.ID, employeetask.StartRunParams{ExpectedVersion: task.Version, Source: employeetask.Source{Namespace: "executor-test", Key: "run"}, QueueTaskID: f.external}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_entry", "employee_task_run", "employee_task"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1::uuid AND agent_id=$2::uuid`, testWorkspaceID, f.agent)
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_scene WHERE id=$1`, sc.ID)
	})
	return employeeExecutionFixture{f, runtime}
}

func employeeExecutorPAT(t *testing.T, user string) string {
	t.Helper()
	raw, err := auth.GeneratePATToken()
	if err != nil {
		t.Fatal(err)
	}
	pat, err := testHandler.Queries.CreatePersonalAccessToken(context.Background(), db.CreatePersonalAccessTokenParams{UserID: parseUUID(user), Name: "executor-test", TokenHash: auth.HashToken(raw), TokenPrefix: raw[:12], ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, pat.ID)
	})
	return raw
}

func employeeExecutorRequest(t *testing.T, token, method string, body any, handler http.HandlerFunc, params ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := newRequest(method, "/", body)
	r = withWorkspaceAccessParams(r, params...)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityEmployeeDirectV1)
	w := httptest.NewRecorder()
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(handler).ServeHTTP(w, r)
	return w
}

func TestEmployeeDirectExecutionClaimRejectsMemberBeforeQueueMutation(t *testing.T) {
	for _, surface := range []string{"runtime", "targeted", "batch", "stale_runtime", "stale_batch"} {
		t.Run(surface, func(t *testing.T) {
			f := employeeExecutionDatabase(t)
			token := employeeExecutorPAT(t, f.plain)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, f.legacy); err != nil {
				t.Fatal(err)
			}
			stale := strings.HasPrefix(surface, "stale_")
			if stale {
				if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now()-interval '10 minutes',prepare_lease_expires_at=NULL WHERE id=$1::uuid`, f.external); err != nil {
					t.Fatal(err)
				}
			}
			before, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(f.external))
			if err != nil {
				t.Fatal(err)
			}
			var w *httptest.ResponseRecorder
			switch surface {
			case "batch", "stale_batch":
				w = employeeExecutorRequest(t, token, http.MethodPost, map[string]any{"daemon_id": f.runtime.DaemonID.String, "runtime_ids": []string{testRuntimeID}, "max_tasks": 1}, testHandler.ClaimTasksByRuntime)
			case "targeted":
				w = employeeExecutorRequest(t, token, http.MethodPost, map[string]any{"target_task_id": f.external}, testHandler.ClaimTaskByRuntime, "runtimeId", testRuntimeID)
			default:
				w = employeeExecutorRequest(t, token, http.MethodPost, nil, testHandler.ClaimTaskByRuntime, "runtimeId", testRuntimeID)
			}
			after, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(f.external))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(w.Body.String(), "PRIVATE_EXECUTION_PROMPT") || strings.Contains(w.Body.String(), "auth_token") {
				t.Errorf("unauthorized claim disclosed Direct input/token: HTTP %d", w.Code)
			}
			if after.Status != before.Status || after.DispatchedAt != before.DispatchedAt || after.PrepareLeaseExpiresAt != before.PrepareLeaseExpiresAt {
				t.Errorf("unauthorized claim mutated queue: before=%s after=%s", before.Status, after.Status)
			}
			var tokens int
			if err = testPool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id=$1::uuid`, f.external).Scan(&tokens); err != nil || tokens != 0 {
				t.Fatal("unauthorized executor minted token", tokens, err)
			}
		})
	}
}

func TestEmployeeDirectExecutionTerminalReplayCannotReadPrivateResult(t *testing.T) {
	for _, path := range []string{"complete", "fail"} {
		t.Run(path, func(t *testing.T) {
			f := employeeExecutionDatabase(t)
			if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, f.external); err != nil {
				t.Fatal(err)
			}
			handler := testHandler.CompleteTask
			body := map[string]any{"output": "attacker"}
			if path == "fail" {
				handler = testHandler.FailTask
				body = map[string]any{"error": "attacker"}
			}
			w := employeeExecutorRequest(t, employeeExecutorPAT(t, f.plain), http.MethodPost, body, handler, "taskId", f.external)
			if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "PRIVATE_CONNECTOR_RESULT") {
				t.Fatalf("terminal replay bypassed execution auth: HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEmployeeDirectExecutionAllTaskCallbacksRequireExecutor(t *testing.T) {
	f := employeeExecutionDatabase(t)
	token := employeeExecutorPAT(t, f.plain)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		body    any
	}{
		{"start", testHandler.StartTask, nil},
		{"wait", testHandler.MarkTaskWaitingLocalDirectory, map[string]any{"reason": "attacker"}},
		{"progress", testHandler.ReportTaskProgress, map[string]any{"summary": "attacker"}},
		{"usage", testHandler.ReportTaskUsage, map[string]any{"usage": []any{}}},
		{"messages", testHandler.ReportTaskMessages, map[string]any{"messages": []any{map[string]any{"seq": 2, "type": "text", "content": "attacker"}}}},
		{"session", testHandler.PinTaskSession, map[string]any{"session_id": "attacker"}},
		{"cancel_ack", testHandler.AckTaskCancelled, nil},
		{"prepare_lease", testHandler.ExtendTaskPrepareLease, nil},
		{"skill_bundles", testHandler.ResolveTaskSkillBundles, map[string]any{"skills": []any{}}},
		{"runtime_start", testHandler.RecordRuntimeStartEvent, map[string]any{"runtime_start_attempt_id": uuid.NewString(), "startup_status_protocol": service.RuntimeStartProtocolHTTPJSONV1, "event": "runner_started"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := employeeExecutorRequest(t, token, http.MethodPost, tc.body, tc.handler, "taskId", f.external, "runtimeId", testRuntimeID)
			if w.Code != http.StatusForbidden {
				t.Fatalf("callback bypassed executor auth: HTTP %d %s", w.Code, w.Body.String())
			}
		})
	}
	current, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(f.external))
	if err != nil || current.Status != "queued" || current.SessionID.Valid || current.WorkDir.Valid {
		t.Fatal("unauthorized callback changed queue", current.Status, err)
	}
}

func employeeExecutorDaemonToken(t *testing.T, workspaceID, daemonID string) string {
	t.Helper()
	raw, err := auth.GenerateDaemonToken()
	if err != nil {
		t.Fatal(err)
	}
	_, err = testHandler.Queries.CreateDaemonToken(context.Background(), db.CreateDaemonTokenParams{TokenHash: auth.HashToken(raw), WorkspaceID: parseUUID(workspaceID), DaemonID: daemonID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM daemon_token WHERE token_hash=$1`, auth.HashToken(raw))
	})
	return raw
}

func TestEmployeeDirectExecutionAuthenticatedRuntimeClaimsAndCompletes(t *testing.T) {
	for _, mode := range []string{"local_pat", "local_mdt", "fc_mdt"} {
		t.Run(mode, func(t *testing.T) {
			f := employeeExecutionDatabase(t)
			if mode == "fc_mdt" {
				if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET runtime_mode='cloud',metadata='{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"codex","artifact_kind":"e2b_template","artifact_ref":"test-immutable-r2","capabilities":["employee-direct-v1"]}'::jsonb WHERE id=$1::uuid`, testRuntimeID); err != nil {
					t.Fatal(err)
				}
			}
			token := employeeExecutorPAT(t, testUserID)
			if mode != "local_pat" {
				token = employeeExecutorDaemonToken(t, testWorkspaceID, f.runtime.DaemonID.String)
			}
			w := employeeExecutorRequest(t, token, http.MethodPost, map[string]any{"target_task_id": f.external}, testHandler.ClaimTaskByRuntime, "runtimeId", testRuntimeID)
			var claimed struct {
				Task *AgentTaskResponse `json:"task"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &claimed); err != nil || w.Code != http.StatusOK || claimed.Task == nil || claimed.Task.DirectTaskPrompt != "PRIVATE_EXECUTION_PROMPT" || claimed.Task.AuthToken == "" {
				t.Fatalf("authorized claim failed: HTTP %d error=%v", w.Code, err)
			}
			w = employeeExecutorRequest(t, token, http.MethodPost, nil, testHandler.StartTask, "taskId", f.external)
			if w.Code != http.StatusOK {
				t.Fatalf("authorized start: HTTP %d %s", w.Code, w.Body.String())
			}
			w = employeeExecutorRequest(t, token, http.MethodPost, map[string]any{"output": "AUTHORIZED_EXECUTION_OK"}, testHandler.CompleteTask, "taskId", f.external)
			if w.Code != http.StatusOK {
				t.Fatalf("authorized complete: HTTP %d %s", w.Code, w.Body.String())
			}
			var result string
			if err := testPool.QueryRow(context.Background(), `SELECT result FROM employee_task_run WHERE queue_task_id=$1::uuid`, f.external).Scan(&result); err != nil || result != "AUTHORIZED_EXECUTION_OK" {
				t.Fatal(result, err)
			}
		})
	}
}

func TestEmployeeDirectExecutionOrdinaryClaimAndRecoveryRetainLegacyContract(t *testing.T) {
	f := employeeExecutionDatabase(t)
	token := employeeExecutorPAT(t, f.plain)
	w := employeeExecutorRequest(t, token, http.MethodPost, nil, testHandler.ClaimTaskByRuntime, "runtimeId", testRuntimeID)
	var claimed struct {
		Task *AgentTaskResponse `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &claimed); err != nil || w.Code != http.StatusOK || claimed.Task == nil || claimed.Task.ID != f.legacy {
		t.Fatalf("ordinary member claim changed: HTTP %d error=%v", w.Code, err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running' WHERE id=ANY($1::uuid[])`, []string{f.external, f.legacy}); err != nil {
		t.Fatal(err)
	}
	w = employeeExecutorRequest(t, token, http.MethodPost, nil, testHandler.RecoverOrphanedTasks, "runtimeId", testRuntimeID)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	direct, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(f.external))
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(f.legacy))
	if err != nil {
		t.Fatal(err)
	}
	if direct.Status != "running" || ordinary.Status != "failed" {
		t.Fatalf("recovery crossed Direct boundary or changed ordinary tasks: direct=%s ordinary=%s", direct.Status, ordinary.Status)
	}
}

func TestEmployeeDirectExecutionManagersAndOtherDaemonsCannotExecute(t *testing.T) {
	for _, credential := range []string{"manager_pat", "other_mdt"} {
		t.Run(credential, func(t *testing.T) {
			f := employeeExecutionDatabase(t)
			var token string
			if credential == "manager_pat" {
				if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET owner_id=$2::uuid WHERE id=$1::uuid`, testRuntimeID, f.plain); err != nil {
					t.Fatal(err)
				}
				token = employeeExecutorPAT(t, testUserID)
			} else {
				token = employeeExecutorDaemonToken(t, testWorkspaceID, "different-runtime-daemon")
			}
			w := employeeExecutorRequest(t, token, http.MethodPost, map[string]any{"target_task_id": f.external}, testHandler.ClaimTaskByRuntime, "runtimeId", testRuntimeID)
			if strings.Contains(w.Body.String(), "PRIVATE_EXECUTION_PROMPT") {
				t.Fatal("read management or unrelated daemon granted execution")
			}
			w = employeeExecutorRequest(t, token, http.MethodPost, map[string]any{"output": "attacker"}, testHandler.CompleteTask, "taskId", f.external)
			if w.Code != http.StatusForbidden {
				t.Fatalf("wrong executor accepted: HTTP %d", w.Code)
			}
		})
	}
}

func TestEmployeeDirectExecutionWebSocketPreservesCredentialKind(t *testing.T) {
	for _, authPath := range []string{middleware.DaemonAuthPathPAT, middleware.DaemonAuthPathCloudPAT, middleware.DaemonAuthPathJWT} {
		t.Run(authPath, func(t *testing.T) {
			f := employeeExecutionDatabase(t)
			if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, f.legacy); err != nil {
				t.Fatal(err)
			}
			// These values are captured from the authenticated upgrade, not the
			// RPC body. A cloud node's associated owner is not a human PAT.
			identity := daemonws.ClientIdentity{UserID: testUserID, RuntimeIDs: []string{testRuntimeID}, WorkspaceID: testWorkspaceID, Capabilities: protocol.DaemonCapabilityEmployeeDirectV1}
			body, _ := json.Marshal(map[string]any{"daemon_id": f.runtime.DaemonID.String, "runtime_ids": []string{testRuntimeID}, "max_tasks": 1, "auth_path": "pat"})
			identity.AuthPath = authPath
			status, response, err := testHandler.DaemonRPCHandler(context.Background(), identity, "tasks.claim", body)
			if err != nil || status != http.StatusOK {
				t.Fatal(status, err)
			}
			got := strings.Contains(string(response), "PRIVATE_EXECUTION_PROMPT")
			if got != (authPath == middleware.DaemonAuthPathPAT) {
				t.Fatalf("WS credential kind lost: auth=%s claimedDirect=%v", authPath, got)
			}
		})
	}
}
