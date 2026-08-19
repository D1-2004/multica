package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

func TestHealthHandlerReportsCLIVersionAndTaskCounts(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg: Config{
			CLIVersion:    "v9.9.9",
			DaemonID:      "daemon-test",
			DeviceName:    "dev",
			ServerBaseURL: "http://localhost:8080",
		},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	d.activeTasks.Store(2)
	d.runningTasks.Store(1)
	d.resourceWaitTasks.Store(1)
	d.ready.Store(true) // preflight done -> status should be "running"

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Decode into a raw map so the test locks in the exact wire-level JSON
	// keys — the desktop TS client depends on snake_case (cli_version,
	// active_task_count), so a silent struct-tag rename must fail here. The
	// execution/wait split is additive: active_task_count keeps its ownership
	// semantics for old clients and restart barriers.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if got, want := raw["cli_version"], "v9.9.9"; got != want {
		t.Errorf("cli_version key: got %v, want %q", got, want)
	}
	// JSON numbers decode to float64 through map[string]any.
	if got, want := raw["active_task_count"], float64(2); got != want {
		t.Errorf("active_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["running_task_count"], float64(1); got != want {
		t.Errorf("running_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["resource_wait_task_count"], float64(1); got != want {
		t.Errorf("resource_wait_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["status"], "running"; got != want {
		t.Errorf("status key: got %v, want %q", got, want)
	}
	if got, _ := raw["control_nonce"].(string); got == "" {
		t.Error("control_nonce key must expose the current daemon instance challenge")
	}
	// The desktop relies on the `os` key (runtime.GOOS) to detect a daemon it
	// can't manage (e.g. Linux-in-WSL behind a Windows desktop). A rename or
	// drop would silently re-break #3916, so lock both the key and its value.
	if got, want := raw["os"], runtime.GOOS; got != want {
		t.Errorf("os key: got %v, want %q", got, want)
	}

	// Also round-trip into the typed struct as a separate check that the
	// field values match, independent of key naming.
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode typed response: %v", err)
	}
	if resp.CLIVersion != "v9.9.9" {
		t.Errorf("CLIVersion: got %q, want %q", resp.CLIVersion, "v9.9.9")
	}
	if resp.ActiveTaskCount != 2 {
		t.Errorf("ActiveTaskCount: got %d, want 2", resp.ActiveTaskCount)
	}
	if resp.RunningTaskCount != 1 {
		t.Errorf("RunningTaskCount: got %d, want 1", resp.RunningTaskCount)
	}
	if resp.ResourceWaitTaskCount != 1 {
		t.Errorf("ResourceWaitTaskCount: got %d, want 1", resp.ResourceWaitTaskCount)
	}
}

// TestHealthHandlerReportsDeferredReload covers the "while waiting to restart,
// the reason and state are visible" criterion. When trySelfReload has confirmed
// a multica version change but the daemon was busy at the barrier check, the
// only way a user can tell why the daemon is still on the old version is this
// field. It is omitempty, so an idle daemon must not emit the key at all.
func TestHealthHandlerReportsDeferredReload(t *testing.T) {
	t.Parallel()

	newHealthProbe := func(t *testing.T) (*Daemon, func() map[string]any) {
		t.Helper()
		d := &Daemon{
			cfg:        Config{CLIVersion: "0.3.7"},
			workspaces: map[string]*workspaceState{},
			logger:     slog.Default(),
		}
		d.ready.Store(true)
		return d, func() map[string]any {
			rec := httptest.NewRecorder()
			d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			var raw map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode raw response: %v", err)
			}
			return raw
		}
	}

	t.Run("absent when nothing pending", func(t *testing.T) {
		_, probe := newHealthProbe(t)
		if _, present := probe()["reload_pending_reason"]; present {
			t.Error("reload_pending_reason must be omitted when no restart is pending")
		}
	})

	t.Run("explains a deferred restart", func(t *testing.T) {
		d, probe := newHealthProbe(t)
		d.setReloadPending("multica binary on disk reports 0.3.8, running 0.3.7")

		got, _ := probe()["reload_pending_reason"].(string)
		if !strings.Contains(got, "0.3.8") {
			t.Errorf("reload_pending_reason = %q, want it to name the version on disk", got)
		}
	})
}

