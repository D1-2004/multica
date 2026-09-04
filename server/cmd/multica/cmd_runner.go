package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
	"github.com/spf13/cobra"
)

var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Bind and run this machine as an Agent Runner MCP",
}

var runnerBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Authorize this machine and bind it to an Agent",
	Args:  cobra.NoArgs,
	RunE:  runRunnerBind,
}

var runnerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the local Runner",
	Args:  cobra.NoArgs,
	RunE:  runRunnerStart,
}

var runnerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the local Runner",
	Args:  cobra.NoArgs,
	RunE:  runRunnerStop,
}

var runnerReconnectCmd = &cobra.Command{
	Use:   "reconnect",
	Short: "Reconnect one Agent binding for this local Runner",
	Args:  cobra.NoArgs,
	RunE:  runRunnerReconnect,
}

var runnerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local Runner status",
	Args:  cobra.NoArgs,
	RunE:  runRunnerStatus,
}

func init() {
	runnerBindCmd.Flags().String("pairing-token", "", "Short-lived pairing token copied from General settings")
	runnerBindCmd.Flags().StringSlice("root", nil, "Absolute file root to expose; defaults to this user's Desktop on first bind")
	runnerReconnectCmd.Flags().String("reconnect-token", "", "Short-lived reconnect token copied from General settings")
	runnerStartCmd.Flags().Bool("foreground", false, "Run in the current terminal")
	runnerCmd.AddCommand(runnerBindCmd, runnerReconnectCmd, runnerStartCmd, runnerStopCmd, runnerStatusCmd)
}

type runnerDeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func runRunnerBind(cmd *cobra.Command, _ []string) error {
	serverURL, err := cmd.Flags().GetString("server-url")
	if err != nil {
		return err
	}
	serverURL, err = normalizeRunnerServerURL(serverURL)
	if err != nil {
		return err
	}
	pairingToken, _ := cmd.Flags().GetString("pairing-token")
	if strings.TrimSpace(pairingToken) == "" {
		return errors.New("--pairing-token is required")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return errors.New("Multica Runner currently supports macOS and Linux")
	}

	cfg, err := loadRunnerConfig()
	if err != nil {
		return err
	}
	if err := ensureRunnerKey(&cfg); err != nil {
		return err
	}
	roots, _ := cmd.Flags().GetStringSlice("root")
	if len(roots) == 0 {
		if len(cfg.Roots) > 0 {
			roots = cfg.Roots
		} else {
			desktop, desktopErr := defaultRunnerDesktop()
			if desktopErr != nil {
				return desktopErr
			}
			roots = []string{desktop}
		}
	}
	for i, root := range roots {
		absolute, absoluteErr := filepath.Abs(root)
		if absoluteErr != nil {
			return fmt.Errorf("resolve Runner root %q: %w", root, absoluteErr)
		}
		info, statErr := os.Stat(absolute)
		if statErr != nil || !info.IsDir() {
			return fmt.Errorf("Runner root must be an existing directory: %s", absolute)
		}
		roots[i] = filepath.Clean(absolute)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read machine name: %w", err)
	}
	client := cli.NewAPIClient(serverURL, "", "")
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	var authorization runnerDeviceAuthorization
	err = client.PostJSON(ctx, "/api/runner/device-authorizations", map[string]any{
		"pairing_token":  pairingToken,
		"public_key":     cfg.PublicKey,
		"machine_name":   hostname,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"client_version": version,
		"roots":          roots,
	}, &authorization)
	if err != nil {
		return fmt.Errorf("begin Runner authorization: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Opening browser to authorize %s for this Agent...\n", hostname)
	if err := openBrowser(authorization.VerificationURIComplete); err != nil {
		fmt.Fprintln(os.Stderr, "Could not open the browser automatically.")
	}
	fmt.Fprintf(os.Stderr, "If the browser did not open, visit:\n  %s\n", authorization.VerificationURIComplete)

	pollEvery := time.Duration(authorization.Interval) * time.Second
	if pollEvery < time.Second {
		pollEvery = 2 * time.Second
	}
	deadline := time.Now().Add(time.Duration(authorization.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(pollEvery):
		}
		pollCtx, pollCancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		var result struct {
			Status    string `json:"status"`
			MachineID string `json:"machine_id"`
		}
		err := client.PostJSON(pollCtx, "/api/runner/device-authorizations/token", map[string]string{"device_code": authorization.DeviceCode}, &result)
		pollCancel()
		if err != nil {
			return fmt.Errorf("poll Runner authorization: %w", err)
		}
		switch result.Status {
		case "pending":
			continue
		case "approved":
			if err := cfg.upsertBinding(serverURL, result.MachineID); err != nil {
				return err
			}
			cfg.Roots = roots
			if err := saveRunnerConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Runner authorized. Starting the background service...")
			if err := restartRunnerBackground(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Runner started and is connecting. File roots: %s\n", strings.Join(roots, ", "))
			fmt.Fprintln(os.Stderr, "Shell commands run with your full operating-system user permissions.")
			return nil
		case "denied":
			return errors.New("Runner authorization was denied")
		case "expired":
			return errors.New("Runner authorization expired; create a new command in Agent settings")
		default:
			return fmt.Errorf("unexpected Runner authorization status %q", result.Status)
		}
	}
	return errors.New("Runner authorization expired; create a new command in Agent settings")
}

func defaultRunnerDesktop() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for default Runner root: %w", err)
	}
	desktop := filepath.Join(home, "Desktop")
	info, err := os.Stat(desktop)
	if err != nil {
		return "", fmt.Errorf("default Runner root is not available: %s", desktop)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("default Runner root is not a directory: %s", desktop)
	}
	return filepath.Clean(desktop), nil
}

