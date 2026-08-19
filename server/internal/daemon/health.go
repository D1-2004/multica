package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// HealthResponse is returned by the daemon's local health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
	PID    int    `json:"pid"`
	// OS is the daemon's runtime.GOOS. The desktop app compares it against its
	// own host OS to detect a daemon it cannot manage — e.g. a Windows desktop
	// reaching a Linux daemon inside WSL2 over localhost forwarding. The
	// lifecycle CLI (`daemon start/stop`) acts on the host process namespace,
	// so a foreign-OS daemon can't be started/stopped by the app even though
	// /health is reachable. See #3916.
	OS         string `json:"os"`
	Uptime     string `json:"uptime"`
	DaemonID   string `json:"daemon_id"`
	DeviceName string `json:"device_name"`
	ServerURL  string `json:"server_url"`
	CLIVersion string `json:"cli_version"`
	// ControlNonce is a public, per-process challenge. The management CLI
	// combines it with the profile credential to derive a shutdown-only token;
	// publishing the nonce does not disclose either credential.
	ControlNonce string `json:"control_nonce,omitempty"`
	// ActiveTaskCount remains the compatibility/safety count of every claimed
	// handleTask lifecycle. The additive counters split actual provider
	// execution from local-directory parking for throughput and diagnostics.
	ActiveTaskCount       int64    `json:"active_task_count"`
	RunningTaskCount      int64    `json:"running_task_count"`
	ResourceWaitTaskCount int64    `json:"resource_wait_task_count"`
	Agents                []string `json:"agents"`
	// SkippedAgents maps a provider that WAS discovered on this machine to the
	// reason the last registration round dropped it (version undetectable,
	// below the minimum supported version). Purely diagnostic, and omitted when
	// empty so older consumers see no change.
	//
	// Without it, "CLI not installed" and "CLI installed but rejected" both
	// render as an absent runtime, which is what made GH #6077 unactionable for
	// the reporter (MUL-5439).
	SkippedAgents map[string]string `json:"skipped_agents,omitempty"`
	// ReloadPendingReason explains why the daemon has confirmed a multica
	// version change on disk but hasn't restarted into it yet — it was busy at
	// the last barrier check and will retry when idle. Omitted when empty, so
	// older consumers see no change. Diagnostic only: nothing keys off it.
	ReloadPendingReason string            `json:"reload_pending_reason,omitempty"`
	Workspaces          []healthWorkspace `json:"workspaces"`
}

type healthWorkspace struct {
	ID       string   `json:"id"`
	Runtimes []string `json:"runtimes"`
}

// listenHealth binds the health port. Returns the listener or an error if
// another daemon is already running (port taken).
func (d *Daemon) listenHealth() (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", d.cfg.HealthPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("another daemon is already running on %s: %w", addr, err)
	}
	return ln, nil
}

// repoCheckoutRequest is the body of a POST /repo/checkout request.
type repoCheckoutRequest struct {
	URL          string `json:"url"`
	WorkspaceID  string `json:"workspace_id"`
	WorkDir      string `json:"workdir"`
	Ref          string `json:"ref,omitempty"`
	AgentName    string `json:"agent_name"`
	TaskID       string `json:"task_id"`
	CheckoutMode string `json:"checkout_mode,omitempty"`
}

const localControlTokenDomain = "multica-local-control-v1"

type localTaskCapability struct {
	mu          sync.RWMutex
	active      bool
	taskID      string
	workspaceID string
	workDir     string
	allowedRepo map[string]struct{}
}