// TestHealthHandlerReportsStartingUntilReady pins the liveness/readiness split:
// the health server binds and answers before preflight finishes, but it must
// report "starting" until d.ready is set, and only then "running". Otherwise a
// slow or failing preflight would be misreported to `daemon start` (and the
// desktop) as a fully started daemon.
func TestHealthHandlerReportsStartingUntilReady(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	readStatus := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var resp HealthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp.Status
	}

	if got := readStatus(); got != "starting" {
		t.Fatalf("status before ready: got %q, want \"starting\"", got)
	}

	d.ready.Store(true)

	if got := readStatus(); got != "running" {
		t.Fatalf("status after ready: got %q, want \"running\"", got)
	}
}

func TestHealthHandlerActiveTaskCountTracksCounter(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	// Simulate the pollLoop increment/decrement protocol.
	d.activeTasks.Add(1)
	d.activeTasks.Add(1)
	assertActiveTaskCount(t, handler, 2)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 1)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 0)
}

func TestShutdownHandlerPostCancelsDaemonContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Daemon{
		cfg:               Config{DaemonID: "daemon-test"},
		client:            NewClient("http://unused.test"),
		cancelFunc:        cancel,
		localControlNonce: "nonce-test",
	}
	d.client.SetToken("mul_profile_secret")
	controlToken, err := DeriveLocalControlToken(d.client.Token(), d.cfg.DaemonID, d.localControlNonce)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer "+controlToken)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("daemon context was not cancelled after POST /shutdown")
	}
}

func TestShutdownHandlerRejectsMissingAndTaskCapabilities(t *testing.T) {
	t.Parallel()

	cancelled := false
	d := &Daemon{
		cfg:               Config{DaemonID: "daemon-test"},
		client:            NewClient("http://unused.test"),
		cancelFunc:        func() { cancelled = true },
		localControlNonce: "nonce-test",
	}
	d.client.SetToken("mul_profile_secret")
	release, err := d.registerLocalTaskCapability(Task{
		ID:          "task-1",
		WorkspaceID: "ws-1",
	}, "mat_task_secret", "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	for _, token := range []string{"", "mat_task_secret", "mdc_forged"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		d.shutdownHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: expected 401, got %d", token, rec.Code)
		}
	}

	time.Sleep(10 * time.Millisecond)
	if cancelled {
		t.Fatal("unauthorized shutdown request cancelled daemon context")
	}
}

func TestDeriveLocalControlTokenIsInstanceBound(t *testing.T) {
	t.Parallel()

	tokenA, err := DeriveLocalControlToken("mul_profile_secret", "daemon-test", "nonce-a")
	if err != nil {
		t.Fatal(err)
	}
	tokenB, err := DeriveLocalControlToken("mul_profile_secret", "daemon-test", "nonce-b")
	if err != nil {
		t.Fatal(err)
	}
	if tokenA == tokenB {
		t.Fatal("control token must change with the daemon instance nonce")
	}
	if !strings.HasPrefix(tokenA, "mdc_") {
		t.Fatalf("control token = %q, want mdc_ prefix", tokenA)
	}
}

func TestShutdownHandlerRejectsNonPost(t *testing.T) {
	t.Parallel()

	cancelled := false
	d := &Daemon{cancelFunc: func() { cancelled = true }}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	// Give the handler's deferred cancel goroutine a moment to fire
	// in case a bug causes it to run anyway.
	time.Sleep(10 * time.Millisecond)
	if cancelled {
		t.Fatal("GET request should not trigger cancellation")
	}
}

func TestHealthHandlerRespondsWhileTaskRepoLookupWaits(t *testing.T) {
	const workspaceID = "ws-health"
	const repoURL = "https://github.com/org/repo.git"
	cache := newBlockingLookupRepoCache("/cache/org/repo.git")
	d := &Daemon{
		cfg: Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{
			workspaceID: {
				workspaceID:     workspaceID,
				runtimeIDs:      []string{"rt-1"},
				allowedRepoURLs: map[string]struct{}{repoURL: {}},
				taskRepoURLs:    map[string]struct{}{},
			},
		},
		repoCache: cache,
		logger:    slog.Default(),
	}
	defer cache.release()

	registerDone := make(chan struct{})
	go func() {
		d.registerTaskRepos(workspaceID, "task-health", []RepoData{{URL: repoURL}})
		close(registerDone)
	}()
	cache.waitForLookup(t)

	rec := httptest.NewRecorder()
	healthDone := make(chan struct{})
	go func() {
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		close(healthDone)
	}()

	select {
	case <-healthDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("/health blocked behind task repo cache lookup")
	}

	cache.release()
	select {
	case <-registerDone:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not unblock after repo lookup finished")
	}
}

