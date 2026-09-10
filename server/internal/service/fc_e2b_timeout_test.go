package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/chattrace"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type sandboxTimeoutAPI struct {
	mu sync.Mutex
	posts []string
	deletes []string
	expires map[string]time.Time
	failRenewal map[string]int
	shortRenewal map[string]bool
	failDelete bool
}

func newSandboxTimeoutAPI(t *testing.T) (*sandboxTimeoutAPI, *httptest.Server) {
	t.Helper()
	api := &sandboxTimeoutAPI{expires: make(map[string]time.Time), failRenewal: make(map[string]int), shortRenewal: make(map[string]bool)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if r.Header.Get("X-API-KEY") != "test-key" {
			t.Error("sandbox request did not include the configured API key")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/sandboxes/"), "/timeout")
		switch r.Method {
		case http.MethodPost:
			var body struct { Timeout int `json:"timeout"` }
			if !strings.HasSuffix(r.URL.Path, "/timeout") || json.NewDecoder(r.Body).Decode(&body) != nil || body.Timeout != 3600 {
				t.Error("sandbox renewal must POST timeout=3600")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			api.posts = append(api.posts, id)
			if code := api.failRenewal[id]; code != 0 {
				w.WriteHeader(code)
				return
			}
			api.expires[id] = time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			if api.shortRenewal[id] { api.expires[id] = time.Now().Add(time.Minute) }
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"sandboxID": id, "state": "running", "endAt": api.expires[id]})
		case http.MethodDelete:
			api.deletes = append(api.deletes, id)
			if api.failDelete { w.WriteHeader(http.StatusServiceUnavailable); return }
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return api, server
}

func TestResolveSandboxRenewsEveryWarmAcquisition(t *testing.T) {
	pool := newSandboxLockPool(t)
	workspaceID, _, runtimeID := seedFCE2BSandboxRuntime(t, pool, "Warm Sandbox Renewal")
	queries := db.New(pool)
	scope := fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: workspaceID}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, runtimeID) })
	if _, err := queries.UpsertFCE2BSandboxSession(context.Background(), db.UpsertFCE2BSandboxSessionParams{
		WorkspaceID: workspaceID, RuntimeID: runtimeID, ScopeType: scope.typ, ScopeID: scope.id,
		SandboxID: "sbx_warm", Template: "tpl_test", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(40*time.Minute), Valid: true},
	}); err != nil { t.Fatal(err) }
	api, server := newSandboxTimeoutAPI(t)
	runner := &countingRunner{}
	launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{APIURL: server.URL, APIKey: "test-key", TimeoutSeconds: 300}, runner)
	launcher.SetPool(pool)
	runtime := db.AgentRuntime{ID: runtimeID, WorkspaceID: workspaceID}
	for range 2 {
		id, cold, err := launcher.resolveSandbox(context.Background(), runtime, scope, true, "tpl_test", chattrace.New("task"))
		if err != nil || id != "sbx_warm" || cold { t.Fatalf("warm acquisition = (%q, %v, %v)", id, cold, err) }
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if !reflect.DeepEqual(api.posts, []string{"sbx_warm", "sbx_warm"}) { t.Fatalf("renewal calls = %v; each acquisition must reset the timeout", api.posts) }
	if runner.createCount() != 0 || len(api.deletes) != 0 { t.Fatal("successful renewal must reuse the sandbox") }
	session, err := queries.GetActiveFCE2BSandboxSession(context.Background(), db.GetActiveFCE2BSandboxSessionParams{RuntimeID: runtimeID, ScopeType: scope.typ, ScopeID: scope.id, Template: "tpl_test"})
	if err != nil || !session.ExpiresAt.Time.Equal(api.expires["sbx_warm"]) { t.Fatalf("database expiry = %v, error = %v; want provider expiry %v", session.ExpiresAt.Time, err, api.expires["sbx_warm"]) }
}