func runRunnerReconnect(cmd *cobra.Command, _ []string) error {
	reconnectToken, _ := cmd.Flags().GetString("reconnect-token")
	if strings.TrimSpace(reconnectToken) == "" {
		return errors.New("--reconnect-token is required")
	}
	cfg, err := loadRunnerConfig()
	if err != nil {
		return err
	}
	serverURL, _ := cmd.Flags().GetString("server-url")
	serverURL, err = normalizeRunnerServerURL(serverURL)
	if err != nil {
		return err
	}
	binding, found, err := cfg.bindingForURL(serverURL)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("this Runner is not registered with %s", serverURL)
	}
	privateKey, err := runnerPrivateKey(cfg)
	if err != nil {
		return err
	}
	client := cli.NewAPIClient(binding.ServerURL, "", "")
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	challenge, err := createRunnerChallenge(ctx, client, binding.MachineID)
	if err != nil {
		return fmt.Errorf("create reconnect challenge: %w", err)
	}
	headers := runnerChallengeHeaders(challenge, privateKey)
	var result struct {
		Status             string `json:"status"`
		ActiveBindingCount int64  `json:"active_binding_count"`
		MachineOnline      bool   `json:"machine_online"`
	}
	path := "/api/runner/machines/" + url.PathEscape(binding.MachineID) + "/reconnect"
	if err := client.PostJSONWithHeaders(ctx, path, map[string]string{
		"reconnect_token": reconnectToken,
	}, &result, headers); err != nil {
		return fmt.Errorf("reconnect Runner binding: %w", err)
	}
	if result.Status != "connected" || result.ActiveBindingCount < 1 {
		return errors.New("Runner binding did not reconnect")
	}
	if result.MachineOnline {
		fmt.Fprintln(os.Stderr, "Runner binding reconnected. The shared background service is already online.")
		return keepOnlineOrStartRunnerBackground()
	}
	fmt.Fprintln(os.Stderr, "Runner binding reconnected. The shared background service is offline; restarting it...")
	return restartRunnerBackground()
}

func runRunnerStart(cmd *cobra.Command, _ []string) error {
	foreground, _ := cmd.Flags().GetBool("foreground")
	if !foreground {
		return startRunnerBackground()
	}
	cfg, err := loadRunnerConfig()
	if err != nil {
		return err
	}
	bindings, err := cfg.bindings()
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return errors.New("Runner is not paired; copy a new install command from General settings")
	}
	privateKey, err := runnerPrivateKey(cfg)
	if err != nil {
		return err
	}
	pidPath, err := runnerPIDPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		return err
	}
	releaseInstanceLock, err := acquireRunnerInstanceLock(filepath.Dir(pidPath))
	if err != nil {
		return err
	}
	defer releaseInstanceLock()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return fmt.Errorf("write Runner pid: %w", err)
	}
	defer os.Remove(pidPath)
	runnerCtx, stopSignals := runnerSignalContext(cmd.Context())
	defer stopSignals()
	defer stopAllRunnerBackgroundProcesses()
	err = runAllRunnerLoops(runnerCtx, cfg, privateKey)
	if errors.Is(err, errRunnerHasNoBindings) || (errors.Is(err, context.Canceled) && runnerCtx.Err() != nil) {
		return nil
	}
	return err
}

