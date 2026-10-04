package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/evalcatalog"
	"github.com/multica-ai/multica/server/internal/evalreport"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func localEvalSubmission() evalreport.Submission {
	return evalreport.Submission{
		SchemaVersion: 1, RunID: uuid.NewString(), Title: "接口联调 · 模拟报告", ExecutionKind: "mock", Environment: "isolated-http-test",
		TargetRevision: strings.Repeat("a", 40), Runner: evalreport.Runner{Name: "fixture-runner", Version: "1"},
		StartedAt: time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 10, 4, 1, 1, 0, 0, time.UTC),
		Catalog:       evalreport.Catalog{Revision: strings.Repeat("b", 40), Dirty: true, SHA256: strings.Repeat("c", 64)},
		SelectedCases: []evalreport.CaseDefinition{{ID: "local-regression", Title: "本地新增的用例", Kind: "office", ScenarioID: "local-scene", Roles: []string{"测试执行者"}, Verifies: []string{"核对实际结果"}, Method: []string{"独立读回"}}},
		Results:       []evalreport.CaseResult{{CaseID: "local-regression", Status: "pass", Summary: "模拟断言通过", Evidence: []evalreport.Evidence{{Kind: "artifact", Reference: "evidence/mock-readback.json"}}}},
	}
}

func TestEvalReportStrictHumanGuardRejectsScopedServicePrincipal(t *testing.T) {
	for _, source := range []string{"workspace_access_token", "task_token", "cloud_pat", "workspace_mcp_token", "future_service"} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("X-Actor-Source", source)
		req.Header.Set("X-User-ID", uuid.NewString())
		wsID := uuid.NewString()
		wsUUID, _ := util.ParseUUID(wsID)
		userUUID, _ := util.ParseUUID(req.Header.Get("X-User-ID"))
		ctx := middleware.WithWorkspaceAccessPrincipal(req.Context(), middleware.WorkspaceAccessPrincipal{WorkspaceID: wsID, UserID: req.Header.Get("X-User-ID"), Permission: "all"})
		ctx = middleware.SetMemberContext(ctx, wsID, db.Member{WorkspaceID: wsUUID, UserID: userUUID, Role: "owner"})
		called := false
		guarded := RequireEvalReportHumanActor(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		recorder := httptest.NewRecorder()
		guarded.ServeHTTP(recorder, req.WithContext(ctx))
		if called || recorder.Code != http.StatusForbidden {
			t.Fatalf("service principal admitted: %s", source)
		}
	}
}

func TestEvalReportReplicaGateBlocksUnsupportedClusterBeforeWrite(t *testing.T) {
	for _, test := range []struct {
		ready  func(context.Context) (bool, error)
		status int
	}{
		{nil, 503},
		{func(context.Context) (bool, error) { return false, nil }, 503},
		{func(context.Context) (bool, error) { return false, errors.New("offline") }, 503},
		{func(context.Context) (bool, error) { return true, nil }, 204},
	} {
		called := false
		guarded := RequireEvalReportReplicas(test.ready)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
		recorder := httptest.NewRecorder()
		guarded.ServeHTTP(recorder, httptest.NewRequest("POST", "/", nil))
		if recorder.Code != test.status || called != (test.status == 204) {
			t.Fatal("replica gate allowed an incompatible write")
		}
	}
}

func TestEvalReportHTTPValidationAndUnavailableStorage(t *testing.T) {
	wsID, userID := uuid.NewString(), uuid.NewString()
	wsUUID, _ := util.ParseUUID(wsID)
	userUUID, _ := util.ParseUUID(userID)
	valid, _ := json.Marshal(localEvalSubmission())
	for _, test := range []struct {
		body   []byte
		media  string
		status int
	}{
		{valid, "text/plain", http.StatusUnsupportedMediaType},
		{[]byte(`{"schema_version":1,"submitted_by":"spoof"}`), "application/json", http.StatusBadRequest},
		{bytes.Repeat([]byte("x"), maxEvalReportBytes+1), "application/json", http.StatusRequestEntityTooLarge},
		{valid, "application/json", http.StatusServiceUnavailable},
	} {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(test.body))
		req.Header.Set("Content-Type", test.media)
		req.Header.Set("X-User-ID", userID)
		ctx := middleware.SetMemberContext(req.Context(), wsID, db.Member{WorkspaceID: wsUUID, UserID: userUUID, Role: "member"})
		recorder := httptest.NewRecorder()
		(&Handler{}).SubmitEvalReport(recorder, req.WithContext(ctx))
		if recorder.Code != test.status {
			t.Fatalf("status=%d expected=%d", recorder.Code, test.status)
		}
	}
}

