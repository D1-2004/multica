package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
		pgtype.UUID,
		pgtype.UUID,
		pgtype.UUID,
		string,
		string,
		BUCIdentityTokens,
	) (EnterpriseIdentitySourceAvailability, error)
	Prepare(context.Context, pgtype.UUID, string, string, string) error
	Park(context.Context, pgtype.UUID, string) error
	Delete(context.Context, pgtype.UUID, string) error
}

type asbIdentitySourceRuntimeStore interface {
	GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error)
}

// ASBIdentitySourceManager establishes a BUC credential directory inside a
// dedicated ASB sandbox. The source stays Running so task sandboxes can inherit
// that directory through buc.originalSandboxID. ASB BUC identity requires an
// egress-wgclient sidecar, while the documented snapshot/checkpoint persistence
// modes support only single-container sandboxes, so pausing this source is not
// a supported persistence boundary.
type ASBIdentitySourceManager struct {
	Store       asbIdentitySourceRuntimeStore
	Credentials *ASBRuntimeClientProvider
	Capacity    ASBSandboxCapacity
	Config      ASBConfig
}

func (m *ASBIdentitySourceManager) Create(
	ctx context.Context,
	runtimeID pgtype.UUID,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	employeeID string,
	bucAgentID string,
	tokens BUCIdentityTokens,
) (EnterpriseIdentitySourceAvailability, error) {
	if m == nil || m.Store == nil || m.Credentials == nil || m.Capacity == nil {
		return EnterpriseIdentitySourceAvailability{}, errors.New(
			"ASB enterprise identity source manager is unavailable",
		)
	}
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
		ResourceCPU:    m.Config.ResourceCPU,
		ResourceMemory: m.Config.ResourceMemory,
		Entrypoint:     []string{"sleep infinity"},
		Metadata: map[string]string{
			"multica.identity_source": "true",
			"multica.workspace_id":    util.UUIDToString(workspaceID),
			"multica.agent_id":        util.UUIDToString(agentID),
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
	retained := false
	defer func() {
		if retained {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cleanupErr := client.DeleteSandbox(cleanupCtx, sandbox.ID); cleanupErr != nil {
			logASBIdentitySourceFailure("cleanup_sandbox", sandbox.ID, cleanupErr)
		}
	}()
	if err := waitForASBSandboxRunning(
		ctx,
		client,
		sandbox.ID,
		m.Config.ReadyTimeout,
	); err != nil {
		logASBIdentitySourceFailure("wait_running", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, err
	}
	if err := attachAndProbeASBIdentitySource(
		ctx,
		client,
		sandbox.ID,
		employeeID,
		bucAgentID,
		tokens,
		m.Config.WireGuardCredentials,
		m.Config.WireGuardReadyTimeout,
	); err != nil {
		logASBIdentitySourceFailure("establish_buc_identity", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, err
	}
	if err := renewASBIdentitySource(ctx, client, sandbox.ID); err != nil {
		logASBIdentitySourceFailure("renew_source", sandbox.ID, err)
		return EnterpriseIdentitySourceAvailability{}, fmt.Errorf(
			"renew temporary ASB enterprise identity source: %w",
			err,
		)
	}
	retained = true
	slog.Info(
		"running ASB enterprise identity source established",
		"runtime_id", util.UUIDToString(runtimeID),
		"agent_id", util.UUIDToString(agentID),
		"sandbox_id", sandbox.ID,
		"running_quota_retained", true,
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
	if err := keepASBIdentitySourceRunning(
		ctx,
		client,
		sandboxID,
	); err != nil {
		logASBIdentitySourceFailure("keep_source_running", sandboxID, err)
		return err
	}
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
	return deleteASBSandboxIfExists(ctx, client, sandboxID)
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
	timeout := m.Config.ReadyTimeout
	if m.Config.WireGuardReadyTimeout > timeout {
		timeout = m.Config.WireGuardReadyTimeout
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
	if timeout <= 0 {
		return errors.New("ASB WireGuard ready timeout is not configured")
	}
	identityCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := attachASBBUCIdentitySource(
		identityCtx,
		client,
		sandboxID,
		ASBBUCIdentityGrant{
			EmployeeID:           employeeID,
			BUCAccessToken:       tokens.AccessToken,
			BUCRefreshToken:      tokens.RefreshToken,
			BUCIDToken:           tokens.IDToken,
			WireGuardCredentials: wireGuardCredentials,
		},
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

	// The attachment is deliberately submitted exactly once. ASB documents that
	// every repeated WireGuard attachment restarts the VPN tunnel, so retrying
	// the POST while it is converging can prevent it from ever becoming ready.
	// Readiness is determined only by the identity probes below.
	ticker := time.NewTicker(asbIdentityProbeInterval(timeout))
	defer ticker.Stop()
	var lastErr error
	for {
		if err := probeASBBUCIdentity(
			identityCtx,
			client,
			sandboxID,
			employeeID,
			bucAgentID,
		); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-identityCtx.Done():
			if !errors.Is(identityCtx.Err(), context.DeadlineExceeded) {
				return identityCtx.Err()
			}
			return fmt.Errorf(
				"temporary ASB enterprise identity source did not become ready within %s: %w",
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
	// Use one synchronous attachment. ASB only reports HTTP 200 after the
	// sandbox-side WireGuard setup has completed; returning from an async
	// attachment while that setup is still replacing sandbox resources can
	// expose a transient Running state and then a terminal sandbox. Retrying
	// the attachment is also unsafe because every POST restarts the setup.
	if err := client.AttachBUCIdentity(ctx, sandboxID, grant, true); err != nil {
		return fmt.Errorf("attach BUC identity to temporary ASB source: %w", err)
	}
	return nil
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
		return fmt.Errorf("load ASB enterprise identity source: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(sandbox.Status.State), "running") {
		return ErrEnterpriseIdentityNeedsReauth
	}
	if err := renewASBIdentitySource(sourceCtx, client, sandboxID); err != nil {
		return err
	}
	if err := probeASBBUCIdentity(sourceCtx, client, sandboxID, employeeID, bucAgentID); err != nil {
		return fmt.Errorf("probe running ASB enterprise identity source: %w", err)
	}
	return nil
}

func keepASBIdentitySourceRunning(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) error {
	source, err := client.GetSandbox(ctx, sandboxID)
	if err != nil {
		return fmt.Errorf("load ASB enterprise identity source after inheritance: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(source.Status.State), "running") {
		return ErrEnterpriseIdentityNeedsReauth
	}
	return nil
}

func renewASBIdentitySource(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) error {
	expiresAt := time.Now().Add(asbMaxRenewalDuration - time.Minute)
	if err := client.RenewSandbox(ctx, sandboxID, expiresAt, false); err != nil {
		return fmt.Errorf("renew ASB enterprise identity source: %w", err)
	}
	return nil
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
		return fmt.Errorf("ASB enterprise CLI identity probe failed at %s", stage)
	}
	return nil
}

func asbBUCIdentityProbeCommand() string {
	return "set -euo pipefail; " +
		"printf 'probe_stage=buc\\n' >&2; " +
		"curl -fsS --max-time 10 -X POST " +
		"'https://login.alibaba-inc.com/rpc/cli/v1/get_zt_identity.json?sandbox=true' | " +
		"/opt/task-python/bin/python -c '" +
		"import json,os,sys; p=json.load(sys.stdin); d=p.get(\"content\",{}).get(\"data\",{}); " +
		"ok=p.get(\"success\") is True and str(p.get(\"errorCode\")) == \"0\" and " +
		"str(d.get(\"empId\", \"\")) == os.environ[\"EXPECTED_EMP_ID\"] and " +
		"str(d.get(\"agentId\", \"\")) == os.environ[\"EXPECTED_BUC_AGENT_ID\"]; " +
		"raise SystemExit(0 if ok else 1)" +
		"'; " +
		"printf 'probe_stage=a1\\n' >&2; " +
		"a1 --no-update-check -f json auth whoami >/dev/null; " +
		"printf 'probe_stage=nw_aliwork_login\\n' >&2; " +
		"nw-aliwork-cli login --no-update-check >/dev/null; " +
		"printf 'probe_stage=nw_aliwork_whoami\\n' >&2; " +
		"nw-aliwork-cli whoami --no-update-check >/dev/null; " +
		"printf 'probe_stage=complete\\n' >&2"
}

func asbIdentityProbeStage(stderr string) string {
	var stage string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "probe_stage=") {
			stage = strings.TrimSpace(strings.TrimPrefix(line, "probe_stage="))
		}
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