func startRunnerBackground() error {
	return ensureRunnerBackground(false)
}

func keepOnlineOrStartRunnerBackground() error {
	return ensureRunnerBackground(true)
}

func ensureRunnerBackground(preserveRunning bool) error {
	cfg, err := loadRunnerConfig()
	if err != nil {
		return err
	}
	bindings, err := cfg.bindings()
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return errors.New("Runner is not paired; copy a new install command from General settings")
	}
	if pid, running := currentRunnerPID(); running {
		if preserveRunning || runnerAllBindingsConnected(pid, cfg) {
			fmt.Fprintf(os.Stderr, "Runner is already running and connected (pid %d).\n", pid)
			return nil
		}
		fmt.Fprintf(os.Stderr, "Runner process %d is running but offline; restarting it...\n", pid)
		if err := stopAndWaitRunner(pid); err != nil {
			return err
		}
	}
	return launchRunnerBackground(cfg)
}

func restartRunnerBackground() error {
	if pid, running := currentRunnerPID(); running {
		fmt.Fprintf(os.Stderr, "Restarting Runner process %d to apply current bindings...\n", pid)
		if err := stopAndWaitRunner(pid); err != nil {
			return err
		}
	}
	cfg, err := loadRunnerConfig()
	if err != nil {
		return err
	}
	bindings, err := cfg.bindings()
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return errors.New("Runner is not paired; copy a new install command from General settings")
	}
	return launchRunnerBackground(cfg)
}

func stopAndWaitRunner(pid int) error {
	if err := stopRunnerPID(pid); err != nil {
		return fmt.Errorf("stop offline Runner: %w", err)
	}
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, running := currentRunnerPID(); !running {
			return nil
		}
	}
	return errors.New("offline Runner did not stop within 5 seconds")
}

func launchRunnerBackground(cfg runnerConfig) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Runner executable: %w", err)
	}
	logPath, err := runnerLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open Runner log: %w", err)
	}
	defer logFile.Close()
	process := exec.Command(executable, "runner", "start", "--foreground")
	process.Stdout = logFile
	process.Stderr = logFile
	process.Stdin = nil
	if err := configureRunnerDetached(process); err != nil {
		return err
	}
	if err := process.Start(); err != nil {
		return fmt.Errorf("start Runner: %w", err)
	}
	if err := process.Process.Release(); err != nil {
		return fmt.Errorf("release Runner process: %w", err)
	}
	// A freshly downloaded macOS binary can spend several seconds in the
	// operating system's first-launch verification before it reaches Cobra and
	// writes runner.pid. Keep the installer attached long enough to observe the
	// real child instead of reporting a false startup failure while that child
	// is already on its way to connecting.
	startedPID := 0
	for i := 0; i < 300; i++ {
		time.Sleep(100 * time.Millisecond)
		if pid, running := currentRunnerPID(); running {
			startedPID = pid
			if runnerAllBindingsConnected(pid, cfg) {
				fmt.Fprintf(os.Stderr, "Runner started and connected (pid %d). Log: %s\n", pid, logPath)
				return nil
			}
		} else if startedPID != 0 {
			return fmt.Errorf("Runner process %d exited before connecting; inspect %s", startedPID, logPath)
		}
	}
	if startedPID != 0 {
		return fmt.Errorf("Runner process %d did not establish a WebSocket within 30 seconds; it is still retrying. Inspect %s", startedPID, logPath)
	}
	return fmt.Errorf("Runner did not start; inspect %s", logPath)
}

func currentRunnerPID() (int, bool) {
	path, err := runnerPIDPath()
	if err != nil {
		return 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !runnerProcessRunning(pid) {
		return 0, false
	}
	return pid, true
}

func runRunnerStop(_ *cobra.Command, _ []string) error {
	pid, running := currentRunnerPID()
	if !running {
		fmt.Fprintln(os.Stderr, "Runner is not running.")
		return nil
	}
	if err := stopRunnerPID(pid); err != nil {
		return fmt.Errorf("stop Runner: %w", err)
	}
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, running := currentRunnerPID(); !running {
			fmt.Fprintln(os.Stderr, "Runner stopped.")
			return nil
		}
	}
	return errors.New("Runner did not stop within 5 seconds")
}

