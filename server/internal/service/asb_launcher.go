package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	defaultASBTimeoutSeconds      = 3600
	defaultASBAnchorTimeout       = asbMaxRenewalDuration
	defaultASBReadyTimeout        = 90 * time.Second
	defaultASBIdentityProbePeriod = time.Second
	defaultASBIdentityProbeLimit  = 60 * time.Second
	defaultASBResourceCPU         = "2"
	defaultASBResourceMemory      = "4Gi"
	asbRunnerHome                 = "/home/user"
)

// ASBConfig is the deployment-owned configuration for the Aone Sandbox
// backend. Runtime-specific image digests stay in runtime metadata.
type ASBConfig struct {
	Enabled                bool
	APIURL                 string
	APIKey                 string
	ServerURL              string
	LLMBaseURL             string
	LLMAPIKey              string
	LLMModels              []string
	TimeoutSeconds         int
	IdentityAnchorTimeout  time.Duration
	ReadyTimeout           time.Duration
	IdentityProbeTimeout   time.Duration
	ResourceCPU            string
	ResourceMemory         string
	WireGuardCredentials   string
	IdentityAnchorImageRef string
	ParseError             error
}

func ASBConfigFromEnv() ASBConfig {
	cfg := ASBConfig{
		Enabled:                envBool("MULTICA_ASB_ENABLED"),
		APIURL:                 strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_API_URL")), "/"),
		APIKey:                 strings.TrimSpace(os.Getenv("MULTICA_ASB_API_KEY")),
		ServerURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_SERVER_URL")), "/"),
		LLMBaseURL:             strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:              strings.TrimSpace(os.Getenv("MULTICA_ASB_OPENAI_API_KEY")),
		TimeoutSeconds:         defaultASBTimeoutSeconds,
		IdentityAnchorTimeout:  defaultASBAnchorTimeout,
		ReadyTimeout:           defaultASBReadyTimeout,
		IdentityProbeTimeout:   defaultASBIdentityProbeLimit,
		ResourceCPU:            firstNonEmptyString(os.Getenv("MULTICA_ASB_RESOURCE_CPU"), defaultASBResourceCPU),
		ResourceMemory:         firstNonEmptyString(os.Getenv("MULTICA_ASB_RESOURCE_MEMORY"), defaultASBResourceMemory),
		WireGuardCredentials:   strings.TrimSpace(os.Getenv("MULTICA_ASB_WG_CLIENT_CREDENTIALS")),
		IdentityAnchorImageRef: strings.TrimSpace(os.Getenv("MULTICA_ASB_IDENTITY_ANCHOR_IMAGE")),
	}
	models, err := parseStringListEnv("MULTICA_ASB_OPENAI_MODELS", os.Getenv("MULTICA_ASB_OPENAI_MODELS"))
	if err != nil {
		cfg.ParseError = errors.Join(cfg.ParseError, err)
	} else {
		cfg.LLMModels = models
	}
	parsePositiveIntEnv("MULTICA_ASB_TIMEOUT_SECONDS", &cfg.TimeoutSeconds, &cfg.ParseError)
	parsePositiveDurationEnv("MULTICA_ASB_IDENTITY_ANCHOR_TIMEOUT", &cfg.IdentityAnchorTimeout, &cfg.ParseError)
	parsePositiveDurationEnv("MULTICA_ASB_READY_TIMEOUT", &cfg.ReadyTimeout, &cfg.ParseError)
	parsePositiveDurationEnv("MULTICA_ASB_IDENTITY_PROBE_TIMEOUT", &cfg.IdentityProbeTimeout, &cfg.ParseError)
	return cfg
}

func parseStringListEnv(name, raw string) ([]string, error) {
	values, err := parseFCE2BModels(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: expected a JSON string array", name)
	}
	return values, nil
}

func parsePositiveIntEnv(name string, target *int, parseErr *error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		*parseErr = errors.Join(*parseErr, fmt.Errorf("invalid %s", name))
		return
	}
	*target = value
}

func parsePositiveDurationEnv(name string, target *time.Duration, parseErr *error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		*parseErr = errors.Join(*parseErr, fmt.Errorf("invalid %s", name))
		return
	}
	*target = value
}

