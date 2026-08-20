package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

type EnterpriseIdentitySourceAvailability struct {
	SandboxID string
	RuntimeID pgtype.UUID
}

type EnterpriseIdentitySource interface {
	Create(
		context.Context,
		enterpriseIdentitySourceKey,
		BUCIdentityTokens,
	) (EnterpriseIdentitySourceAvailability, error)
	Rotate(context.Context, pgtype.UUID, string, string, string, BUCIdentityTokens) (EnterpriseIdentitySourceAvailability, error)
	Prepare(context.Context, pgtype.UUID, string, string, string) error
	Park(context.Context, pgtype.UUID, string) error
	Delete(context.Context, pgtype.UUID, string) error
}

type asbIdentitySourceRuntimeStore interface {
	GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error)
}

// ASBIdentitySourceManager proves BUC attach on a short-lived ASB sandbox built
// from the owning Runtime's current image. After the credentials have been
// proven, the sandbox is terminated. Task sandboxes do not inherit that seed;
// they attach a persisted, refreshed BUC token trio. Seed rotation uses the
// same token attach path.
type ASBIdentitySourceManager struct {
	Store          asbIdentitySourceRuntimeStore
	Credentials    *ASBRuntimeClientProvider
	Capacity       ASBSandboxCapacity
	Config         ASBConfig
	ConfigProvider func() ASBConfig
}

func (m *ASBIdentitySourceManager) currentConfig() ASBConfig {
	if m != nil && m.ConfigProvider != nil {
		return m.ConfigProvider()
	}
	if m == nil {
		return ASBConfig{}
	}
	return m.Config
}

func (m *ASBIdentitySourceManager) validateRuntime(
	ctx context.Context,
	runtimeID pgtype.UUID,
) error {
	if m == nil || m.Store == nil {
		return errors.New("ASB enterprise identity source manager is unavailable")
	}
	runtime, err := m.Store.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return fmt.Errorf("load ASB Runtime for enterprise identity source: %w", err)
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil || metadata.SandboxBackend != SandboxBackendASB {
		return errors.New("enterprise identity source requires an ASB Runtime")
	}
	return nil
}