func TestRepoCheckoutUsesTaskScopedProjectRefByDefault(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	d.registerTaskRepos(workspaceID, "task-1", []RepoData{{URL: repoURL, Ref: "release/v2"}})
	release := registerRepoCheckoutCapability(t, d, workspaceID, "task-1", "/tmp/work", repoURL)
	defer release()

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", body)
	req.Header.Set("Authorization", "Bearer mat_repo_checkout_test")
	d.repoCheckoutHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams().Ref; got != "release/v2" {
		t.Fatalf("CreateWorktree Ref = %q, want release/v2", got)
	}
}

func TestRepoCheckoutExplicitRefOverridesProjectDefault(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	d.registerTaskRepos(workspaceID, "task-1", []RepoData{{URL: repoURL, Ref: "release/v2"}})
	release := registerRepoCheckoutCapability(t, d, workspaceID, "task-1", "/tmp/work", repoURL)
	defer release()

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-1","ref":"hotfix"}`)
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", body)
	req.Header.Set("Authorization", "Bearer mat_repo_checkout_test")
	d.repoCheckoutHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams().Ref; got != "hotfix" {
		t.Fatalf("CreateWorktree Ref = %q, want explicit hotfix", got)
	}
}

func TestRepoCheckoutCapabilityIsBoundAndExpires(t *testing.T) {
	t.Parallel()

	const (
		workspaceID = "ws-checkout"
		repoURL     = "https://github.com/org/repo.git"
	)
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	release := registerRepoCheckoutCapability(t, d, workspaceID, "task-1", "/tmp/work", repoURL)

	tests := []struct {
		name  string
		token string
		body  string
		want  int
	}{
		{
			name: "missing bearer",
			body: `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-1"}`,
			want: http.StatusUnauthorized,
		},
		{
			name:  "forged bearer",
			token: "mat_forged",
			body:  `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-1"}`,
			want:  http.StatusUnauthorized,
		},
		{
			name:  "other task",
			token: "mat_repo_checkout_test",
			body:  `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-2"}`,
			want:  http.StatusForbidden,
		},
		{
			name:  "other workspace",
			token: "mat_repo_checkout_test",
			body:  `{"url":"` + repoURL + `","workspace_id":"ws-other","workdir":"/tmp/work","task_id":"task-1"}`,
			want:  http.StatusForbidden,
		},
		{
			name:  "other workdir",
			token: "mat_repo_checkout_test",
			body:  `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/other","task_id":"task-1"}`,
			want:  http.StatusForbidden,
		},
		{
			name:  "repo outside task",
			token: "mat_repo_checkout_test",
			body:  `{"url":"https://github.com/org/private.git","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-1"}`,
			want:  http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/repo/checkout", strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			d.repoCheckoutHandler().ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("expected %d, got %d: %s", tt.want, rec.Code, rec.Body.String())
			}
		})
	}

	release()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", strings.NewReader(
		`{"url":"`+repoURL+`","workspace_id":"`+workspaceID+`","workdir":"/tmp/work","task_id":"task-1"}`,
	))
	req.Header.Set("Authorization", "Bearer mat_repo_checkout_test")
	d.repoCheckoutHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired capability: expected 401, got %d", rec.Code)
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("unauthorized requests reached repo cache: %+v", got)
	}
}

