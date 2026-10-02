package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type directAccessFixture struct{ agent, originator, plain, owned, external, legacy string }

func employeeDirectAccessFixture(t *testing.T) directAccessFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("DATABASE_URL required")
	}
	ctx := context.Background()
	f := directAccessFixture{agent: createHandlerTestAgent(t, "direct-access-"+uuid.NewString(), nil)}
	var previousMode string
	if err := testPool.QueryRow(ctx, `SELECT runtime_mode FROM agent_runtime WHERE id=$1::uuid`, testRuntimeID).Scan(&previousMode); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode='local' WHERE id=$1::uuid`, testRuntimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET runtime_mode='local' WHERE id=$1::uuid`, f.agent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode=$1 WHERE id=$2::uuid`, previousMode, testRuntimeID)
	})
	for _, id := range []*string{&f.originator, &f.plain} {
		*id = uuid.NewString()
		if _, err := testPool.Exec(ctx, `INSERT INTO "user"(id,name,email) VALUES($1::uuid,'Direct member',$2)`, *id, *id+"@direct-access.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1::uuid,$2::uuid,'member')`, testWorkspaceID, *id); err != nil {
			t.Fatal(err)
		}
		user := *id
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id=$1::uuid`, user)
			_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1::uuid`, user)
		})
	}
	for _, row := range []struct {
		id     *string
		origin string
	}{{&f.owned, f.originator}, {&f.external, ""}} {
		*row.id = uuid.NewString()
		raw, _ := json.Marshal(service.DirectTaskContext{Type: service.DirectTaskContextType, WorkspaceID: testWorkspaceID, EmployeeTaskID: uuid.NewString(), Prompt: "PRIVATE_CONNECTOR_RESULT", PrincipalID: f.plain, OriginatorUserID: f.plain})
		var origin pgtype.UUID
		source := "owner_fallback"
		accountable := f.plain
		if row.origin != "" {
			origin = parseUUID(row.origin)
			source = "direct_human"
			accountable = row.origin
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context,originator_user_id,accountable_user_id,originator_source,trigger_evidence_kind,trigger_evidence_ref_id,trigger_summary,result) VALUES($1::uuid,$2::uuid,$3::uuid,'queued',$4,$5,$6::uuid,$7,'employee_task',$8::uuid,'PRIVATE_CONNECTOR_RESULT','{"output":"PRIVATE_CONNECTOR_RESULT"}'::jsonb)`, *row.id, f.agent, testRuntimeID, raw, origin, accountable, source, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO task_message(task_id,seq,type,content,output) VALUES($1::uuid,1,'text','PRIVATE_CONNECTOR_RESULT','PRIVATE_CONNECTOR_RESULT')`, *row.id); err != nil {
			t.Fatal(err)
		}
	}
	issueID := uuid.NewString()
	f.legacy = uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO issue(id,workspace_id,title,creator_type,creator_id) VALUES($1::uuid,$2::uuid,'Public issue','member',$3::uuid)`, issueID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,issue_id,status) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'queued')`, f.legacy, f.agent, testRuntimeID, issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []string{f.owned, f.external, f.legacy} {
			_, _ = testPool.Exec(ctx, `DELETE FROM task_token WHERE task_id=$1::uuid`, id)
			_, _ = testPool.Exec(ctx, `DELETE FROM task_message WHERE task_id=$1::uuid`, id)
			_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1::uuid`, id)
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id=$1::uuid`, issueID)
	})
	return f
}
func employeeAccessRequest(t *testing.T, user, task string) *http.Request {
	t.Helper()
	r := withURLParam(newRequestAs(user, http.MethodGet, "/", nil), "taskId", task)
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(user), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	return r.WithContext(middleware.SetMemberContext(r.Context(), testWorkspaceID, member))
}
func employeeAccessTaskIDs(t *testing.T, w *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("list HTTP %d: %s", w.Code, w.Body.String())
	}
	var rows []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}