func TestResolveSandboxReplacesFailedRenewal(t *testing.T) {
	for _, scenario := range []string{"provider_error", "insufficient_expiry", "release_error", "replacement_renewal_error"} {
		t.Run(scenario, func(t *testing.T) {
			pool := newSandboxLockPool(t)
			workspaceID, _, runtimeID := seedFCE2BSandboxRuntime(t, pool, "Sandbox Renewal Replacement")
			queries := db.New(pool)
			scope := fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: workspaceID}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, runtimeID) })
			if _, err := queries.UpsertFCE2BSandboxSession(context.Background(), db.UpsertFCE2BSandboxSessionParams{
				WorkspaceID: workspaceID, RuntimeID: runtimeID, ScopeType: scope.typ, ScopeID: scope.id,
				SandboxID: "sbx_warm", Template: "tpl_test", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			}); err != nil { t.Fatal(err) }
			api, server := newSandboxTimeoutAPI(t)
			api.failRenewal["sbx_warm"] = http.StatusServiceUnavailable
			if scenario == "insufficient_expiry" { delete(api.failRenewal, "sbx_warm"); api.shortRenewal["sbx_warm"] = true }
			if scenario == "release_error" { api.failDelete = true }
			if scenario == "replacement_renewal_error" { api.failRenewal["sbx_1"] = http.StatusServiceUnavailable }
			runner := &countingRunner{}
			launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{APIURL: server.URL, APIKey: "test-key", TimeoutSeconds: 3600}, runner)
			launcher.SetPool(pool)
			id, cold, err := launcher.resolveSandbox(context.Background(), db.AgentRuntime{ID: runtimeID, WorkspaceID: workspaceID}, scope, true, "tpl_test", chattrace.New("task"))
			if scenario == "replacement_renewal_error" {
				if err == nil || id != "" { t.Fatalf("failed replacement returned (%q, %v)", id, err) }
			} else if err != nil || id != "sbx_1" || !cold { t.Fatalf("replacement = (%q, %v, %v)", id, cold, err) }
			if runner.createCount() != 1 { t.Fatalf("created %d replacement sandboxes, want one", runner.createCount()) }
			api.mu.Lock()
			defer api.mu.Unlock()
			if !reflect.DeepEqual(api.posts, []string{"sbx_warm", "sbx_1"}) { t.Fatalf("renewal calls = %v", api.posts) }
			wantDeletes := []string{"sbx_warm"}
			if scenario == "replacement_renewal_error" { wantDeletes = append(wantDeletes, "sbx_1") }
			if !reflect.DeepEqual(api.deletes, wantDeletes) { t.Fatalf("released = %v, want %v", api.deletes, wantDeletes) }
			var savedID, status string
			if err := pool.QueryRow(context.Background(), `SELECT sandbox_id, status FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, runtimeID).Scan(&savedID, &status); err != nil { t.Fatal(err) }
			if scenario == "replacement_renewal_error" {
				if status != "stale" { t.Fatalf("failed old sandbox remains reusable: %s", status) }
			} else if savedID != "sbx_1" || status != "running" { t.Fatalf("session = (%s, %s)", savedID, status) }
		})
	}
}

func TestResolveSandboxRenewsNewSandboxAndBoundsReplacement(t *testing.T) {
	for _, failCount := range []int{0, 1, 2} {
		t.Run(string(rune('0'+failCount)), func(t *testing.T) {
			api, server := newSandboxTimeoutAPI(t)
			if failCount > 0 { api.failRenewal["sbx_1"] = http.StatusServiceUnavailable }
			if failCount > 1 { api.failRenewal["sbx_2"] = http.StatusServiceUnavailable }
			runner := &countingRunner{}
			launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{APIURL: server.URL, APIKey: "test-key", TimeoutSeconds: 300}, runner)
			id, cold, err := launcher.resolveSandbox(context.Background(), db.AgentRuntime{}, fcE2BTaskScope{}, false, "tpl_test", chattrace.New("task"))
			if failCount == 2 {
				if err == nil || id != "" { t.Fatalf("exhausted renewal returned (%q, %v)", id, err) }
			} else {
				want := "sbx_1"
				if failCount == 1 { want = "sbx_2" }
				if err != nil || id != want || !cold { t.Fatalf("new sandbox = (%q, %v, %v), want %s", id, cold, err, want) }
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			wantCreates := 1
			if failCount > 0 { wantCreates = 2 }
			if runner.createCount() != wantCreates || len(api.posts) != wantCreates || len(api.deletes) != failCount { t.Fatalf("creates=%d renewals=%v deletes=%v", runner.createCount(), api.posts, api.deletes) }
		})
	}
}