func (c ASBConfig) Validate() error {
	if c.ParseError != nil {
		return c.ParseError
	}
	var missing []string
	required := []struct {
		name  string
		value string
	}{
		{"MULTICA_ASB_API_URL", c.APIURL},
		{"MULTICA_ASB_API_KEY", c.APIKey},
		{"MULTICA_ASB_SERVER_URL", c.ServerURL},
		{"MULTICA_ASB_OPENAI_BASE_URL", c.LLMBaseURL},
		{"MULTICA_ASB_OPENAI_API_KEY", c.LLMAPIKey},
		{"MULTICA_ASB_RESOURCE_CPU", c.ResourceCPU},
		{"MULTICA_ASB_RESOURCE_MEMORY", c.ResourceMemory},
		{"MULTICA_ASB_WG_CLIENT_CREDENTIALS", c.WireGuardCredentials},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.name)
		}
	}
	if len(c.LLMModels) == 0 {
		missing = append(missing, "MULTICA_ASB_OPENAI_MODELS")
	}
	if c.TimeoutSeconds <= 0 {
		missing = append(missing, "MULTICA_ASB_TIMEOUT_SECONDS")
	} else if c.TimeoutSeconds < asbMinCreateTimeout || c.TimeoutSeconds > asbMaxCreateTimeout {
		return fmt.Errorf(
			"invalid MULTICA_ASB_TIMEOUT_SECONDS: must be between %d and %d",
			asbMinCreateTimeout,
			asbMaxCreateTimeout,
		)
	}
	if c.IdentityAnchorTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_IDENTITY_ANCHOR_TIMEOUT")
	} else if c.IdentityAnchorTimeout < time.Duration(asbMinCreateTimeout)*time.Second ||
		c.IdentityAnchorTimeout > asbMaxRenewalDuration {
		return fmt.Errorf(
			"invalid MULTICA_ASB_IDENTITY_ANCHOR_TIMEOUT: must be between %s and %s",
			time.Duration(asbMinCreateTimeout)*time.Second,
			asbMaxRenewalDuration,
		)
	}
	if c.ReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_READY_TIMEOUT")
	}
	if c.IdentityProbeTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_IDENTITY_PROBE_TIMEOUT")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing ASB config: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (c ASBConfig) ModelForAgent(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		if len(c.LLMModels) == 0 {
			return "", errors.New("MULTICA_ASB_OPENAI_MODELS is empty")
		}
		return c.LLMModels[0], nil
	}
	for _, allowed := range c.LLMModels {
		if model == allowed {
			return model, nil
		}
	}
	return "", fmt.Errorf("agent model %q is not configured in MULTICA_ASB_OPENAI_MODELS", model)
}

// ASBResolvedIdentity contains only the short-lived AIT needed for one launch
// plus stable, non-secret identity coordinates.
type ASBResolvedIdentity struct {
	RawEmployeeID      string
	BUCAgentID         string
	AgentSPIFFEID      string
	AIPID              string
	AnchorSandboxID    string
	AgentIdentityToken string
	Fingerprint        string
}

type ASBArtifact struct {
	Ref      string
	BuildID  string
	Alias    string
	Digest   string
	Manifest map[string]any
}