func (m *ASBIdentitySourceManager) Create(
	ctx context.Context,
	key enterpriseIdentitySourceKey,
	tokens BUCIdentityTokens,
) (EnterpriseIdentitySourceAvailability, error) {
	config := m.currentConfig()
	if m == nil || m.Store == nil || m.Credentials == nil || m.Capacity == nil {
		return EnterpriseIdentitySourceAvailability{}, errors.New(
			"ASB enterprise identity source manager is unavailable",
		)
	}
	if err := key.validate(); err != nil {
		return EnterpriseIdentitySourceAvailability{}, err
	}
	runtimeID := key.RuntimeID
	runtime, err := m.Store.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"load ASB Runtime for enterprise identity source: %w",
			err,
		)
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil || metadata.SandboxBackend != SandboxBackendASB {
		return EnterpriseIdentitySourceAvailability{}, errors.New(
			"enterprise identity requires an ASB Runtime",
		)
	}
	client, err := m.Credentials.ClientForRuntime(ctx, runtimeID)
	if err != nil {
		return EnterpriseIdentitySourceAvailability{}, err
	}
	sandbox, err := m.Capacity.Create(ctx, runtimeID, client, ASBCreateSandboxInput{
		ImageURI:       metadata.ArtifactRef,
		TimeoutSeconds: asbMaxCreateTimeout,
		ResourceCPU:    config.ResourceCPU,
		ResourceMemory: config.ResourceMemory,
		Entrypoint:     []string{"sleep infinity"},
		Metadata: map[string]string{
			"multica.identity_source": "true",
			"multica.workspace_id":    util.UUIDToString(key.WorkspaceID),
			"multica.subject_key":     enterpriseIdentitySourceFingerprint(key),
			"multica.runtime_id":      util.UUIDToString(runtimeID),
		},
		Extensions: map[string]string{
			"wireguard.lazyAuth": "true",
		},
	})
	if err != nil {
		logASBIdentitySourceFailure("create_sandbox", "", err)
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"create temporary ASB enterprise identity source: %w",
			err,
		)
	}
	seedReady := false
	defer func() {
		if seedReady {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := deleteASBIdentitySourceIfExists(
			cleanupCtx,
			client,
			sandbox.ID,
			m.sourceLifecycleTimeout(),
		); cleanupErr != nil {
			logASBIdentitySourceFailure("cleanup_sandbox", sandbox.ID, cleanupErr)
		}
	}()
	if err := waitForASBSandboxRunning(
		ctx,
		client,
		sandbox.ID,
		config.ReadyTimeout,
	); err != nil {
		logASBIdentitySourceFailure("wait_running", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, err
	}
	if err := attachAndProbeASBIdentitySource(
		ctx,
		client,
		sandbox.ID,
		key.RawEmployeeID,
		key.BUCAgentID,
		tokens,
		config.WireGuardCredentials,
		config.WireGuardReadyTimeout,
	); err != nil {
		logASBIdentitySourceFailure("establish_buc_identity", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, err
	}
	if err := terminateASBIdentitySource(
		ctx,
		client,
		sandbox.ID,
		m.sourceLifecycleTimeout(),
	); err != nil {
		logASBIdentitySourceFailure("terminate_seed", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"terminate temporary ASB enterprise identity seed: %w",
			err,
		)
	}
	seedReady = true
	slog.Info(
		"terminated ASB enterprise identity seed established",
		"runtime_id", util.UUIDToString(runtimeID),
		"subject_key", enterpriseIdentitySourceFingerprint(key),
		"sandbox_id", sandbox.ID,
		"running_quota_retained", false,
	)
	return EnterpriseIdentitySourceAvailability{
		SandboxID: sandbox.ID,
		RuntimeID: runtimeID,
	}, nil
}

func (m *ASBIdentitySourceManager) Rotate(
	ctx context.Context,
	runtimeID pgtype.UUID,
	predecessorSandboxID string,
	employeeID string,
	bucAgentID string,
	tokens BUCIdentityTokens,
) (EnterpriseIdentitySourceAvailability, error) {
	config := m.currentConfig()
	if m == nil || m.Store == nil || m.Credentials == nil || m.Capacity == nil {
		return EnterpriseIdentitySourceAvailability{}, errors.New(
			"ASB enterprise identity source manager is unavailable",
		)
	}
	predecessorSandboxID = strings.TrimSpace(predecessorSandboxID)
	if !runtimeID.Valid || predecessorSandboxID == "" ||
		strings.TrimSpace(employeeID) == "" || strings.TrimSpace(bucAgentID) == "" ||
		strings.TrimSpace(tokens.AccessToken) == "" ||
		strings.TrimSpace(tokens.RefreshToken) == "" ||
		strings.TrimSpace(tokens.IDToken) == "" {
		return EnterpriseIdentitySourceAvailability{}, errors.New(
			"ASB enterprise identity seed rotation input is incomplete",
		)
	}
	runtime, err := m.Store.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"load ASB Runtime for enterprise identity seed rotation: %w",
			err,
		)
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil || metadata.SandboxBackend != SandboxBackendASB {
		return EnterpriseIdentitySourceAvailability{}, errors.New(
			"enterprise identity seed rotation requires an ASB Runtime",
		)
	}
	client, err := m.Credentials.ClientForRuntime(ctx, runtimeID)
	if err != nil {
		return EnterpriseIdentitySourceAvailability{}, err
	}
	predecessorPresent := true
	if err := validateASBIdentitySeed(ctx, client, predecessorSandboxID); err != nil {
		if !errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
			logASBIdentitySourceFailure("validate_predecessor_seed", predecessorSandboxID, err)
			return EnterpriseIdentitySourceAvailability{}, err
		}
		predecessorPresent = false
	}
	sandbox, err := m.Capacity.Create(ctx, runtimeID, client, ASBCreateSandboxInput{
		ImageURI:       metadata.ArtifactRef,
		TimeoutSeconds: asbMaxCreateTimeout,
		ResourceCPU:    config.ResourceCPU,
		ResourceMemory: config.ResourceMemory,
		Entrypoint:     []string{"sleep infinity"},
		Metadata: map[string]string{
			"multica.identity_source":             "true",
			"multica.identity_source_rotation":    "true",
			"multica.identity_source_predecessor": predecessorSandboxID,
			"multica.runtime_id":                  util.UUIDToString(runtimeID),
		},
		Extensions: map[string]string{
			"wireguard.lazyAuth": "true",
		},
	})
	if err != nil {
		logASBIdentitySourceFailure("create_rotated_seed", predecessorSandboxID, err)
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"create rotated ASB enterprise identity seed: %w",
			err,
		)
	}
	seedReady := false
	defer func() {
		if seedReady {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if cleanupErr := deleteASBIdentitySourceIfExists(
			cleanupCtx,
			client,
			sandbox.ID,
			m.sourceLifecycleTimeout(),
		); cleanupErr != nil {
			logASBIdentitySourceFailure("cleanup_rotated_seed", sandbox.ID, cleanupErr)
		}
	}()
	if err := waitForASBSandboxRunning(ctx, client, sandbox.ID, config.ReadyTimeout); err != nil {
		logASBIdentitySourceFailure("wait_rotated_seed_running", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, err
	}
	if err := attachAndProbeASBIdentitySource(
		ctx,
		client,
		sandbox.ID,
		employeeID,
		bucAgentID,
		tokens,
		config.WireGuardCredentials,
		config.WireGuardReadyTimeout,
	); err != nil {
		logASBIdentitySourceFailure("attach_and_probe_rotated_seed", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"prove rotated ASB enterprise identity seed: %w",
			err,
		)
	}
	if err := terminateASBIdentitySource(
		ctx,
		client,
		sandbox.ID,
		m.sourceLifecycleTimeout(),
	); err != nil {
		logASBIdentitySourceFailure("terminate_rotated_seed", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"terminate rotated ASB enterprise identity seed: %w",
			err,
		)
	}
	if predecessorPresent {
		if err := terminateASBIdentitySource(
			ctx,
			client,
			predecessorSandboxID,
			m.sourceLifecycleTimeout(),
		); err != nil {
			logASBIdentitySourceFailure("terminate_predecessor_seed", predecessorSandboxID, err)
			return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
				"terminate predecessor ASB enterprise identity seed: %w",
				err,
			)
		}
	}
	seedReady = true
	slog.Info(
		"ASB enterprise identity seed rotated",
		"runtime_id", util.UUIDToString(runtimeID),
		"predecessor_sandbox_id", predecessorSandboxID,
		"sandbox_id", sandbox.ID,
		"running_quota_retained", false,
	)
	return EnterpriseIdentitySourceAvailability{
		SandboxID: sandbox.ID,
		RuntimeID: runtimeID,
	}, nil
}