func TestEmployeeDirectAccessListsFilterPrivateResults(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	for _, user := range []string{testUserID, f.originator, f.plain} {
		for _, surface := range []string{"agent", "snapshot", "daemon_pending"} {
			t.Run(user+"/"+surface, func(t *testing.T) {
				r := employeeAccessRequest(t, user, f.owned)
				w := httptest.NewRecorder()
				switch surface {
				case "agent":
					testHandler.ListAgentTasks(w, withURLParam(r, "id", f.agent))
				case "snapshot":
					testHandler.ListWorkspaceAgentTaskSnapshot(w, r)
				case "daemon_pending":
					testHandler.ListPendingTasksByRuntime(w, withURLParam(r, "runtimeId", testRuntimeID))
				}
				ids := employeeAccessTaskIDs(t, w)
				if ids[f.owned] != (user != f.plain) || ids[f.external] != (user == testUserID) || !ids[f.legacy] {
					t.Fatalf("incorrect visibility user=%s: %v", user, ids)
				}
			})
		}
	}
}
func TestEmployeeDirectAccessMessagesAndTrajectory(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	for _, user := range []string{testUserID, f.originator, f.plain} {
		for _, task := range []string{f.owned, f.external} {
			for _, surface := range []string{"messages", "daemon_messages", "trajectory_guard", "daemon_status", "daemon_gc"} {
				t.Run(user+"/"+task+"/"+surface, func(t *testing.T) {
					r := employeeAccessRequest(t, user, task)
					w := httptest.NewRecorder()
					want := user == testUserID || user == f.originator && task == f.owned
					switch surface {
					case "messages":
						testHandler.ListTaskMessagesByUser(w, r)
					case "daemon_messages":
						testHandler.ListTaskMessages(w, r)
					case "trajectory_guard":
						if _, ok := testHandler.requireUserTaskViewAccess(w, r, task); ok {
							w.WriteHeader(http.StatusOK)
						}
					case "daemon_status":
						testHandler.GetTaskStatus(w, r)
					case "daemon_gc":
						testHandler.GetTaskGCCheck(w, r)
					}
					if (w.Code == http.StatusOK) != want {
						t.Fatalf("HTTP %d allowed=%v body=%s", w.Code, want, w.Body.String())
					}
					if !want && strings.Contains(w.Body.String(), "PRIVATE_CONNECTOR_RESULT") {
						t.Fatal("private content leaked")
					}
				})
			}
		}
	}
}
func TestEmployeeDirectAccessIgnoresAgentHeaderImpersonation(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	r := employeeAccessRequest(t, f.plain, f.owned)
	r.Header.Set("X-Agent-ID", f.agent)
	r.Header.Set("X-Task-ID", f.owned)
	w := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("member spoofed Agent read: %s", w.Body.String())
	}
}
func TestEmployeeDirectAccessRealTaskTokenIsExact(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	_, err = testHandler.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{TokenHash: auth.HashToken(token), TaskID: parseUUID(f.owned), AgentID: parseUUID(f.agent), WorkspaceID: parseUUID(testWorkspaceID), UserID: parseUUID(testUserID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.owned, f.external} {
		req := withURLParam(httptest.NewRequest(http.MethodGet, "/", nil), "taskId", id)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Task-ID", id)
		req.Header.Set("X-Agent-ID", f.agent)
		w := httptest.NewRecorder()
		handler := middleware.Auth(testHandler.Queries, nil, nil)(middleware.RequireWorkspaceMember(testHandler.Queries)(http.HandlerFunc(testHandler.ListTaskMessagesByUser)))
		handler.ServeHTTP(w, req)
		if (w.Code == http.StatusOK) != (id == f.owned) {
			t.Fatalf("token inherited manager or wrong queue: id=%s HTTP %d %s", id, w.Code, w.Body.String())
		}
	}
}
func TestEmployeeDirectAccessEndpointActorNeedsCurrentManagement(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	endpoint, secret := createAgentDispatchEndpointForTest(t, testUserID, f.agent)
	for _, user := range []string{testUserID, f.plain} {
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_dispatch_endpoint SET actor_user_id=$1::uuid WHERE endpoint_id=$2`, user, endpoint); err != nil {
			t.Fatal(err)
		}
		r := withURLParam(withURLParam(httptest.NewRequest(http.MethodGet, "/", nil), "endpointId", endpoint), "taskId", f.external)
		// withURLParam replaces route context, so set both params together.
		rctx := chiRouteParamsForEmployeeAccess(r, endpoint, f.external)
		r = rctx
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		testHandler.ListAgentDispatchTaskMessages(w, r)
		if (w.Code == http.StatusOK) != (user == testUserID) {
			t.Fatalf("endpoint operator gained private requester rights: HTTP %d %s", w.Code, w.Body.String())
		}
	}
}

func chiRouteParamsForEmployeeAccess(r *http.Request, endpoint, task string) *http.Request {
	rc := chi.NewRouteContext()
	rc.URLParams.Add("endpointId", endpoint)
	rc.URLParams.Add("taskId", task)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
}

func TestEmployeeDirectAccessPreservesBoundDaemonAndRuntimeOwner(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	ctx := context.Background()
	var previousDaemon pgtype.Text
	if err := testPool.QueryRow(ctx, `SELECT daemon_id FROM agent_runtime WHERE id=$1::uuid`, testRuntimeID).Scan(&previousDaemon); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id='verified-daemon' WHERE id=$1::uuid`, testRuntimeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id=$1 WHERE id=$2::uuid`, previousDaemon, testRuntimeID)
	})
	r := employeeAccessRequest(t, f.plain, f.external)
	r = r.WithContext(middleware.WithDaemonContext(r.Context(), testWorkspaceID, "verified-daemon"))
	w := httptest.NewRecorder()
	testHandler.ListTaskMessages(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("bound daemon lost execution read: %d %s", w.Code, w.Body.String())
	}
	var previous pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT owner_id FROM agent_runtime WHERE id=$1::uuid`, testRuntimeID).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$1::uuid WHERE id=$2::uuid`, f.plain, testRuntimeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$1 WHERE id=$2::uuid`, previous, testRuntimeID)
	})
	w = httptest.NewRecorder()
	patRequest := employeeDirectPATRequest(t, f.plain, f.external)
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(testHandler.ListTaskMessages)).ServeHTTP(w, patRequest)
	if w.Code != http.StatusOK {
		t.Fatalf("runtime owner PAT lost actual execution read: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(w, employeeAccessRequest(t, f.plain, f.external))
	if w.Code == http.StatusOK {
		t.Fatal("runtime ownership became human inspection access")
	}
}

func TestEmployeeDirectAccessOriginatorSurvivesAgentVisibilityChange(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET permission_mode='private' WHERE id=$1::uuid`, f.agent); err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"agent", "snapshot", "messages"} {
		r := employeeAccessRequest(t, f.originator, f.owned)
		w := httptest.NewRecorder()
		switch surface {
		case "agent":
			testHandler.ListAgentTasks(w, withURLParam(r, "id", f.agent))
		case "snapshot":
			testHandler.ListWorkspaceAgentTaskSnapshot(w, r)
		case "messages":
			testHandler.ListTaskMessagesByUser(w, r)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("originator %s denied: %d %s", surface, w.Code, w.Body.String())
		}
		if surface != "messages" {
			ids := employeeAccessTaskIDs(t, w)
			if !ids[f.owned] || ids[f.external] || ids[f.legacy] {
				t.Fatalf("private Agent broadened task visibility: %v", ids)
			}
		}
	}
}

func TestEmployeeDirectAccessRealPATCannotSpoofDaemon(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	raw, err := auth.GeneratePATToken()
	if err != nil {
		t.Fatal(err)
	}
	pat, err := testHandler.Queries.CreatePersonalAccessToken(context.Background(), db.CreatePersonalAccessTokenParams{UserID: parseUUID(f.plain), Name: "direct-access", TokenHash: auth.HashToken(raw), TokenPrefix: raw[:12], ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, pat.ID)
	})
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/api/daemon/tasks/"+f.external+"/messages", nil), "taskId", f.external)
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", f.agent)
	req.Header.Set("X-Task-ID", f.external)
	req.Header.Set("X-Client", "multica-daemon")
	w := httptest.NewRecorder()
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(testHandler.ListTaskMessages)).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("PAT self-identified as executor: HTTP %d %s", w.Code, w.Body.String())
	}
}

func TestEmployeeDirectAccessTrajectoryDownloadDeniesBeforeStorage(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	w := httptest.NewRecorder()
	testHandler.GetDSHTrajectory(w, employeeAccessRequest(t, f.plain, f.owned))
	if w.Code != http.StatusForbidden {
		t.Fatalf("trajectory bypassed task guard: HTTP %d %s", w.Code, w.Body.String())
	}
}

func employeeDirectPATRequest(t *testing.T, user, task string) *http.Request {
	t.Helper()
	raw, err := auth.GeneratePATToken()
	if err != nil {
		t.Fatal(err)
	}
	pat, err := testHandler.Queries.CreatePersonalAccessToken(context.Background(), db.CreatePersonalAccessTokenParams{UserID: parseUUID(user), Name: "direct-executor", TokenHash: auth.HashToken(raw), TokenPrefix: raw[:12], ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, pat.ID)
	})
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/", nil), "taskId", task)
	req.Header.Set("Authorization", "Bearer "+raw)
	return req
}

func TestEmployeeDirectAccessMachineCredentialsCannotInheritUserRights(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	for _, source := range []string{"cloud_pat", "workspace_access_token", "workspace_mcp_token", "future_machine"} {
		for _, surface := range []string{"human", "daemon"} {
			t.Run(source+"/"+surface, func(t *testing.T) {
				r := employeeAccessRequest(t, testUserID, f.external)
				r.Header.Set("X-Actor-Source", source)
				w := httptest.NewRecorder()
				if surface == "human" {
					testHandler.ListTaskMessagesByUser(w, r)
				} else {
					testHandler.ListTaskMessages(w, r)
				}
				if w.Code != http.StatusForbidden {
					t.Fatalf("machine credential inherited manager/runtime-owner: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	r := employeeAccessRequest(t, testUserID, f.external)
	r = r.WithContext(middleware.WithWorkspaceAccessPrincipal(r.Context(), middleware.WorkspaceAccessPrincipal{UserID: testUserID, WorkspaceID: testWorkspaceID, Permission: "manage"}))
	w := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("workspace principal became a human when actor header was absent")
	}
}

func TestEmployeeDirectAccessRuntimeOwnerFallbackRequiresDaemonPAT(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	ctx := context.Background()
	var previous pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT owner_id FROM agent_runtime WHERE id=$1::uuid`, testRuntimeID).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$1::uuid WHERE id=$2::uuid`, f.plain, testRuntimeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE agent_runtime SET owner_id=$1 WHERE id=$2::uuid`, previous, testRuntimeID)
	})
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": f.plain, "exp": time.Now().Add(time.Hour).Unix()}).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatal(err)
	}
	req := withURLParam(httptest.NewRequest(http.MethodGet, "/", nil), "taskId", f.external)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(http.HandlerFunc(testHandler.ListTaskMessages)).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("runtime owner JWT gained PAT executor exception: %d %s", w.Code, w.Body.String())
	}
}