func TestA2ATaskDoesNotRegisterRepoCheckoutCapability(t *testing.T) {
	t.Parallel()

	d := &Daemon{}
	release, err := d.registerLocalTaskCapability(Task{
		ID:            "task-a2a",
		WorkspaceID:   "ws-a2a",
		A2AInvocation: true,
		Repos:         []RepoData{{URL: "https://github.com/org/repo.git"}},
	}, "", "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	release()

	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer mat_guessed")
	rec := httptest.NewRecorder()
	d.repoCheckoutHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func registerRepoCheckoutCapability(t *testing.T, d *Daemon, workspaceID, taskID, workDir, repoURL string) func() {
	t.Helper()
	release, err := d.registerLocalTaskCapability(Task{
		ID:          taskID,
		WorkspaceID: workspaceID,
		Repos:       []RepoData{{URL: repoURL}},
	}, "mat_repo_checkout_test", workDir)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestRepoCheckoutForwardsIsolatedMode(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	workDir := t.TempDir()
	release := registerRepoCheckoutCapability(t, d, workspaceID, "task-1", workDir, repoURL)
	t.Cleanup(release)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","checkout_mode":"isolated"}`)
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", body)
	req.Header.Set("Authorization", "Bearer mat_repo_checkout_test")
	d.repoCheckoutHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !cache.lastCreateParams().IsolatedGitMetadata {
		t.Fatal("isolated checkout_mode was not forwarded to repo cache")
	}
}

func TestRepoCheckoutRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	workDir := t.TempDir()
	release := registerRepoCheckoutCapability(t, d, workspaceID, "task-1", workDir, repoURL)
	t.Cleanup(release)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","checkout_mode":"unsafe"}`)
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", body)
	req.Header.Set("Authorization", "Bearer mat_repo_checkout_test")
	d.repoCheckoutHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("invalid checkout mode reached repo cache: %+v", got)
	}
}

func newRepoCheckoutTestDaemon(t *testing.T, workspaceID, repoURL string, cache *recordingRepoCache) *Daemon {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/daemon/workspaces/"+workspaceID+"/repos" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(WorkspaceReposResponse{
			WorkspaceID:  workspaceID,
			Repos:        []RepoData{{URL: repoURL}},
			ReposVersion: "v1",
		})
	}))
	t.Cleanup(srv.Close)
	return &Daemon{
		cfg:       Config{CLIVersion: "v1.0.0"},
		client:    NewClient(srv.URL),
		repoCache: cache,
		workspaces: map[string]*workspaceState{
			workspaceID: newWorkspaceState(workspaceID, nil, "", []RepoData{{URL: repoURL}}, nil),
		},
		logger: slog.Default(),
	}
}

type blockingLookupRepoCache struct {
	path          string
	lookupSeen    chan struct{}
	releaseLookup chan struct{}
	releaseOnce   sync.Once
}

func newBlockingLookupRepoCache(path string) *blockingLookupRepoCache {
	return &blockingLookupRepoCache{
		path:          path,
		lookupSeen:    make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
}

func (c *blockingLookupRepoCache) BarePath(_, _ string) string {
	return ""
}

func (c *blockingLookupRepoCache) Lookup(_, _ string) string {
	select {
	case <-c.lookupSeen:
	default:
		close(c.lookupSeen)
	}
	<-c.releaseLookup
	return c.path
}

func (c *blockingLookupRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *blockingLookupRepoCache) WithRepoLock(_ string, fn func() error) error {
	return fn()
}

func (c *blockingLookupRepoCache) CreateWorktree(repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	return nil, nil
}

type recordingRepoCache struct {
	lookupPath string
	mu         sync.Mutex
	params     []repocache.WorktreeParams
}

func (c *recordingRepoCache) Lookup(_, _ string) string {
	return c.lookupPath
}

func (c *recordingRepoCache) BarePath(_, _ string) string {
	return c.lookupPath
}

func (c *recordingRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *recordingRepoCache) WithRepoLock(_ string, fn func() error) error {
	return fn()
}

func (c *recordingRepoCache) CreateWorktree(params repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.params = append(c.params, params)
	return &repocache.WorktreeResult{Path: params.WorkDir, BranchName: "agent/test"}, nil
}

func (c *recordingRepoCache) lastCreateParams() repocache.WorktreeParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.params) == 0 {
		return repocache.WorktreeParams{}
	}
	return c.params[len(c.params)-1]
}

func (c *blockingLookupRepoCache) waitForLookup(t *testing.T) {
	t.Helper()
	select {
	case <-c.lookupSeen:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not call repo lookup")
	}
}

func (c *blockingLookupRepoCache) release() {
	c.releaseOnce.Do(func() {
		close(c.releaseLookup)
	})
}

func assertActiveTaskCount(t *testing.T, h http.HandlerFunc, want int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ActiveTaskCount != want {
		t.Errorf("active_task_count: got %d, want %d", resp.ActiveTaskCount, want)
	}
}