func BuildASBRuntimeMetadata(
	artifact ASBArtifact,
	provider string,
	channel string,
) (map[string]any, error) {
	if err := validateASBArtifact(artifact); err != nil {
		return nil, err
	}
	if err := validateASBRuntimeManifest(artifact.Manifest); err != nil {
		return nil, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = FCE2BProvider
	}
	if !IsFCE2BSupportedProvider(provider) ||
		!containsAllStrings(stringSliceMetadataValue(artifact.Manifest, "providers"), provider) {
		return nil, ErrFCE2BTemplateProviderUnsupported
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != CloudSandboxChannelStable && channel != CloudSandboxChannelCandidate {
		return nil, errors.New("artifact channel must be 'stable' or 'candidate'")
	}
	alias := strings.TrimSpace(artifact.Alias)
	if alias == "" {
		alias = asbArtifactAlias(artifact.Ref, artifact.BuildID)
	}
	return map[string]any{
		"kind":               CloudSandboxMetadataKind,
		"sandbox_backend":    string(SandboxBackendASB),
		"provider":           provider,
		"artifact_kind":      CloudSandboxArtifactOCIImage,
		"artifact_channel":   channel,
		"artifact_ref":       artifact.Ref,
		"artifact_build_id":  artifact.BuildID,
		"artifact_alias":     alias,
		"artifact_digest":    artifact.Digest,
		"artifact_status":    "READY",
		"manifest_version":   intMetadataValue(artifact.Manifest, "schema_version"),
		"capabilities":       manifestStringSliceForBackend(artifact.Manifest, "capabilities_by_backend", "asb"),
		"component_versions": artifact.Manifest["component_versions"],
		"runner_protocol":    stringMetadataValue(artifact.Manifest, "runner_protocol"),
		"runner":             FCE2BRunnerCommandForProvider(provider),
	}, nil
}

type ASBRuntimeArtifactUpdateResult struct {
	Runtime                 db.AgentRuntime
	PreviousArtifactRef     string
	PreviousArtifactBuildID string
	PreviousArtifactDigest  string
	InvalidatedSandboxCount int64
	Changed                 bool
}

type ASBTaskIdentityResolver interface {
	ResolveASBTaskIdentity(context.Context, pgtype.UUID, pgtype.UUID) (ASBResolvedIdentity, error)
}

type ASBLauncher struct {
	Queries  *db.Queries
	Tasks    *TaskService
	Common   *FCE2BLauncher
	Config   ASBConfig
	Client   *ASBClient
	Identity ASBTaskIdentityResolver
	Pool     *pgxpool.Pool
}

type asbLaunchSubmission struct {
	runtime             db.AgentRuntime
	sandboxID           string
	coldStart           bool
	scope               fcE2BTaskScope
	scoped              bool
	identityFingerprint string
}

func NewASBLauncher(
	queries *db.Queries,
	tasks *TaskService,
	common *FCE2BLauncher,
	cfg ASBConfig,
	client *ASBClient,
	identity ASBTaskIdentityResolver,
) *ASBLauncher {
	return &ASBLauncher{
		Queries:  queries,
		Tasks:    tasks,
		Common:   common,
		Config:   cfg,
		Client:   client,
		Identity: identity,
	}
}

func (l *ASBLauncher) SetPool(pool *pgxpool.Pool) {
	if l != nil {
		l.Pool = pool
	}
}

func (l *ASBLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	if l == nil || l.Queries == nil || l.Tasks == nil || !task.RuntimeID.Valid {
		return nil
	}
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return fmt.Errorf("load runtime for ASB launch: %w", err)
	}
	if !IsASBRuntime(runtime) {
		return nil
	}
	taskID := util.UUIDToString(task.ID)
	runtimeID := util.UUIDToString(task.RuntimeID)
	trace, traceErr := chattrace.ForTask(task.Context, taskID, task.CreatedAt.Time)
	if traceErr != nil {
		return l.failLaunch(ctx, task, "invalid task trace: "+traceErr.Error())
	}
	chattrace.LogStage(slog.Default(), trace, "asb_launch", "started", "task_id", taskID, "runtime_id", runtimeID)
	if !l.Config.Enabled {
		return l.failLaunch(ctx, task, "ASB runtime is disabled")
	}
	if err := l.Config.Validate(); err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if l.Client == nil {
		return l.failLaunch(ctx, task, "ASB client is unavailable")
	}
	if l.Identity == nil {
		return l.failLaunch(ctx, task, "Agent enterprise identity service is unavailable")
	}

	identity, err := l.Identity.ResolveASBTaskIdentity(ctx, runtime.WorkspaceID, task.AgentID)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if l.Pool == nil {
		return l.failLaunch(ctx, task, "ASB sandbox coordination requires a database pool")
	}

	runtimeLockConn, releaseRuntimeLock, err := l.lockRuntimeShared(ctx, task.RuntimeID)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			releaseRuntimeLock()
		}
	}()

	locked := *l
	locked.Queries = db.New(runtimeLockConn)
	if locked.Common != nil {
		common := *locked.Common
		common.Queries = locked.Queries
		locked.Common = &common
	}
	submission, deferred, err := locked.submitTaskUnderRuntimeLock(ctx, task, runtimeLockConn, identity, trace)
	releaseRuntimeLock()
	lockHeld = false
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if deferred {
		return nil
	}

	slog.Info("ASB run-once submitted",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"sandbox_id", submission.sandboxID,
		"cold_start", submission.coldStart,
	)
	chattrace.LogStage(slog.Default(), trace, "asb_run_once", "submitted",
		"task_id", taskID,
		"sandbox_id", submission.sandboxID,
	)
	claimState, err := l.waitForRunOnceClaim(ctx, task)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if claimState == fcE2BRunnerClaimStalled {
		return l.failLaunch(ctx, task, fmt.Sprintf("ASB runner did not claim task within %s after sandbox exec", fcE2BRunnerClaimTimeout))
	}
	chattrace.LogStage(slog.Default(), trace, "asb_claim", string(claimState),
		"task_id", taskID,
		"sandbox_id", submission.sandboxID,
	)
	if submission.scoped {
		_ = l.Queries.TouchCloudSandboxSession(ctx, db.TouchCloudSandboxSessionParams{
			RuntimeID:           submission.runtime.ID,
			ScopeType:           submission.scope.typ,
			ScopeID:             submission.scope.id,
			SandboxID:           submission.sandboxID,
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: submission.identityFingerprint,
		})
	}
	return nil
}

func (l *ASBLauncher) submitTaskUnderRuntimeLock(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtimeLockConn *pgxpool.Conn,
	identity ASBResolvedIdentity,
	trace chattrace.Trace,
) (asbLaunchSubmission, bool, error) {
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("reload runtime under ASB runtime lock: %w", err)
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil || metadata.SandboxBackend != SandboxBackendASB {
		return asbLaunchSubmission{}, false, ErrCloudSandboxRuntimeRequired
	}
	if !runtime.DaemonID.Valid || strings.TrimSpace(runtime.DaemonID.String) == "" {
		return asbLaunchSubmission{}, false, errors.New("ASB runtime has no daemon_id")
	}
	if !runtime.OwnerID.Valid {
		return asbLaunchSubmission{}, false, errors.New("ASB runtime has no owner_id")
	}
	tasks, err := l.Queries.ListAgentTasks(ctx, task.AgentID)
	if err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("check ASB launch serialization: %w", err)
	}
	if blocker, reason, blocked := fcE2BTaskLaunchBlocker(task, tasks); blocked {
		slog.Info("ASB launch deferred by serialized task",
			"task_id", util.UUIDToString(task.ID),
			"runtime_id", util.UUIDToString(task.RuntimeID),
			"blocker_task_id", util.UUIDToString(blocker.ID),
			"blocker_status", blocker.Status,
			"defer_reason", reason,
		)
		return asbLaunchSubmission{}, true, nil
	}

	scope, scoped := fcE2BScopeForTask(task)
	sandboxID, coldStart, err := l.resolveSandbox(
		ctx,
		runtime,
		metadata,
		scope,
		scoped,
		identity,
		runtimeLockConn,
		trace,
	)
	if err != nil {
		return asbLaunchSubmission{}, false, err
	}
	extraEnv, err := l.extraEnvForTask(ctx, task, runtime, sandboxID)
	if err != nil {
		return asbLaunchSubmission{}, false, err
	}
	token, err := auth.GenerateDaemonToken()
	if err != nil {
		return asbLaunchSubmission{}, false, errors.New("failed to mint ASB daemon token")
	}
	tokenExpiresAt := time.Now().Add(fcE2BDaemonTokenTTL)
	if l.Common != nil {
		extraEnv, err = l.Common.withSandboxRelayToken(
			extraEnv,
			task.ID,
			task.AgentID,
			runtime.ID,
			sandboxID,
			token,
			tokenExpiresAt,
		)
		if err != nil {
			return asbLaunchSubmission{}, false, err
		}
	}
	if _, err := l.Queries.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(token),
		WorkspaceID: runtime.WorkspaceID,
		DaemonID:    runtime.DaemonID.String,
		ExpiresAt:   pgtype.Timestamptz{Time: tokenExpiresAt, Valid: true},
	}); err != nil {
		return asbLaunchSubmission{}, false, errors.New("failed to persist ASB daemon token")
	}
	if err := l.execRunOnce(ctx, sandboxID, runtime, task.ID, token, coldStart, extraEnv); err != nil {
		return asbLaunchSubmission{}, false, err
	}
	return asbLaunchSubmission{
		runtime:             runtime,
		sandboxID:           sandboxID,
		coldStart:           coldStart,
		scope:               scope,
		scoped:              scoped,
		identityFingerprint: identity.Fingerprint,
	}, false, nil
}