func (m *ASBIdentitySourceManager) Prepare(
	ctx context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
	employeeID string,
	bucAgentID string,
) error {
	if err := m.validateRuntime(ctx, runtimeID); err != nil {
		return err
	}
	client, err := m.clientForRuntime(ctx, runtimeID)
	if err != nil {
		return err
	}
	if err := prepareASBIdentitySource(
		ctx,
		client,
		sandboxID,
		employeeID,
		bucAgentID,
		m.sourceLifecycleTimeout(),
	); err != nil {
		logASBIdentitySourceFailure("prepare_source", sandboxID, err)
		return err
	}
	slog.Info(
		"ASB enterprise identity seed prepared",
		"runtime_id", util.UUIDToString(runtimeID),
		"sandbox_id", sandboxID,
	)
	return nil
}

func (m *ASBIdentitySourceManager) Park(
	ctx context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
) error {
	client, err := m.clientForRuntime(ctx, runtimeID)
	if err != nil {
		return err
	}
	if err := terminateASBIdentitySource(
		ctx,
		client,
		sandboxID,
		m.sourceLifecycleTimeout(),
	); err != nil {
		logASBIdentitySourceFailure("terminate_source_after_lease", sandboxID, err)
		return err
	}
	slog.Info(
		"ASB enterprise identity seed lease released",
		"runtime_id", util.UUIDToString(runtimeID),
		"sandbox_id", sandboxID,
		"sandbox_lifecycle_action", "terminate",
		"running_quota_retained", false,
	)
	return nil
}