func runRunnerStatus(_ *cobra.Command, _ []string) error {
	cfg, err := loadRunnerConfig()
	if err != nil {
		return err
	}
	bindings, err := cfg.bindings()
	if err != nil {
		return err
	}
	pid, running := currentRunnerPID()
	status := "stopped"
	if running {
		status = "running (offline)"
		if runnerAllBindingsConnected(pid, cfg) {
			status = "online"
		} else {
			for _, binding := range bindings {
				if runnerConnectionActive(pid, binding.MachineID) {
					status = "running (partial)"
					break
				}
			}
		}
	}
	fmt.Printf("Status: %s\n", status)
	if running {
		fmt.Printf("PID: %d\n", pid)
	}
	if len(cfg.Roots) > 0 {
		fmt.Printf("Roots: %s\n", strings.Join(cfg.Roots, ", "))
	}
	for _, binding := range bindings {
		connection := "offline"
		if running && runnerConnectionActive(pid, binding.MachineID) {
			connection = "online"
		}
		fmt.Printf("Server: %s\nMachine: %s\nConnection: %s\n", binding.ServerURL, binding.MachineID, connection)
	}
	return nil
}

type runnerChallenge struct {
	ChallengeID        string `json:"challenge_id"`
	Challenge          string `json:"challenge"`
	ActiveBindingCount int64  `json:"active_binding_count"`
}

var errRunnerHasNoBindings = errors.New("Runner has no connected Agent bindings")

func createRunnerChallenge(ctx context.Context, client *cli.APIClient, machineID string) (runnerChallenge, error) {
	var challenge runnerChallenge
	err := client.PostJSON(ctx, "/api/runner/machines/"+url.PathEscape(machineID)+"/challenges", map[string]any{}, &challenge)
	return challenge, err
}

func runnerChallengeHeaders(challenge runnerChallenge, privateKey ed25519.PrivateKey) map[string]string {
	return map[string]string{
		"X-Runner-Challenge-ID": challenge.ChallengeID,
		"X-Runner-Challenge":    challenge.Challenge,
		"X-Runner-Signature":    base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(challenge.Challenge))),
	}
}

func normalizeRunnerServerURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.EscapedPath() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Runner server URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	return value, nil
}