func (l *ASBLauncher) resolveSandbox(
	ctx context.Context,
	runtime db.AgentRuntime,
	metadata CloudSandboxRuntimeMetadata,
	scope fcE2BTaskScope,
	scoped bool,
	identity ASBResolvedIdentity,
	runtimeLockConn *pgxpool.Conn,
	trace chattrace.Trace,
) (string, bool, error) {
	if identity.Fingerprint == "" || identity.AnchorSandboxID == "" {
		return "", false, errors.New("Agent enterprise identity is incomplete")
	}
	if scoped {
		release, err := l.lockSandboxScopeOnConnection(ctx, runtime, scope, runtimeLockConn)
		if err != nil {
			return "", false, err
		}
		defer release()
		session, err := l.Queries.GetActiveCloudSandboxSession(ctx, db.GetActiveCloudSandboxSessionParams{
			RuntimeID:           runtime.ID,
			ScopeType:           scope.typ,
			ScopeID:             scope.id,
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: identity.Fingerprint,
			ArtifactRef:         metadata.ArtifactRef,
		})
		if err == nil {
			if err := l.ensureSandboxIdentityReady(ctx, session.SandboxID, identity); err != nil {
				_ = l.Queries.MarkCloudSandboxSessionStale(ctx, db.MarkCloudSandboxSessionStaleParams{
					RuntimeID:           runtime.ID,
					ScopeType:           scope.typ,
					ScopeID:             scope.id,
					SandboxID:           session.SandboxID,
					SandboxBackend:      string(SandboxBackendASB),
					IdentityFingerprint: identity.Fingerprint,
				})
				return "", false, fmt.Errorf("ASB warm sandbox is unavailable: %w", err)
			}
			return session.SandboxID, false, nil
		}
		if err != pgx.ErrNoRows {
			return "", false, fmt.Errorf("load ASB sandbox session: %w", err)
		}
	}

	chattrace.LogStage(slog.Default(), trace, "asb_sandbox_create", "started",
		"artifact_ref", metadata.ArtifactRef,
	)
	sandbox, err := l.Client.CreateSandbox(ctx, ASBCreateSandboxInput{
		ImageURI:       metadata.ArtifactRef,
		TimeoutSeconds: l.Config.TimeoutSeconds,
		ResourceCPU:    l.Config.ResourceCPU,
		ResourceMemory: l.Config.ResourceMemory,
		Entrypoint:     []string{"sleep infinity"},
		Metadata: map[string]string{
			"multica.runtime_id": util.UUIDToString(runtime.ID),
			"multica.backend":    string(SandboxBackendASB),
		},
		Extensions: map[string]string{
			"spiffe.lazyAuth":          "true",
			"wireguard.worker":         identity.RawEmployeeID,
			"wireguard.uemCredentials": l.Config.WireGuardCredentials,
			"buc.originalSandboxID":    identity.AnchorSandboxID,
		},
	})
	if err != nil {
		return "", true, fmt.Errorf("create ASB sandbox: %w", err)
	}
	keepSandbox := false
	defer func() {
		if !keepSandbox {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = l.Client.DeleteSandbox(cleanupCtx, sandbox.ID)
		}
	}()
	if err := l.waitSandboxRunning(ctx, sandbox.ID); err != nil {
		return "", true, err
	}
	if err := l.ensureSandboxIdentityReady(ctx, sandbox.ID, identity); err != nil {
		return "", true, err
	}
	if scoped {
		if _, err := l.Queries.UpsertCloudSandboxSession(ctx, db.UpsertCloudSandboxSessionParams{
			WorkspaceID:         runtime.WorkspaceID,
			RuntimeID:           runtime.ID,
			ScopeType:           scope.typ,
			ScopeID:             scope.id,
			SandboxID:           sandbox.ID,
			ArtifactRef:         metadata.ArtifactRef,
			ExpiresAt:           pgtype.Timestamptz{Time: time.Now().Add(time.Duration(l.Config.TimeoutSeconds) * time.Second), Valid: true},
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: identity.Fingerprint,
		}); err != nil {
			return "", true, fmt.Errorf("record ASB sandbox session: %w", err)
		}
	}
	keepSandbox = true
	chattrace.LogStage(slog.Default(), trace, "asb_sandbox_create", "ready",
		"sandbox_id", sandbox.ID,
	)
	return sandbox.ID, true, nil
}