func (m *ASBIdentitySourceManager) Delete(
	ctx context.Context,
	runtimeID pgtype.UUID,
	sandboxID string,
) error {
	client, err := m.clientForRuntime(ctx, runtimeID)
	if err != nil {
		return err
	}
	if err := deleteASBIdentitySourceIfExists(
		ctx,
		client,
		sandboxID,
		m.sourceLifecycleTimeout(),
	); err != nil {
		return err
	}
	slog.Info(
		"deleted ASB enterprise identity source",
		"runtime_id", util.UUIDToString(runtimeID),
		"sandbox_id", sandboxID,
	)
	return nil
}

func (m *ASBIdentitySourceManager) clientForRuntime(
	ctx context.Context,
	runtimeID pgtype.UUID,
) (*ASBClient, error) {
	if m == nil || m.Credentials == nil {
		return nil, errors.New("ASB enterprise identity source manager is unavailable")
	}
	return m.Credentials.ClientForRuntime(ctx, runtimeID)
}

func (m *ASBIdentitySourceManager) sourceLifecycleTimeout() time.Duration {
	config := m.currentConfig()
	timeout := config.ReadyTimeout
	if config.WireGuardReadyTimeout > timeout {
		timeout = config.WireGuardReadyTimeout
	}
	return timeout
}

func attachAndProbeASBIdentitySource(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
	bucAgentID string,
	tokens BUCIdentityTokens,
	wireGuardCredentials string,
	timeout time.Duration,
) error {
	return attachAndProbeASBIdentitySourceGrant(
		ctx,
		client,
		sandboxID,
		employeeID,
		bucAgentID,
		ASBBUCIdentityGrant{
			EmployeeID:           employeeID,
			BUCAccessToken:       tokens.AccessToken,
			BUCRefreshToken:      tokens.RefreshToken,
			BUCIDToken:           tokens.IDToken,
			WireGuardCredentials: wireGuardCredentials,
		},
		timeout,
	)
}

func attachAndProbeASBIdentitySourceGrant(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
	bucAgentID string,
	grant ASBBUCIdentityGrant,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("ASB WireGuard ready timeout is not configured")
	}
	identityCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := attachASBBUCIdentitySource(
		identityCtx,
		client,
		sandboxID,
		grant,
	); err != nil {
		return err
	}
	if err := waitForASBSandboxState(
		identityCtx,
		client,
		sandboxID,
		timeout,
		"running",
	); err != nil {
		return fmt.Errorf("wait for attached ASB identity source to return running: %w", err)
	}

	return waitForASBIdentitySourceBUC(
		identityCtx,
		client,
		sandboxID,
		employeeID,
		bucAgentID,
		timeout,
	)
}

func waitForASBIdentitySourceBUC(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
	bucAgentID string,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("ASB WireGuard ready timeout is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Source establishment and rotation prove BUC attach from persisted tokens.
	// A1 and other CLI checks belong to real task verification and must not make
	// an otherwise valid seed fail.
	ticker := time.NewTicker(asbIdentityProbeInterval(timeout))
	defer ticker.Stop()
	var lastErr error
	for {
		if err := probeASBIdentitySourceBUC(
			probeCtx,
			client,
			sandboxID,
			employeeID,
			bucAgentID,
		); err == nil {
			return nil
		} else {
			// A probe that starts just before the outer deadline can return the
			// deadline itself. Preserve the last substantive result so a seed
			// that consistently presented the wrong identity is still classified
			// as requiring reauthorization after the full convergence window.
			if !errors.Is(err, context.DeadlineExceeded) || lastErr == nil {
				lastErr = err
			}
		}
		select {
		case <-probeCtx.Done():
			if !errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
				return probeCtx.Err()
			}
			if errors.Is(lastErr, ErrEnterpriseIdentityNeedsReauth) {
				return fmt.Errorf(
					"temporary ASB enterprise identity seed did not present the expected identity within %s: %w",
					timeout,
					ErrEnterpriseIdentityNeedsReauth,
				)
			}
			return fmt.Errorf(
				"temporary ASB enterprise identity seed did not become ready within %s: %w",
				timeout,
				lastErr,
			)
		case <-ticker.C:
		}
	}
}