func TestEmployeeDirectAccessCancelCannotExposeOrMutatePrivateTask(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	ctx := context.Background()
	for _, status := range []string{"completed", "running"} {
		if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status=$1 WHERE id=$2::uuid`, status, f.owned); err != nil {
			t.Fatal(err)
		}
		r := employeeAccessRequest(t, f.plain, f.owned)
		r.Method = http.MethodPost
		w := httptest.NewRecorder()
		testHandler.CancelTaskByUser(w, r)
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "PRIVATE_CONNECTOR_RESULT") {
			t.Fatalf("cancel %s leaked or entered private mutation: %d %s", status, w.Code, w.Body.String())
		}
		task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(f.owned))
		if err != nil || task.Status != status {
			t.Fatalf("unauthorized cancel changed task: %s %v", task.Status, err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, f.owned); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{testUserID, f.originator} {
		r := employeeAccessRequest(t, user, f.owned)
		r.Method = http.MethodPost
		w := httptest.NewRecorder()
		testHandler.CancelTaskByUser(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("authorized idempotent cancel lost access: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestEmployeeDirectAccessDaemonTokenRequiresActualRuntimeBinding(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	r := employeeAccessRequest(t, f.plain, f.external)
	r = r.WithContext(middleware.WithDaemonContext(r.Context(), testWorkspaceID, "not-the-task-runtime-daemon"))
	w := httptest.NewRecorder()
	testHandler.ListTaskMessages(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("workspace daemon token read another daemon runtime: %d %s", w.Code, w.Body.String())
	}
}