func TestEvalReportHTTPPostReadBackAndRevocation(t *testing.T) {
	url := os.Getenv("EVAL_REPORT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("explicit isolated EVAL_REPORT_TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect isolated DB")
	}
	defer admin.Close(ctx)
	schema := "evalreport_http_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA "+ident+" CASCADE")
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, statement := range []string{
		`CREATE TABLE workspace(id uuid NOT NULL,name text NOT NULL)`,
		`CREATE UNIQUE INDEX CONCURRENTLY workspace_fixture_idx ON workspace(id)`,
		`CREATE TABLE member(id uuid NOT NULL,workspace_id uuid NOT NULL,user_id uuid NOT NULL,role text NOT NULL,created_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE UNIQUE INDEX CONCURRENTLY member_fixture_idx ON member(user_id,workspace_id)`,
	} {
		if _, err = pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"10020_eval_report.up.sql", "10021_eval_report_id_idx.up.sql", "10022_eval_report_run_idx.up.sql", "10023_eval_report_workspace_time_idx.up.sql", "10020_eval_report.up.sql", "10021_eval_report_id_idx.up.sql", "10022_eval_report_run_idx.up.sql", "10023_eval_report_workspace_time_idx.up.sql"} {
		data, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	wsID, userID, otherUser := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO workspace(id,name)VALUES($1,'接口联调隔离项目')`, wsID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO member(id,workspace_id,user_id,role)VALUES($1,$2,$3,'member')`, uuid.NewString(), wsID, userID); err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: pool, TxStarter: pool, Queries: db.New(pool)}
	router := chi.NewRouter()
	// The test supplies a synthetic verified principal; no real credentials are used.
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Fixture-Principal") == "outsider" {
				r.Header.Set("X-User-ID", otherUser)
			} else {
				r.Header.Set("X-User-ID", userID)
			}
			next.ServeHTTP(w, r)
		})
	})
	router.With(RequireEvalReportHumanActor).Get("/api/evals", evalcatalog.NewHandler(h.EvalReportPage).ServeHTTP)
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(RequireEvalReportHumanActor)
		r.Use(middleware.RequireWorkspaceMemberFromURL(h.Queries, "id"))
		r.Post("/eval-reports", h.SubmitEvalReport)
		r.Get("/eval-reports", h.ListEvalReports)
		r.Get("/eval-reports/{reportId}", h.GetEvalReport)
	})
	request := func(method, path, principal string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fixture-Principal", principal)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	input := localEvalSubmission()
	payload, _ := json.Marshal(input)
	path := "/api/workspaces/" + wsID + "/eval-reports"
	first := request("POST", path, "member", payload)
	if first.Code != 201 {
		t.Fatalf("submit: %d %s", first.Code, first.Body.String())
	}
	var receipt struct {
		ID      string             `json:"id"`
		Summary evalreport.Summary `json:"summary"`
	}
	if err = json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Summary.RealE2EPass != 0 || receipt.Summary.Pass != 1 {
		t.Fatal("mock conflated with real E2E")
	}
	replay := request("POST", path, "member", payload)
	if replay.Code != 200 {
		t.Fatal("replay failed", replay.Code)
	}
	input.Results[0].Summary = "different observation"
	changed, _ := json.Marshal(input)
	if got := request("POST", path, "member", changed); got.Code != 409 {
		t.Fatal("different content overwrote report", got.Code)
	}
	detail := request("GET", path+"/"+receipt.ID, "member", nil)
	var stored evalreport.Record
	if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &stored) != nil || stored.Submission.SelectedCases[0].Title != "本地新增的用例" {
		t.Fatal("snapshot readback failed")
	}
	list := request("GET", path, "member", nil)
	if list.Code != 200 || strings.Contains(list.Body.String(), "selected_cases") {
		t.Fatal("list should contain bounded metadata")
	}
	page := request("GET", "/api/evals?tab=reports&workspace_id="+wsID+"&report="+receipt.ID, "member", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "本地新增的用例") || !strings.Contains(page.Body.String(), "模拟测试") {
		t.Fatal("report page did not render stored snapshot", page.Code)
	}
	if outsider := request("GET", path+"/"+receipt.ID, "outsider", nil); outsider.Code != 404 {
		t.Fatal("cross-member report exposure", outsider.Code)
	}
	if _, err = pool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, wsID, userID); err != nil {
		t.Fatal(err)
	}
	if removed := request("GET", path+"/"+receipt.ID, "member", nil); removed.Code != 404 {
		t.Fatal("revoked member still read report", removed.Code)
	}
	if removed := request("GET", "/api/evals?tab=reports&workspace_id="+wsID+"&report="+receipt.ID, "member", nil); removed.Code != 404 {
		t.Fatal("revoked member still viewed report", removed.Code)
	}
}