func attachASBBUCIdentitySource(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	grant ASBBUCIdentityGrant,
) error {
	if err := attachASBBUCIdentityOnce(ctx, client, sandboxID, grant); err != nil {
		return fmt.Errorf("attach BUC identity to temporary ASB source: %w", err)
	}
	return nil
}

func attachASBBUCIdentityOnce(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	grant ASBBUCIdentityGrant,
) error {
	// Submit attachWireguardIdentity once with sync=true. ASB can finish the
	// attach but misclassify its post-attach tunnel check as HTTP 400. Multica
	// ignores only that known false negative and proves the effective identity
	// with the BUC probe. Never repeat this POST because it restarts wgclient.
	err := client.AttachBUCIdentity(ctx, sandboxID, grant)
	if err == nil {
		return nil
	}
	if isASBWireGuardPostAttachCheckPending(err) {
		slog.Info(
			"ASB BUC identity bootstrap submitted; post-attach check is pending",
			"sandbox_id", sandboxID,
		)
		return nil
	}
	return err
}

func isASBWireGuardPostAttachCheckPending(err error) bool {
	var httpErr *ASBHTTPError
	if !errors.As(err, &httpErr) ||
		httpErr.Operation != "attach_buc_identity" ||
		httpErr.StatusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(httpErr.ErrorMessage))
	if strings.Contains(message, "commandexecerror") ||
		strings.Contains(message, "exit status") ||
		strings.Contains(message, `"type":"error"`) {
		return false
	}
	return strings.Contains(message, "tunnel not ready") ||
		(strings.Contains(message, "failed to check wireguard status") &&
			strings.Contains(message, "status code 404"))
}

func asbIdentityProbeInterval(timeout time.Duration) time.Duration {
	interval := defaultASBWireGuardProbeInterval
	if timeout < interval {
		interval = timeout / 10
	}
	if interval < time.Millisecond {
		return time.Millisecond
	}
	return interval
}

func prepareASBIdentitySource(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
	bucAgentID string,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("ASB identity source lifecycle timeout is not configured")
	}
	sourceCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sandbox, err := client.GetSandbox(sourceCtx, sandboxID)
	if err != nil {
		if isASBSandboxNotFound(err) {
			return ErrEnterpriseIdentityNeedsReauth
		}
		return fmt.Errorf("load ASB enterprise identity source: %w", err)
	}
	switch state := strings.ToLower(strings.TrimSpace(sandbox.Status.State)); state {
	case "terminated":
		return nil
	case "paused":
		if err := client.ResumeSandbox(sourceCtx, sandboxID); err != nil {
			return fmt.Errorf("resume ASB enterprise identity source: %w", err)
		}
	case "pausing":
		if err := waitForASBSandboxState(sourceCtx, client, sandboxID, timeout, "paused"); err != nil {
			return err
		}
		if err := client.ResumeSandbox(sourceCtx, sandboxID); err != nil {
			return fmt.Errorf("resume ASB enterprise identity source: %w", err)
		}
	case "running", "pending", "resuming":
	case "failed", "error":
		return ErrEnterpriseIdentityNeedsReauth
	default:
		return fmt.Errorf("ASB enterprise identity source has unsupported state %q", state)
	}
	if err := waitForASBSandboxState(sourceCtx, client, sandboxID, timeout, "running"); err != nil {
		return err
	}
	if err := probeASBIdentitySourceBUC(
		sourceCtx,
		client,
		sandboxID,
		employeeID,
		bucAgentID,
	); err != nil {
		return fmt.Errorf("probe resumed ASB enterprise identity source: %w", err)
	}
	return nil
}