func runAllRunnerLoops(ctx context.Context, cfg runnerConfig, privateKey ed25519.PrivateKey) error {
	bindings, err := cfg.bindings()
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return errors.New("Runner is not bound; copy a new install command from Agent settings")
	}
	mcpConfig, err := loadRunnerMCPConfig()
	if err != nil {
		slog.Warn("Runner MCP config is unavailable", "error", err)
		mcpConfig, _ = parseRunnerMCPConfigWithBuiltins([]byte(`{"mcpServers":{}}`))
	}
	mcpManager := newRunnerMCPManager(mcpConfig, cfg.Roots...)
	defer mcpManager.Close()
	errCh := make(chan error, len(bindings))
	var wg sync.WaitGroup
	for _, binding := range bindings {
		wg.Add(1)
		go func(binding runnerServerBinding) {
			defer wg.Done()
			errCh <- runRunnerLoop(ctx, binding, privateKey, mcpConfig.Inventory, mcpManager)
		}(binding)
	}
	go func() {
		wg.Wait()
		close(errCh)
	}()
	var first error
	for err := range errCh {
		if err == nil || errors.Is(err, context.Canceled) {
			continue
		}
		if first == nil {
			first = err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return first
}

func runRunnerLoop(ctx context.Context, binding runnerServerBinding, privateKey ed25519.PrivateKey, inventory runnerprotocol.MCPInventory, mcpManager *runnerMCPManager) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With(
		"server_url", binding.ServerURL,
		"machine_id", binding.MachineID,
	)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := runRunnerConnectionWithMCP(ctx, binding, privateKey, inventory, mcpManager, logger)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger.Warn("Runner connection closed", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func runRunnerConnection(ctx context.Context, binding runnerServerBinding, privateKey ed25519.PrivateKey, inventory runnerprotocol.MCPInventory, logger *slog.Logger) error {
	return runRunnerConnectionWithMCP(ctx, binding, privateKey, inventory, nil, logger)
}

func runRunnerConnectionWithMCP(ctx context.Context, binding runnerServerBinding, privateKey ed25519.PrivateKey, inventory runnerprotocol.MCPInventory, mcpManager *runnerMCPManager, logger *slog.Logger) error {
	client := cli.NewAPIClient(binding.ServerURL, "", "")
	challengeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	challenge, err := createRunnerChallenge(challengeCtx, client, binding.MachineID)
	if err != nil {
		return fmt.Errorf("create connection challenge: %w", err)
	}
	wsURL, err := url.Parse(binding.ServerURL)
	if err != nil {
		return err
	}
	switch wsURL.Scheme {
	case "http":
		wsURL.Scheme = "ws"
	case "https":
		wsURL.Scheme = "wss"
	default:
		return errors.New("Runner server URL must use http or https")
	}
	wsURL.Path = "/api/runner/ws"
	query := wsURL.Query()
	query.Set("machine_id", binding.MachineID)
	wsURL.RawQuery = query.Encode()
	headers := http.Header{}
	for key, value := range runnerChallengeHeaders(challenge, privateKey) {
		headers.Set(key, value)
	}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, wsURL.String(), headers)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect Runner WebSocket: %w", err)
	}
	defer conn.Close()
	defer clearRunnerConnectionMachine(os.Getpid(), binding.MachineID)
	conn.SetReadLimit(2 << 20)
	logger.Info("Runner connected", "machine_id", binding.MachineID)
	if err := writeRunnerConnectionState(binding.MachineID); err != nil {
		return err
	}

	connectionCtx, connectionCancel := context.WithCancel(ctx)
	defer connectionCancel()
	go func() {
		<-connectionCtx.Done()
		_ = conn.Close()
	}()
	var writeMu sync.Mutex
	writeJSON := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(value)
	}
	if err := writeJSON(runnerprotocol.Heartbeat{Type: runnerprotocol.MessageHeartbeat, ClientVersion: version}); err != nil {
		return err
	}
	if err := writeJSON(inventory); err != nil {
		return err
	}
	heartbeatsDone := make(chan struct{})
	go func() {
		defer close(heartbeatsDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-connectionCtx.Done():
				return
			case <-ticker.C:
				if writeJSON(runnerprotocol.Heartbeat{Type: runnerprotocol.MessageHeartbeat, ClientVersion: version}) != nil {
					connectionCancel()
					return
				}
			}
		}
	}()

	sem := make(chan struct{}, 4)
	var callsMu sync.Mutex
	runningCalls := make(map[string]context.CancelFunc)
	cancelledCalls := make(map[string]struct{})
	defer func() {
		callsMu.Lock()
		defer callsMu.Unlock()
		for _, cancelCall := range runningCalls {
			cancelCall()
		}
	}()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			connectionCancel()
			<-heartbeatsDone
			return err
		}
		var envelope runnerprotocol.Envelope
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		if envelope.Type == runnerprotocol.MessageBindingsChanged {
			var bindings runnerprotocol.BindingsChanged
			if json.Unmarshal(raw, &bindings) != nil {
				continue
			}
			if !runnerConnectionActive(os.Getpid(), binding.MachineID) {
				if err := writeRunnerConnectionState(binding.MachineID); err != nil {
					connectionCancel()
					<-heartbeatsDone
					return err
				}
			}
			continue
		}
		if envelope.Type == runnerprotocol.MessageCallsCancelled {
			var cancelled runnerprotocol.CallsCancelled
			if json.Unmarshal(raw, &cancelled) != nil {
				continue
			}
			callsMu.Lock()
			for _, callID := range cancelled.CallIDs {
				cancelledCalls[callID] = struct{}{}
				if cancelCall, ok := runningCalls[callID]; ok {
					cancelCall()
				}
			}
			callsMu.Unlock()
			continue
		}
		if envelope.Type != runnerprotocol.MessageCall {
			continue
		}
		var call runnerprotocol.Call
		if json.Unmarshal(raw, &call) != nil || call.CallID == "" {
			continue
		}
		callCtx, cancelCall := context.WithCancel(connectionCtx)
		callsMu.Lock()
		runningCalls[call.CallID] = cancelCall
		if _, cancelled := cancelledCalls[call.CallID]; cancelled {
			delete(cancelledCalls, call.CallID)
			cancelCall()
		}
		callsMu.Unlock()
		go func(call runnerprotocol.Call) {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-callCtx.Done():
			}
			result := executeRunnerCallWithMCP(callCtx, call, mcpManager)
			callsMu.Lock()
			delete(runningCalls, call.CallID)
			delete(cancelledCalls, call.CallID)
			callsMu.Unlock()
			cancelCall()
			if err := writeJSON(result); err != nil {
				connectionCancel()
			}
		}(call)
	}
}