func (l *ASBLauncher) waitSandboxRunning(ctx context.Context, sandboxID string) error {
	deadline := time.NewTimer(l.Config.ReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		sandbox, err := l.Client.GetSandbox(ctx, sandboxID)
		if err == nil {
			switch strings.ToLower(strings.TrimSpace(sandbox.Status.State)) {
			case "running":
				return nil
			case "failed", "terminated", "error":
				return fmt.Errorf("ASB sandbox entered terminal state %q", sandbox.Status.State)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err != nil {
				return fmt.Errorf("ASB sandbox was not running within %s: %w", l.Config.ReadyTimeout, err)
			}
			return fmt.Errorf("ASB sandbox was not running within %s", l.Config.ReadyTimeout)
		case <-ticker.C:
		}
	}
}

func (l *ASBLauncher) ensureSandboxIdentityReady(ctx context.Context, sandboxID string, identity ASBResolvedIdentity) error {
	if err := l.Client.AttachAgentIdentity(ctx, sandboxID, ASBAgentIdentityGrant{
		RawEmployeeID: identity.RawEmployeeID,
		AgentToken:    identity.AgentIdentityToken,
		AgentID:       identity.AgentSPIFFEID,
	}); err != nil {
		return fmt.Errorf("attach ASB Agent Identity: %w", err)
	}
	endpoint, err := l.Client.GetEndpoint(ctx, sandboxID, asbExecPort)
	if err != nil {
		return fmt.Errorf("resolve ASB command endpoint: %w", err)
	}
	deadline := time.NewTimer(l.Config.IdentityProbeTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(defaultASBIdentityProbePeriod)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := l.probeSandboxIdentities(ctx, endpoint, identity.RawEmployeeID); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("ASB identity probes did not become ready within %s: %w", l.Config.IdentityProbeTimeout, lastErr)
		case <-ticker.C:
		}
	}
}

func (l *ASBLauncher) probeSandboxIdentities(
	ctx context.Context,
	endpoint *ASBEndpoint,
	employeeID string,
) error {
	probe, err := asbIdentityProbeCommand(employeeID)
	if err != nil {
		return err
	}
	result, err := l.Client.Exec(ctx, endpoint, ASBExecInput{
		Command: probe,
		CWD:     "/",
		Timeout: 25 * time.Second,
		Envs: map[string]string{
			"HOME":    asbRunnerHome,
			"USER":    "user",
			"LOGNAME": "user",
		},
	})
	if err != nil {
		return err
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		return errors.New("ASB identity probe failed")
	}
	return nil
}

func asbIdentityProbeCommand(employeeID string) (string, error) {
	employeeID = strings.TrimSpace(employeeID)
	if !enterpriseEmployeeIDPattern.MatchString(employeeID) {
		return "", errors.New("ASB identity probe employee ID is invalid")
	}
	return fmt.Sprintf(
		"set -eu; "+
			"probe_dir=\"$(mktemp -d)\"; trap 'rm -rf \"$probe_dir\"' EXIT; "+
			"curl -fsS --max-time 10 -H 'Agent-Proof-Token-Audience: https://authx.alibaba-inc.com' "+
			"https://authx.alibaba-inc.com/ciap/sandbox/identity-info -d '' >/dev/null; "+
			"curl -fsS --max-time 10 -X POST "+
			"https://login.alibaba-inc.com/rpc/cli/v1/get_zt_identity.json >/dev/null; "+
			"a1 --no-update-check -f json auth whoami >\"$probe_dir/a1.json\"; "+
			"mw --no-update-check auth whoami --format json >\"$probe_dir/mw.json\"; "+
			"jq -e --arg expected '%s' '(.emp_id | tostring) == $expected' \"$probe_dir/a1.json\" >/dev/null; "+
			"jq -e --arg expected '%s' '(.employee_id | tostring) == $expected' \"$probe_dir/mw.json\" >/dev/null",
		employeeID,
		employeeID,
	), nil
}