func probeASBIdentitySourceBUC(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
	bucAgentID string,
) error {
	endpoint, err := client.GetEndpoint(ctx, sandboxID, asbExecPort)
	if err != nil {
		return fmt.Errorf("resolve ASB identity source command endpoint: %w", err)
	}
	result, err := client.Exec(ctx, endpoint, ASBExecInput{
		Command: asbBUCOnlyIdentityProbeCommand(),
		CWD:     "/",
		Timeout: 15 * time.Second,
		Envs: map[string]string{
			"HOME":                  asbRunnerHome,
			"USER":                  "user",
			"LOGNAME":               "user",
			"EXPECTED_EMP_ID":       employeeID,
			"EXPECTED_BUC_AGENT_ID": bucAgentID,
		},
	})
	if err != nil {
		return err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.ErrorName != "" {
		if result.ExitCode != nil && *result.ExitCode == asbIdentityInvalidExitCode {
			return ErrEnterpriseIdentityNeedsReauth
		}
		return errors.New("ASB BUC identity probe failed")
	}
	return nil
}

func validateASBIdentitySeed(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) error {
	source, err := client.GetSandbox(ctx, sandboxID)
	if err != nil {
		if isASBSandboxNotFound(err) {
			return ErrEnterpriseIdentityNeedsReauth
		}
		return fmt.Errorf("load ASB enterprise identity seed: %w", err)
	}
	switch state := strings.ToLower(strings.TrimSpace(source.Status.State)); state {
	case "running", "paused", "terminated":
		return nil
	case "failed", "error":
		return ErrEnterpriseIdentityNeedsReauth
	default:
		return fmt.Errorf("ASB enterprise identity seed has unsupported state %q", state)
	}
}

func terminateASBIdentitySource(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("ASB identity source lifecycle timeout is not configured")
	}
	sourceCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := validateASBIdentitySeed(sourceCtx, client, sandboxID); err != nil {
		return err
	}
	source, err := client.GetSandbox(sourceCtx, sandboxID)
	if err != nil {
		if isASBSandboxNotFound(err) {
			return ErrEnterpriseIdentityNeedsReauth
		}
		return err
	}
	if strings.EqualFold(strings.TrimSpace(source.Status.State), "terminated") {
		return nil
	}
	if err := client.DeleteSandbox(sourceCtx, sandboxID); err != nil {
		return fmt.Errorf("terminate ASB enterprise identity seed: %w", err)
	}
	if err := waitForASBSandboxState(sourceCtx, client, sandboxID, timeout, "terminated"); err != nil {
		return fmt.Errorf("wait for ASB enterprise identity seed termination: %w", err)
	}
	return nil
}

func deleteASBIdentitySourceIfExists(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("ASB identity source lifecycle timeout is not configured")
	}
	source, err := client.GetSandbox(ctx, sandboxID)
	if err != nil {
		if isASBSandboxNotFound(err) {
			return nil
		}
		return fmt.Errorf("load ASB enterprise identity source before deletion: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(source.Status.State), "terminated") {
		return nil
	}
	if err := client.DeleteSandbox(ctx, sandboxID); err != nil {
		return fmt.Errorf("delete ASB enterprise identity source: %w", err)
	}
	if err := waitForASBSandboxState(ctx, client, sandboxID, timeout, "terminated"); err != nil {
		return fmt.Errorf("wait for ASB enterprise identity source deletion: %w", err)
	}
	return nil
}

func isASBSandboxNotFound(err error) bool {
	var httpErr *ASBHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound
}

func waitForASBSandboxState(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	timeout time.Duration,
	expected string,
) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastState string
	for {
		sandbox, err := client.GetSandbox(ctx, sandboxID)
		if err == nil {
			lastState = strings.ToLower(strings.TrimSpace(sandbox.Status.State))
			if lastState == expected {
				return nil
			}
			switch lastState {
			case "failed", "terminated", "error":
				return ErrEnterpriseIdentityNeedsReauth
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf(
				"ASB enterprise identity source did not enter %s within %s (last_state=%s)",
				expected,
				timeout,
				lastState,
			)
		case <-ticker.C:
		}
	}
}