// DeriveLocalControlToken derives a daemon-local, shutdown-only bearer token
// without sending the profile credential itself over the loopback endpoint.
// The per-process nonce makes a value obtained from a stale or spoofed health
// server unusable against a later daemon instance.
func DeriveLocalControlToken(authToken, daemonID, nonce string) (string, error) {
	authToken = strings.TrimSpace(authToken)
	daemonID = strings.TrimSpace(daemonID)
	nonce = strings.TrimSpace(nonce)
	if authToken == "" {
		return "", errors.New("daemon auth token is required")
	}
	if daemonID == "" {
		return "", errors.New("daemon id is required")
	}
	if nonce == "" {
		return "", errors.New("daemon control nonce is required")
	}

	mac := hmac.New(sha256.New, []byte(authToken))
	_, _ = mac.Write([]byte(localControlTokenDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(daemonID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(nonce))
	return "mdc_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (d *Daemon) controlNonce() (string, error) {
	d.localControlNonceOnce.Do(func() {
		if strings.TrimSpace(d.localControlNonce) != "" {
			return
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			d.localControlNonceErr = fmt.Errorf("generate local control nonce: %w", err)
			return
		}
		d.localControlNonce = base64.RawURLEncoding.EncodeToString(raw[:])
	})
	return d.localControlNonce, d.localControlNonceErr
}

func localBearerToken(r *http.Request) (string, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func writeLocalUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="multica-daemon"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func canonicalLocalWorkDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workdir is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return abs, nil
}

// registerLocalTaskCapability makes repo checkout available only while the
// corresponding child is executing. The map stores a one-way digest instead
// of the bearer token and every authorization is bound to the claimed task,
// workspace, prepared workdir, and task-visible repo set.
func (d *Daemon) registerLocalTaskCapability(task Task, token, workDir string) (func(), error) {
	token = strings.TrimSpace(token)
	if task.A2AInvocation {
		if token != "" {
			return nil, errors.New("A2A task must not receive a local task capability")
		}
		return func() {}, nil
	}
	if !strings.HasPrefix(token, "mat_") {
		return nil, errors.New("local task capability must use a task-scoped token")
	}
	taskID := strings.TrimSpace(task.ID)
	workspaceID := strings.TrimSpace(task.WorkspaceID)
	if taskID == "" || workspaceID == "" {
		return nil, errors.New("local task capability requires task and workspace ids")
	}
	canonicalWorkDir, err := canonicalLocalWorkDir(workDir)
	if err != nil {
		return nil, err
	}
	allowedRepo := make(map[string]struct{}, len(task.Repos))
	for _, repo := range task.Repos {
		if url := strings.TrimSpace(repo.URL); url != "" {
			allowedRepo[url] = struct{}{}
		}
	}
	capability := &localTaskCapability{
		active:      true,
		taskID:      taskID,
		workspaceID: workspaceID,
		workDir:     canonicalWorkDir,
		allowedRepo: allowedRepo,
	}
	digest := sha256.Sum256([]byte(token))

	d.localTaskCapabilitiesMu.Lock()
	if d.localTaskCapabilities == nil {
		d.localTaskCapabilities = make(map[[sha256.Size]byte]*localTaskCapability)
	}
	if _, exists := d.localTaskCapabilities[digest]; exists {
		d.localTaskCapabilitiesMu.Unlock()
		return nil, errors.New("local task capability is already active")
	}
	d.localTaskCapabilities[digest] = capability
	d.localTaskCapabilitiesMu.Unlock()

	return func() {
		// Wait only for requests already authenticated with this exact
		// capability. Once active is false, no later request can enter the
		// mutation path even if it raced with removal from the digest map.
		capability.mu.Lock()
		capability.active = false
		capability.mu.Unlock()

		d.localTaskCapabilitiesMu.Lock()
		if current := d.localTaskCapabilities[digest]; current == capability {
			delete(d.localTaskCapabilities, digest)
		}
		d.localTaskCapabilitiesMu.Unlock()
	}, nil
}

func (d *Daemon) acquireLocalTaskCapability(r *http.Request) (*localTaskCapability, func(), bool) {
	token, ok := localBearerToken(r)
	if !ok {
		return nil, nil, false
	}
	digest := sha256.Sum256([]byte(token))
	d.localTaskCapabilitiesMu.RLock()
	capability := d.localTaskCapabilities[digest]
	if capability != nil {
		capability.mu.RLock()
	}
	d.localTaskCapabilitiesMu.RUnlock()
	if capability == nil {
		return nil, nil, false
	}
	if !capability.active {
		capability.mu.RUnlock()
		return nil, nil, false
	}
	return capability, capability.mu.RUnlock, true
}

// healthHandler returns the /health HTTP handler. Extracted from serveHealth
// so tests can exercise it without spinning up a listener.
func (d *Daemon) healthHandler(startedAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		var wsList []healthWorkspace
		for id, ws := range d.workspaces {
			wsList = append(wsList, healthWorkspace{
				ID:       id,
				Runtimes: ws.runtimeIDs,
			})
		}
		d.mu.Unlock()

		agents := make([]string, 0, len(d.agents()))
		for name := range d.agents() {
			agents = append(agents, name)
		}

		// "starting" until preflight (PAT renew + initial workspace sync +
		// runtime registration) completes; "running" once the daemon can
		// actually claim tasks. The health port is bound before preflight for
		// liveness/diagnostics, so callers must not treat a reachable endpoint
		// as ready — they gate on this status. Consumers that only know
		// "running" (older CLI/desktop) safely treat "starting" as not-ready.
		status := "starting"
		if d.ready.Load() {
			status = "running"
		}

		resp := HealthResponse{
			Status:                status,
			PID:                   os.Getpid(),
			OS:                    runtime.GOOS,
			Uptime:                time.Since(startedAt).Truncate(time.Second).String(),
			DaemonID:              d.cfg.DaemonID,
			DeviceName:            d.cfg.DeviceName,
			ServerURL:             d.cfg.ServerBaseURL,
			CLIVersion:            d.cfg.CLIVersion,
			ActiveTaskCount:       d.activeTasks.Load(),
			RunningTaskCount:      d.runningTasks.Load(),
			ResourceWaitTaskCount: d.resourceWaitTasks.Load(),
			Agents:                agents,
			SkippedAgents:         d.skippedAgentsSnapshot(),

			ReloadPendingReason: d.reloadPending(),
			Workspaces:          wsList,
		}
		if nonce, err := d.controlNonce(); err == nil {
			resp.ControlNonce = nonce
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// shutdownHandler triggers a graceful daemon shutdown by cancelling the
// top-level context. Used by `multica daemon stop` so we don't depend on
// OS-signal delivery, which is unreliable on Windows once the daemon is
// spawned with DETACHED_PROCESS (no shared console with the stop caller).
// The listener is bound to 127.0.0.1 and the endpoint additionally requires a
// daemon-control capability; localhost alone is not an authorization boundary.
func (d *Daemon) shutdownHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		provided, ok := localBearerToken(r)
		if !ok || d.client == nil {
			writeLocalUnauthorized(w)
			return
		}
		nonce, err := d.controlNonce()
		if err != nil {
			http.Error(w, "local control unavailable", http.StatusServiceUnavailable)
			return
		}
		expected, err := DeriveLocalControlToken(d.client.Token(), d.cfg.DaemonID, nonce)
		if err != nil || !hmac.Equal([]byte(provided), []byte(expected)) {
			writeLocalUnauthorized(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})
		if d.cancelFunc != nil {
			// Cancel asynchronously so the response flushes first; otherwise
			// srv.Close() races with the writer.
			go d.cancelFunc()
		}
	}
}

// serveHealth runs the health HTTP server on the given listener.
// Blocks until ctx is cancelled.
func (d *Daemon) serveHealth(ctx context.Context, ln net.Listener, startedAt time.Time) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", d.healthHandler(startedAt))
	mux.HandleFunc("/shutdown", d.shutdownHandler())
	mux.HandleFunc("/repo/checkout", d.repoCheckoutHandler())
	mux.HandleFunc("/a2a/task-control", d.a2aTaskControlHandler())

	srv := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	d.logger.Info("health server listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		d.logger.Warn("health server error", "error", err)
	}
}

func (d *Daemon) repoCheckoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		capability, releaseCapability, ok := d.acquireLocalTaskCapability(r)
		if !ok {
			writeLocalUnauthorized(w)
			return
		}
		defer releaseCapability()

		var req repoCheckoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.URL = strings.TrimSpace(req.URL)
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		if req.WorkDir == "" {
			http.Error(w, "workdir is required", http.StatusBadRequest)
			return
		}
		if req.TaskID == "" {
			http.Error(w, "task_id is required", http.StatusBadRequest)
			return
		}

		workDir, err := canonicalLocalWorkDir(req.WorkDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.TaskID) != capability.taskID ||
			strings.TrimSpace(req.WorkspaceID) != capability.workspaceID ||
			workDir != capability.workDir {
			http.Error(w, "capability does not match task context", http.StatusForbidden)
			return
		}
		if _, allowed := capability.allowedRepo[req.URL]; !allowed {
			http.Error(w, "repository is not available to this task", http.StatusForbidden)
			return
		}
		if req.CheckoutMode != "" && req.CheckoutMode != repoCheckoutModeIsolated {
			http.Error(w, "invalid checkout_mode", http.StatusBadRequest)
			return
		}

		if d.repoCache == nil {
			http.Error(w, "repo cache not initialized", http.StatusInternalServerError)
			return
		}

		if err := d.ensureRepoReady(r.Context(), req.WorkspaceID, req.URL); err != nil {
			statusCode := http.StatusInternalServerError
			if errors.Is(err, ErrRepoNotConfigured) {
				statusCode = http.StatusBadRequest
			}
			d.logger.Error("repo checkout readiness failed", "workspace_id", req.WorkspaceID, "url", req.URL, "error", err)
			http.Error(w, err.Error(), statusCode)
			return
		}

		checkoutRef := strings.TrimSpace(req.Ref)
		if checkoutRef == "" {
			checkoutRef = d.taskRepoDefaultRef(req.WorkspaceID, req.TaskID, req.URL)
		}

		result, err := d.repoCache.CreateWorktree(repocache.WorktreeParams{
			WorkspaceID:         req.WorkspaceID,
			RepoURL:             req.URL,
			WorkDir:             req.WorkDir,
			Ref:                 checkoutRef,
			AgentName:           req.AgentName,
			TaskID:              req.TaskID,
			CoAuthoredByEnabled: d.workspaceCoAuthoredByEnabled(req.WorkspaceID),
			IsolatedGitMetadata: req.CheckoutMode == repoCheckoutModeIsolated,
		})
		if err != nil {
			d.logger.Error("repo checkout failed", "url", req.URL, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}