func (l *ASBLauncher) execRunOnce(
	ctx context.Context,
	sandboxID string,
	runtime db.AgentRuntime,
	taskID pgtype.UUID,
	token string,
	coldStart bool,
	extraEnv map[string]string,
) error {
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil || metadata.SandboxBackend != SandboxBackendASB {
		return ErrCloudSandboxRuntimeRequired
	}
	for key := range extraEnv {
		if !isAllowedFCE2BRunnerExtraEnv(key) {
			return fmt.Errorf("ASB runner environment key %q is not allowed", key)
		}
	}
	command, err := asbRunnerCommand(metadata.Provider, runtime.ID, taskID)
	if err != nil {
		return err
	}
	envs := map[string]string{
		"LD_PRELOAD":                    "",
		"LD_LIBRARY_PATH":               "",
		"LD_AUDIT":                      "",
		"GCONV_PATH":                    "",
		"BASH_ENV":                      "",
		"ENV":                           "",
		"MULTICA_SERVER_URL":            l.Config.ServerURL,
		"MULTICA_DAEMON_TOKEN":          token,
		"MULTICA_RUNTIME_ID":            util.UUIDToString(runtime.ID),
		"MULTICA_TASK_ID":               util.UUIDToString(taskID),
		"MULTICA_DAEMON_ID":             runtime.DaemonID.String,
		"MULTICA_AGENT_RUNTIME_NAME":    runtime.Name,
		"MULTICA_CLOUD_SANDBOX_BACKEND": string(SandboxBackendASB),
		"MULTICA_RUNNER_PROVIDER":       metadata.Provider,
		"HOME":                          asbRunnerHome,
		"USER":                          "user",
		"LOGNAME":                       "user",
		"DWS_CONFIG_DIR":                "/home/user/.dws",
		"OPENAI_BASE_URL":               l.Config.LLMBaseURL,
		"OPENAI_API_KEY":                l.Config.LLMAPIKey,
	}
	if coldStart {
		envs["MULTICA_FC_E2B_COLD_START"] = "true"
	}
	for key, value := range extraEnv {
		envs[key] = value
	}
	endpoint, err := l.Client.GetEndpoint(ctx, sandboxID, asbExecPort)
	if err != nil {
		return fmt.Errorf("resolve ASB command endpoint: %w", err)
	}
	result, err := l.Client.Exec(ctx, endpoint, ASBExecInput{
		Command:    command,
		CWD:        "/workspace",
		Background: true,
		Timeout:    l.Config.ReadyTimeout,
		Envs:       envs,
	})
	if err != nil {
		return fmt.Errorf("ASB runner exec failed: %w", err)
	}
	if result.ErrorName != "" {
		return errors.New("ASB runner exec returned an error")
	}
	return nil
}

func asbRunnerCommand(provider string, runtimeID, taskID pgtype.UUID) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !IsFCE2BSupportedProvider(provider) {
		return "", ErrFCE2BTemplateProviderUnsupported
	}
	return fmt.Sprintf(
		"/usr/local/libexec/multica-fc-runner --runtime-id %s --provider %s --health-port %d",
		util.UUIDToString(runtimeID),
		provider,
		fcE2BHealthPortForTask(taskID),
	), nil
}

func (l *ASBLauncher) extraEnvForTask(ctx context.Context, task db.AgentTaskQueue, runtime db.AgentRuntime, sandboxID string) (map[string]string, error) {
	if l.Common == nil {
		return nil, errors.New("ASB launcher common runtime services are unavailable")
	}
	return l.Common.extraEnvForTaskWithModel(
		ctx,
		task,
		runtime,
		sandboxID,
		l.Config.ModelForAgent,
	)
}

func (l *ASBLauncher) lockRuntimeShared(ctx context.Context, runtimeID pgtype.UUID) (*pgxpool.Conn, func(), error) {
	if l.Common == nil {
		return nil, nil, errors.New("ASB runtime coordination is unavailable")
	}
	common := *l.Common
	common.Pool = l.Pool
	return common.lockRuntimeShared(ctx, runtimeID)
}

func (l *ASBLauncher) lockSandboxScopeOnConnection(
	ctx context.Context,
	runtime db.AgentRuntime,
	scope fcE2BTaskScope,
	existing *pgxpool.Conn,
) (func(), error) {
	if l.Common == nil {
		return nil, errors.New("ASB sandbox coordination is unavailable")
	}
	common := *l.Common
	common.Pool = l.Pool
	return common.lockSandboxScopeOnConnection(ctx, runtime, scope, existing)
}

func (l *ASBLauncher) waitForRunOnceClaim(ctx context.Context, task db.AgentTaskQueue) (fcE2BRunnerClaimState, error) {
	if l.Common == nil {
		return "", errors.New("ASB task claim verification is unavailable")
	}
	common := *l.Common
	common.Queries = l.Queries
	return common.waitForRunOnceClaim(ctx, task)
}

func (l *ASBLauncher) failLaunch(ctx context.Context, task db.AgentTaskQueue, message string) error {
	if cause := context.Cause(ctx); errors.Is(cause, errRuntimeLaunchLeaseLost) {
		return cause
	}
	_, err := l.Tasks.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, message)
	if err != nil {
		return err
	}
	return fmt.Errorf("ASB launch failed: %s", redact.Text(message))
}