func probeASBBUCIdentity(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
	bucAgentID string,
) error {
	endpoint, err := client.GetEndpoint(ctx, sandboxID, asbExecPort)
	if err != nil {
		if sandbox, stateErr := client.GetSandbox(ctx, sandboxID); stateErr == nil {
			return fmt.Errorf(
				"resolve ASB command endpoint for BUC probe (sandbox_state=%s): %w",
				strings.ToLower(strings.TrimSpace(sandbox.Status.State)),
				err,
			)
		}
		return fmt.Errorf("resolve ASB command endpoint for BUC probe: %w", err)
	}
	result, err := client.Exec(ctx, endpoint, ASBExecInput{
		Command: asbBUCIdentityProbeCommand(),
		CWD:     "/",
		Timeout: 60 * time.Second,
		Envs: map[string]string{
			"HOME":                  asbRunnerHome,
			"USER":                  "user",
			"LOGNAME":               "user",
			"EXPECTED_EMP_ID":       employeeID,
			"EXPECTED_BUC_AGENT_ID": bucAgentID,
		},
	})
	if err != nil {
		return err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.ErrorName != "" {
		stage := asbIdentityProbeStage(result.Stderr)
		if stage == "" {
			stage = "unknown"
		}
		return &asbEnterpriseCLIIdentityProbeError{stage: stage}
	}
	return nil
}

type asbEnterpriseCLIIdentityProbeError struct {
	stage string
}

func (e *asbEnterpriseCLIIdentityProbeError) Error() string {
	return fmt.Sprintf("ASB enterprise CLI identity probe failed at %s", e.stage)
}

func asbEnterpriseCLIIdentityProbeFailureStage(err error) string {
	var probeErr *asbEnterpriseCLIIdentityProbeError
	if errors.As(err, &probeErr) {
		return probeErr.stage
	}
	return ""
}

func asbBUCOnlyIdentityProbeCommand() string {
	// The sandbox=true variant additionally requires an SSO ticket. Source and
	// task startup only need to prove the injected zero-trust identity.
	return "set -euo pipefail; " +
		"printf 'probe_stage=buc\\n' >&2; " +
		"curl -fsS --max-time 10 -X POST " +
		"'https://login.alibaba-inc.com/rpc/cli/v1/get_zt_identity.json' | " +
		"/opt/task-python/bin/python -c '" +
		"import json,os,sys; p=json.load(sys.stdin); d=p.get(\"content\",{}).get(\"data\",{}); " +
		"ok=p.get(\"success\") is True and str(p.get(\"errorCode\")) == \"0\" and " +
		"str(d.get(\"empId\", \"\")) == os.environ[\"EXPECTED_EMP_ID\"] and " +
		"str(d.get(\"agentId\", \"\")) == os.environ[\"EXPECTED_BUC_AGENT_ID\"]; " +
		fmt.Sprintf("raise SystemExit(0 if ok else %d)", asbIdentityInvalidExitCode) +
		"'"
}

const asbIdentityInvalidExitCode = 42

func asbBUCIdentityProbeCommand() string {
	return asbBUCOnlyIdentityProbeCommand() + "; " +
		"printf 'probe_stage=a1\\n' >&2; " +
		"a1 --no-update-check -f json auth whoami >/dev/null; " +
		"printf 'probe_stage=complete\\n' >&2"
}

func asbIdentityProbeStage(stderr string) string {
	const marker = "probe_stage="
	var stage string
	for remaining := stderr; ; {
		markerIndex := strings.Index(remaining, marker)
		if markerIndex < 0 {
			break
		}
		value := remaining[markerIndex+len(marker):]
		// Execd may deliver adjacent stderr writes as separate SSE events and
		// decodeASBExecStream intentionally concatenates their text verbatim.
		// Match only the markers emitted by our probe command so a following
		// curl/a1 error cannot turn "a1" into an unknown failure stage and
		// suppress the documented Agent Identity re-attachment.
		switch {
		case strings.HasPrefix(value, "complete"):
			stage = "complete"
		case strings.HasPrefix(value, "a1"):
			stage = "a1"
		case strings.HasPrefix(value, "buc"):
			stage = "buc"
		}
		remaining = value
	}
	return stage
}

func logASBIdentitySourceFailure(stage string, sandboxID string, err error) {
	attributes := []any{
		"stage", stage,
		"sandbox_id", strings.TrimSpace(sandboxID),
		"error_class", enterpriseIdentityErrorClass(err),
		"error", redact.Text(err.Error()),
	}
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) {
		attributes = append(
			attributes,
			"operation", httpErr.Operation,
			"http_status", httpErr.StatusCode,
			"request_id", httpErr.RequestID,
			"error_code", httpErr.ErrorCode,
			"error_message", redact.Text(httpErr.ErrorMessage),
		)
	}
	slog.Warn("ASB enterprise identity source stage failed", attributes...)
}