func (l *ASBLauncher) VerifyStableArtifact(ctx context.Context, artifact ASBArtifact) (map[string]any, error) {
	if l == nil || l.Client == nil {
		return nil, errors.New("ASB launcher is unavailable")
	}
	if err := validateASBArtifact(artifact); err != nil {
		return nil, err
	}
	sandbox, err := l.Client.CreateSandbox(ctx, ASBCreateSandboxInput{
		ImageURI:       artifact.Ref,
		TimeoutSeconds: l.Config.TimeoutSeconds,
		ResourceCPU:    l.Config.ResourceCPU,
		ResourceMemory: l.Config.ResourceMemory,
		Entrypoint:     []string{"sleep infinity"},
		Metadata: map[string]string{
			"multica.release_validation": "true",
			"multica.backend":            string(SandboxBackendASB),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create ASB release validation sandbox: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if deleteErr := l.Client.DeleteSandbox(cleanupCtx, sandbox.ID); deleteErr != nil {
			slog.Warn("failed to delete ASB release validation sandbox",
				"sandbox_id", sandbox.ID,
				"error", deleteErr,
			)
		}
	}()
	if err := waitForASBSandboxRunning(ctx, l.Client, sandbox.ID, l.Config.ReadyTimeout); err != nil {
		return nil, err
	}
	endpoint, err := l.Client.GetEndpoint(ctx, sandbox.ID, asbExecPort)
	if err != nil {
		return nil, fmt.Errorf("resolve ASB validation command endpoint: %w", err)
	}
	smoke, err := l.Client.Exec(ctx, endpoint, ASBExecInput{
		Command: "/usr/local/bin/runtime-smoke-test",
		CWD:     "/home/user",
		Timeout: 5 * time.Minute,
		Envs: map[string]string{
			"HOME":    asbRunnerHome,
			"USER":    "user",
			"LOGNAME": "user",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ASB runtime-smoke-test failed: %w", err)
	}
	if smoke.ExitCode == nil || *smoke.ExitCode != 0 || smoke.ErrorName != "" {
		return nil, errors.New("ASB runtime-smoke-test failed")
	}
	manifestResult, err := l.Client.Exec(ctx, endpoint, ASBExecInput{
		Command: "/bin/cat /usr/local/share/multica/runtime-manifest.json",
		CWD:     "/",
		Timeout: 15 * time.Second,
		Envs: map[string]string{
			"HOME":    asbRunnerHome,
			"USER":    "user",
			"LOGNAME": "user",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("read ASB runtime manifest: %w", err)
	}
	if manifestResult.ExitCode == nil || *manifestResult.ExitCode != 0 {
		return nil, errors.New("read ASB runtime manifest failed")
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(manifestResult.Stdout)), &manifest); err != nil {
		return nil, errors.New("decode ASB runtime manifest")
	}
	if err := validateASBRuntimeManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (l *ASBLauncher) UpdateRuntimeArtifact(
	ctx context.Context,
	runtimeID pgtype.UUID,
	artifact ASBArtifact,
) (ASBRuntimeArtifactUpdateResult, error) {
	return l.updateRuntimeArtifact(ctx, runtimeID, artifact, map[string]any{
		"artifact_channel": CloudSandboxChannelCandidate,
	})
}

func (l *ASBLauncher) UpdateRuntimeArtifactForStableRelease(
	ctx context.Context,
	runtimeID pgtype.UUID,
	artifact ASBArtifact,
	releaseID string,
	batchIndex int,
) (ASBRuntimeArtifactUpdateResult, error) {
	return l.updateRuntimeArtifact(ctx, runtimeID, artifact, map[string]any{
		"artifact_channel":  CloudSandboxChannelStable,
		"stable_release_id": strings.TrimSpace(releaseID),
		"stable_batch":      batchIndex,
	})
}

func (l *ASBLauncher) updateRuntimeArtifact(
	ctx context.Context,
	runtimeID pgtype.UUID,
	artifact ASBArtifact,
	managedMetadata map[string]any,
) (ASBRuntimeArtifactUpdateResult, error) {
	if l == nil || l.Queries == nil || l.Pool == nil {
		return ASBRuntimeArtifactUpdateResult{}, errors.New("ASB runtime coordination requires a database pool")
	}
	if err := validateASBArtifact(artifact); err != nil {
		return ASBRuntimeArtifactUpdateResult{}, err
	}
	if err := validateASBRuntimeManifest(artifact.Manifest); err != nil {
		return ASBRuntimeArtifactUpdateResult{}, err
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("acquire connection for ASB artifact update: %w", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("begin ASB artifact update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", fcE2BRuntimeLockClass, fcE2BRuntimeLockKey(runtimeID)); err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("acquire ASB runtime write lock: %w", err)
	}
	qtx := l.Queries.WithTx(tx)
	runtime, err := qtx.LockAgentRuntime(ctx, runtimeID)
	if err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("lock ASB runtime row: %w", err)
	}
	if !IsASBRuntime(runtime) {
		return ASBRuntimeArtifactUpdateResult{}, ErrCloudSandboxRuntimeRequired
	}
	var metadata map[string]any
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil || metadata == nil {
		return ASBRuntimeArtifactUpdateResult{}, errors.New("decode ASB runtime metadata")
	}
	result := ASBRuntimeArtifactUpdateResult{
		Runtime:                 runtime,
		PreviousArtifactRef:     strings.TrimSpace(stringMetadataValue(metadata, "artifact_ref")),
		PreviousArtifactBuildID: strings.TrimSpace(stringMetadataValue(metadata, "artifact_build_id")),
		PreviousArtifactDigest:  strings.TrimSpace(stringMetadataValue(metadata, "artifact_digest")),
	}
	managedChanged := false
	for key, value := range managedMetadata {
		if fmt.Sprint(metadata[key]) != fmt.Sprint(value) {
			managedChanged = true
			break
		}
	}
	if result.PreviousArtifactRef == artifact.Ref &&
		result.PreviousArtifactBuildID == artifact.BuildID &&
		result.PreviousArtifactDigest == artifact.Digest &&
		!managedChanged {
		if err := tx.Commit(ctx); err != nil {
			return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("commit idempotent ASB artifact update: %w", err)
		}
		return result, nil
	}
	metadata["kind"] = CloudSandboxMetadataKind
	metadata["sandbox_backend"] = string(SandboxBackendASB)
	metadata["artifact_kind"] = CloudSandboxArtifactOCIImage
	metadata["artifact_ref"] = artifact.Ref
	metadata["artifact_build_id"] = artifact.BuildID
	metadata["artifact_alias"] = artifact.Alias
	metadata["artifact_digest"] = artifact.Digest
	metadata["artifact_status"] = "READY"
	metadata["manifest_version"] = intMetadataValue(artifact.Manifest, "schema_version")
	metadata["capabilities"] = manifestStringSliceForBackend(artifact.Manifest, "capabilities_by_backend", "asb")
	metadata["component_versions"] = artifact.Manifest["component_versions"]
	metadata["runner_protocol"] = stringMetadataValue(artifact.Manifest, "runner_protocol")
	metadata["runner"] = FCE2BRunnerCommandForProvider(runtime.Provider)
	for key, value := range managedMetadata {
		metadata[key] = value
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return ASBRuntimeArtifactUpdateResult{}, errors.New("encode ASB runtime metadata")
	}
	updated, err := qtx.UpdateFCE2BRuntimeMetadata(ctx, db.UpdateFCE2BRuntimeMetadataParams{
		ID:       runtimeID,
		Metadata: encoded,
	})
	if err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("update ASB runtime metadata: %w", err)
	}
	invalidated, err := qtx.MarkCloudSandboxSessionsStaleByRuntimeAndBackend(
		ctx,
		db.MarkCloudSandboxSessionsStaleByRuntimeAndBackendParams{
			RuntimeID:      runtimeID,
			SandboxBackend: string(SandboxBackendASB),
		},
	)
	if err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("invalidate ASB sandbox sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("commit ASB artifact update: %w", err)
	}
	result.Runtime = updated
	result.InvalidatedSandboxCount = invalidated
	result.Changed = true
	return result, nil
}

func validateASBArtifact(artifact ASBArtifact) error {
	artifact.Ref = strings.TrimSpace(artifact.Ref)
	artifact.BuildID = strings.TrimSpace(artifact.BuildID)
	artifact.Digest = strings.TrimSpace(artifact.Digest)
	if !cloudSandboxOCIDigestPattern.MatchString(artifact.Ref) ||
		artifact.BuildID == "" ||
		!isSHA256Digest(artifact.Digest) ||
		!strings.HasSuffix(artifact.Ref, "@"+artifact.Digest) {
		return errors.New("ASB artifact must use a matching immutable OCI ref, build ID, and sha256 digest")
	}
	return nil
}

func validateASBRuntimeManifest(manifest map[string]any) error {
	if intMetadataValue(manifest, "schema_version") != 3 ||
		!containsAllStrings(stringSliceMetadataValue(manifest, "sandbox_backends"), "asb") ||
		!containsAllStrings(stringSliceMetadataValue(manifest, "providers"), "hermes", "opencode", "pi") ||
		!containsAllStrings(
			manifestStringSliceForBackend(manifest, "capabilities_by_backend", "asb"),
			"dws", "mcp", "a1", "mw", "buc",
		) ||
		!containsAllStrings(
			manifestStringSliceForBackend(manifest, "identity_modes_by_backend", "asb"),
			"agent_identity", "spiffe", "buc_wireguard",
		) ||
		stringMetadataValue(manifest, "runner_protocol") != string(fcE2BRunnerLaunchRootLog) {
		return errors.New("ASB runtime manifest does not satisfy the enterprise sandbox contract")
	}
	return nil
}

func stringMetadataValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func intMetadataValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	default:
		return 0
	}
}

func stringSliceMetadataValue(values map[string]any, key string) []string {
	raw, _ := values[key].([]any)
	result := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	if typed, ok := values[key].([]string); ok {
		return normalizeCloudSandboxCapabilities(typed)
	}
	return result
}

func manifestStringSliceForBackend(manifest map[string]any, key, backend string) []string {
	byBackend, _ := manifest[key].(map[string]any)
	if byBackend == nil {
		if typed, ok := manifest[key].(map[string][]string); ok {
			return normalizeCloudSandboxCapabilities(typed[backend])
		}
		return nil
	}
	return stringSliceMetadataValue(byBackend, backend)
}

func containsAllStrings(values []string, expected ...string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range expected {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

// Keep protocol import pinned to the launcher's environment allow-list. This
// compile-time reference also makes accidental removal during refactors fail
// loudly instead of silently dropping the relay credential.
var _ = protocol.SandboxRelayTokenEnvKey
