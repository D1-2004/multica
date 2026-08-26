package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	defaultASBTimeoutSeconds         = asbMaxCreateTimeout
	defaultASBReadyTimeout           = 6 * time.Minute
	defaultASBCommandReadyTimeout    = 7 * time.Minute
	defaultASBWireGuardReadyTimeout  = 4 * time.Minute
	defaultASBWireGuardProbeInterval = 5 * time.Second
	defaultASBResourceCPU            = "2"
	defaultASBResourceMemory         = "4Gi"
	asbOptionalIdentityAttachTimeout = 30 * time.Second
	asbCommandReadyRetryInterval     = time.Second
	asbCommandProbeTimeout           = 5 * time.Second
	asbRunnerHome                    = "/home/user"
	asbUnboundIdentityFingerprint    = "7a5d3e85306c71a48596592e43c235dc5f8c9e3f96eeb52b9e6637a04356b1da"
)

type asbIdentityMode string

const (
	asbIdentityModeBound   asbIdentityMode = "bound"
	asbIdentityModeUnbound asbIdentityMode = "unbound"
)

// ASBConfig is the deployment-owned configuration for the Aone Sandbox
// backend. Tenant API keys are Runtime-owned encrypted credentials.
type ASBConfig struct {
	Enabled               bool
	APIURL                string
	ServerURL             string
	LLMBaseURL            string
	LLMAPIKey             string
	LLMModels             []string
	TimeoutSeconds        int
	ReadyTimeout          time.Duration
	CommandReadyTimeout   time.Duration
	WireGuardReadyTimeout time.Duration
	ResourceCPU           string
	ResourceMemory        string
	WireGuardCredentials  string
	ParseError            error
}

func ASBConfigFromEnv() ASBConfig {
	cfg := ASBConfig{
		Enabled:               envBool("MULTICA_ASB_ENABLED"),
		APIURL:                strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_API_URL")), "/"),
		ServerURL:             strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_SERVER_URL")), "/"),
		LLMBaseURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:             strings.TrimSpace(os.Getenv("MULTICA_ASB_OPENAI_API_KEY")),
		TimeoutSeconds:        defaultASBTimeoutSeconds,
		ReadyTimeout:          defaultASBReadyTimeout,
		CommandReadyTimeout:   defaultASBCommandReadyTimeout,
		WireGuardReadyTimeout: defaultASBWireGuardReadyTimeout,
		ResourceCPU:           firstNonEmptyString(os.Getenv("MULTICA_ASB_RESOURCE_CPU"), defaultASBResourceCPU),
		ResourceMemory:        firstNonEmptyString(os.Getenv("MULTICA_ASB_RESOURCE_MEMORY"), defaultASBResourceMemory),
		WireGuardCredentials:  strings.TrimSpace(os.Getenv("MULTICA_ASB_WG_CLIENT_CREDENTIALS")),
	}
	models, err := parseStringListEnv("MULTICA_ASB_OPENAI_MODELS", os.Getenv("MULTICA_ASB_OPENAI_MODELS"))
	if err != nil {
		cfg.ParseError = errors.Join(cfg.ParseError, err)
	} else {
		cfg.LLMModels = models
	}
	parsePositiveIntEnv("MULTICA_ASB_TIMEOUT_SECONDS", &cfg.TimeoutSeconds, &cfg.ParseError)
	parsePositiveDurationEnv("MULTICA_ASB_READY_TIMEOUT", &cfg.ReadyTimeout, &cfg.ParseError)
	parsePositiveDurationEnv(
		"MULTICA_ASB_COMMAND_READY_TIMEOUT",
		&cfg.CommandReadyTimeout,
		&cfg.ParseError,
	)
	parsePositiveDurationEnv(
		"MULTICA_ASB_WIREGUARD_READY_TIMEOUT",
		&cfg.WireGuardReadyTimeout,
		&cfg.ParseError,
	)
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
	if c.ReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_READY_TIMEOUT")
	}
	if c.CommandReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_COMMAND_READY_TIMEOUT")
	}
	if c.WireGuardReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_WIREGUARD_READY_TIMEOUT")
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

// ASBResolvedIdentity contains the short-lived AIT needed for one launch,
// BUC OAuth tokens used to attach WireGuard identity to a new sandbox, and
// stable non-secret identity coordinates.
type ASBResolvedIdentity struct {
	Mode               asbIdentityMode
	RawEmployeeID      string
	BUCAgentID         string
	AgentSPIFFEID      string
	AIPID              string
	SourceSandboxID    string
	SourceRuntimeID    pgtype.UUID
	AgentIdentityToken string
	BUCTokens          BUCIdentityTokens
	Fingerprint        string
}

func (identity ASBResolvedIdentity) validate() error {
	switch identity.Mode {
	case asbIdentityModeBound:
		for _, value := range []string{
			identity.RawEmployeeID,
			identity.BUCAgentID,
			identity.AgentSPIFFEID,
			identity.AIPID,
			identity.AgentIdentityToken,
			identity.Fingerprint,
			identity.BUCTokens.AccessToken,
			identity.BUCTokens.RefreshToken,
			identity.BUCTokens.IDToken,
		} {
			if strings.TrimSpace(value) == "" {
				return errors.New("Agent enterprise identity is incomplete")
			}
		}
		return nil
	case asbIdentityModeUnbound:
		if identity.Fingerprint != asbUnboundIdentityFingerprint ||
			identity.RawEmployeeID != "" ||
			identity.BUCAgentID != "" ||
			identity.AgentSPIFFEID != "" ||
			identity.AIPID != "" ||
			identity.SourceSandboxID != "" ||
			identity.SourceRuntimeID.Valid ||
			identity.AgentIdentityToken != "" ||
			identity.BUCTokens.AccessToken != "" ||
			identity.BUCTokens.RefreshToken != "" ||
			identity.BUCTokens.IDToken != "" {
			return errors.New("unbound ASB identity is invalid")
		}
		return nil
	default:
		return errors.New("ASB identity mode is invalid")
	}
}

func (identity ASBResolvedIdentity) sandboxExtensions(
	wireGuardCredentials string,
) map[string]string {
	if identity.Mode == asbIdentityModeUnbound {
		return nil
	}
	_ = wireGuardCredentials
	// New task sandboxes attach a fresh BUC token trio after they are Running.
	// lazyAuth keeps wgclient from starting until attachWireguardIdentity runs.
	// SPIFFE remains a runtime attachment because its task-scoped token is issued
	// immediately before launch.
	return map[string]string{
		"spiffe.lazyAuth":    "true",
		"wireguard.lazyAuth": "true",
	}
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

// BuildASBCandidateBootstrapMetadata records an immutable candidate image and
// its Runtime-scoped credential without running the full native ASB smoke test
// on the request path. Candidate Runtimes stay offline and exist only to give
// the asynchronous stable-release validator a credential. The stable release
// still reads and validates the image's real manifest before it can publish.
func BuildASBCandidateBootstrapMetadata(
	artifact ASBArtifact,
	provider string,
) (map[string]any, error) {
	if err := validateASBArtifact(artifact); err != nil {
		return nil, err
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = FCE2BProvider
	}
	if !IsFCE2BSupportedProvider(provider) {
		return nil, ErrFCE2BTemplateProviderUnsupported
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
		"artifact_channel":   CloudSandboxChannelCandidate,
		"artifact_ref":       artifact.Ref,
		"artifact_build_id":  artifact.BuildID,
		"artifact_alias":     alias,
		"artifact_digest":    artifact.Digest,
		"artifact_status":    "PENDING_STABLE_VALIDATION",
		"manifest_version":   0,
		"capabilities":       []string{},
		"component_versions": map[string]string{},
		"runner_protocol":    string(fcE2BRunnerLaunchRootLog),
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

type ASBRuntimeCredentialUpdateResult struct {
	APIKeyHint              string
	InvalidatedSandboxCount int64
	Quotas                  []ASBSandboxQuota
}

var ErrASBRuntimeHasActiveTaskSandboxes = errors.New(
	"ASB Runtime has active task sandboxes; wait for tasks to finish before updating the API key",
)

type ASBTaskIdentityResolver interface {
	ResolveASBTaskIdentity(
		context.Context,
		pgtype.UUID,
		pgtype.UUID,
		pgtype.UUID,
	) (ASBResolvedIdentity, error)
}

type ASBLauncher struct {
	Queries        *db.Queries
	Tasks          *TaskService
	Common         *FCE2BLauncher
	Config         ASBConfig
	ConfigProvider func() ASBConfig
	Client         *ASBClient
	Identity       ASBTaskIdentityResolver
	Credentials    *ASBRuntimeClientProvider
	Capacity       ASBSandboxCapacity
	CapacityWait   *asbCapacityWaitCoordinator
	Pool           *pgxpool.Pool
}

func (l *ASBLauncher) withCurrentConfig() *ASBLauncher {
	if l == nil || l.ConfigProvider == nil {
		return l
	}
	configured := *l
	configured.Config = l.ConfigProvider()
	configured.ConfigProvider = nil
	configured.Common = l.Common.withCurrentConfig()
	if configured.Credentials != nil {
		credentials := *configured.Credentials
		credentials.Config = configured.Config
		credentials.ConfigProvider = nil
		configured.Credentials = &credentials
		if capacity, ok := configured.Capacity.(*ASBSandboxCapacityManager); ok {
			capacityCopy := *capacity
			capacityCopy.Credentials = configured.Credentials
			configured.Capacity = &capacityCopy
		}
	}
	return &configured
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
	identity ASBTaskIdentityResolver,
	credentials *ASBRuntimeClientProvider,
) *ASBLauncher {
	return &ASBLauncher{
		Queries:      queries,
		Tasks:        tasks,
		Common:       common,
		Config:       cfg,
		Identity:     identity,
		Credentials:  credentials,
		CapacityWait: newASBCapacityWaitCoordinator(),
	}
}

func (l *ASBLauncher) SetPool(pool *pgxpool.Pool) {
	if l != nil {
		l.Pool = pool
		if pool == nil || l.Credentials == nil {
			l.Capacity = nil
			return
		}
		l.Capacity = &ASBSandboxCapacityManager{
			Pool:        pool,
			Credentials: l.Credentials,
		}
	}
}

func (l *ASBLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.LaunchTask(ctx, task)
	}
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
		failure := ClassifyRuntimeStartFailure(SandboxBackendASB, "invalid task trace: "+traceErr.Error())
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	chattrace.LogStage(slog.Default(), trace, "asb_launch", "started", "task_id", taskID, "runtime_id", runtimeID)
	if !l.Config.Enabled {
		failure := ClassifyRuntimeStartFailure(SandboxBackendASB, "ASB runtime is disabled")
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	if err := l.Config.Validate(); err != nil {
		failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	if l.Credentials == nil {
		failure := ClassifyRuntimeStartFailure(SandboxBackendASB, "ASB Runtime API key is not configured")
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	if l.Pool == nil {
		failure := ClassifyRuntimeStartFailure(SandboxBackendASB, "ASB sandbox coordination requires a database pool")
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	attempt, err := l.Tasks.BeginRuntimeStartAttempt(
		ctx,
		task,
		SandboxBackendASB,
		runtimeStartProtocolForRuntime(runtime),
	)
	if err != nil {
		if errors.Is(err, errRuntimeLaunchLeaseLost) {
			return err
		}
		failure := NewRuntimeStartFailure(
			SandboxBackendASB,
			"ASB-ATTEMPT-CREATE-FAILED",
			"launch_started",
			false,
			"无法创建 Runtime 启动记录。",
			err.Error(),
		)
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}

	runtimeLockConn, releaseRuntimeLock, err := l.lockRuntimeShared(ctx, task.RuntimeID)
	if err != nil {
		failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
		failure.Phase = "runtime_lock"
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			releaseRuntimeLock()
		}
	}()

	locked := *l
	locked.Queries = db.New(runtimeLockConn)
	locked.Tasks = &TaskService{Queries: locked.Queries}
	lockedCredentials := *locked.Credentials
	lockedCredentials.Store = locked.Queries
	locked.Credentials = &lockedCredentials
	identity, err := locked.resolveTaskIdentityForTask(ctx, task, runtime.WorkspaceID)
	if err != nil {
		releaseRuntimeLock()
		lockHeld = false
		failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
		failure.Phase = "identity_resolve"
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	client, err := locked.Credentials.ClientForRuntime(ctx, task.RuntimeID)
	if err != nil {
		releaseRuntimeLock()
		lockHeld = false
		failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
		failure.Phase = "credential_resolve"
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	locked.Client = client
	if locked.Common != nil {
		common := *locked.Common
		common.Queries = locked.Queries
		common.Tasks = locked.Tasks
		locked.Common = &common
	}
	submission, deferred, err := locked.submitTaskUnderRuntimeLock(ctx, task, runtimeLockConn, identity, trace, attempt)
	releaseRuntimeLock()
	lockHeld = false
	if err != nil {
		if errors.Is(err, ErrASBCapacityUnavailable) && !requiresA2ADEAPDWSToken(task.Context) {
			waiting, waitErr := l.Tasks.MarkRuntimeStartCapacityWaiting(ctx, attempt)
			if waitErr != nil {
				if errors.Is(waitErr, errRuntimeLaunchLeaseLost) {
					return waitErr
				}
				failure := NewRuntimeStartFailure(
					SandboxBackendASB,
					"ASB-CAPACITY-WAIT-RECORD-FAILED",
					asbCapacityWaitingStage,
					true,
					"沙箱容量排队状态记录失败。",
					waitErr.Error(),
				)
				return l.failLaunch(ctx, task, attempt.ID, failure)
			}
			if !waiting {
				return nil
			}
			slog.Info("ASB task queued while tenant sandbox capacity is busy",
				"task_id", taskID,
				"runtime_id", runtimeID,
				"runtime_start_attempt_id", util.UUIDToString(attempt.ID),
			)
			chattrace.LogStage(slog.Default(), trace, "asb_capacity_wait", "queued",
				"task_id", taskID,
				"runtime_id", runtimeID,
			)
			return nil
		}
		failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
		failure = runtimeStartFailureAtLastStage(ctx, l.Queries, attempt, failure)
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	if deferred {
		if err := l.Tasks.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
			failure := NewRuntimeStartFailure(
				SandboxBackendASB,
				"ASB-BLOCKED-ATTEMPT-RECORD-FAILED",
				"task_serialization",
				true,
				"Runtime 启动排队状态记录失败。",
				err.Error(),
			)
			return l.failLaunch(ctx, task, attempt.ID, failure)
		}
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
	claimState, err := l.waitForRunOnceClaim(ctx, task, attempt)
	if err != nil {
		if errors.Is(err, errRuntimeLaunchLeaseLost) {
			return err
		}
		failure := ClassifyRuntimeStartError(SandboxBackendASB, err)
		failure = runtimeStartFailureAtLastStage(ctx, l.Queries, attempt, failure)
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	switch claimState {
	case fcE2BRunnerClaimObserved:
	case fcE2BRunnerClaimBlocked:
		if err := l.Tasks.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
			failure := NewRuntimeStartFailure(
				SandboxBackendASB,
				"ASB-BLOCKED-ATTEMPT-RECORD-FAILED",
				"task_serialization",
				true,
				"Runtime 启动排队状态记录失败。",
				err.Error(),
			)
			return l.failLaunch(ctx, task, attempt.ID, failure)
		}
	case fcE2BRunnerClaimFailed:
		return errors.New("ASB launch failed: Runtime reported a startup failure")
	case fcE2BRunnerClaimStalled:
		detail := fmt.Sprintf("ASB runner did not claim task within %s after sandbox exec", fcE2BRunnerClaimTimeout)
		failure := ClassifyRuntimeStartFailure(SandboxBackendASB, detail)
		if current, lookupErr := l.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
			ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
		}); lookupErr == nil && current.LastStage != "" {
			failure.Phase = current.LastStage
			failure.InternalDetail = detail + "; last_stage=" + current.LastStage
		}
		return l.failLaunch(ctx, task, attempt.ID, failure)
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

func (l *ASBLauncher) resolveTaskIdentity(
	ctx context.Context,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	runtimeID pgtype.UUID,
) (ASBResolvedIdentity, error) {
	if l.Identity == nil {
		return unboundASBResolvedIdentity(), nil
	}
	identity, err := l.Identity.ResolveASBTaskIdentity(
		ctx,
		workspaceID,
		agentID,
		runtimeID,
	)
	if err != nil {
		slog.Warn(
			"ASB employee identity is unavailable; starting task without employee identity",
			"workspace_id", util.UUIDToString(workspaceID),
			"agent_id", util.UUIDToString(agentID),
			"runtime_id", util.UUIDToString(runtimeID),
			"needs_reauthorization", errors.Is(err, ErrEnterpriseIdentityNeedsReauth),
			"error", redact.Text(err.Error()),
		)
		return unboundASBResolvedIdentity(), nil
	}
	if err := identity.validate(); err != nil {
		slog.Warn(
			"ASB employee identity is invalid; starting task without employee identity",
			"workspace_id", util.UUIDToString(workspaceID),
			"agent_id", util.UUIDToString(agentID),
			"runtime_id", util.UUIDToString(runtimeID),
			"error", redact.Text(err.Error()),
		)
		return unboundASBResolvedIdentity(), nil
	}
	return identity, nil
}

func (l *ASBLauncher) resolveTaskIdentityForTask(
	ctx context.Context,
	task db.AgentTaskQueue,
	workspaceID pgtype.UUID,
) (ASBResolvedIdentity, error) {
	if IsA2ATaskOrigin(task.Context) && !requiresA2ADEAPDWSToken(task.Context) {
		// An ordinary external A2A caller may use only the identity explicitly
		// supplied for this turn. DEAP DWS invocations are the deliberate hybrid
		// case: extraEnvForTask and the Runtime wrapper keep DWS bound to the
		// request-scoped DEAP token, while the separately resolved enterprise
		// identity supplies non-DWS corporate access such as BUC/A1/Aliway.
		return unboundASBResolvedIdentity(), nil
	}
	return l.resolveTaskIdentity(ctx, workspaceID, task.AgentID, task.RuntimeID)
}

func unboundASBResolvedIdentity() ASBResolvedIdentity {
	return ASBResolvedIdentity{
		Mode:        asbIdentityModeUnbound,
		Fingerprint: asbUnboundIdentityFingerprint,
	}
}

func (l *ASBLauncher) submitTaskUnderRuntimeLock(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtimeLockConn *pgxpool.Conn,
	identity ASBResolvedIdentity,
	trace chattrace.Trace,
	attempt db.AgentTaskRuntimeStartAttempt,
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
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "runtime_validated"); err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB Runtime validation stage: %w", err)
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
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "sandbox_resolving"); err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB sandbox stage: %w", err)
	}
	sandboxID, coldStart, effectiveIdentity, err := l.resolveSandbox(
		ctx,
		runtime,
		metadata,
		scope,
		scoped,
		task.AgentID,
		task.ID,
		identity,
		runtimeLockConn,
		trace,
	)
	if err != nil {
		return asbLaunchSubmission{}, false, err
	}
	attempt, err = l.Tasks.UpdateRuntimeStartSandbox(ctx, attempt, sandboxID, coldStart, "sandbox_ready")
	if err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB sandbox ready stage: %w", err)
	}
	identity = effectiveIdentity
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "task_environment_preparing"); err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB task environment stage: %w", err)
	}
	extraEnv, err := l.extraEnvForTask(ctx, task, runtime, sandboxID)
	if err != nil {
		return asbLaunchSubmission{}, false, err
	}
	extraEnv = hardenCloudSandboxA2ARunnerEnv(task, runtime, extraEnv)
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "daemon_token_preparing"); err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB daemon token stage: %w", err)
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
	if attempt.Protocol == RuntimeStartProtocolHTTPJSONV1 {
		if extraEnv == nil {
			extraEnv = make(map[string]string)
		}
		extraEnv["MULTICA_RUNTIME_START_ATTEMPT_ID"] = util.UUIDToString(attempt.ID)
		extraEnv["MULTICA_RUNTIME_START_PROTOCOL"] = attempt.Protocol
	}
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "runner_exec_submitting"); err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB runner exec submission stage: %w", err)
	}
	if err := l.execRunOnce(ctx, sandboxID, runtime, task.ID, token, coldStart, extraEnv); err != nil {
		return asbLaunchSubmission{}, false, err
	}
	if err := l.Tasks.RecordRuntimeStartRunnerExecSubmitted(ctx, attempt.ID, task.ID, task.RuntimeID); err != nil {
		return asbLaunchSubmission{}, false, fmt.Errorf("record ASB runner exec stage: %w", err)
	}
	// Employee identity is an optional Runtime enhancement. Submit the runner
	// first, then ask ASB to attach SPIFFE identity once through its lifecycle
	// API. Do not run CLI probes here: the sandbox command endpoint is also used
	// by the runner and must never be held up by identity readiness.
	l.attachSandboxIdentityAfterTaskStart(runtime.ID, task.ID, sandboxID, identity)
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
	agentID pgtype.UUID,
	excludedTaskID pgtype.UUID,
	identity ASBResolvedIdentity,
	runtimeLockConn *pgxpool.Conn,
	trace chattrace.Trace,
) (string, bool, ASBResolvedIdentity, error) {
	if err := identity.validate(); err != nil {
		return "", false, ASBResolvedIdentity{}, err
	}
	if scoped {
		release, err := l.lockSandboxScopeOnConnection(ctx, runtime, scope, runtimeLockConn)
		if err != nil {
			return "", false, ASBResolvedIdentity{}, err
		}
		defer release()
		for {
			session, err := l.Queries.GetActiveCloudSandboxSession(ctx, db.GetActiveCloudSandboxSessionParams{
				RuntimeID:           runtime.ID,
				ScopeType:           scope.typ,
				ScopeID:             scope.id,
				SandboxBackend:      string(SandboxBackendASB),
				IdentityFingerprint: identity.Fingerprint,
				ArtifactRef:         metadata.ArtifactRef,
			})
			if err == pgx.ErrNoRows {
				break
			}
			if err != nil {
				return "", false, ASBResolvedIdentity{}, fmt.Errorf("load ASB sandbox session: %w", err)
			}
			reusable, state, err := inspectReusableASBSandbox(ctx, l.Client, session.SandboxID)
			if err != nil {
				return "", false, ASBResolvedIdentity{}, fmt.Errorf(
					"query reusable ASB sandbox: %w",
					err,
				)
			}
			if !reusable {
				slog.Info(
					"ASB sandbox session points to an unavailable sandbox; creating a replacement",
					"runtime_id", util.UUIDToString(runtime.ID),
					"sandbox_id", session.SandboxID,
					"scope_type", scope.typ,
					"scope_id", util.UUIDToString(scope.id),
					"sandbox_state", state,
				)
				break
			}
			if identity.Mode == asbIdentityModeBound {
				probeStarted := time.Now()
				if probeErr := probeASBTaskBUCIdentity(
					ctx,
					l.Client,
					session.SandboxID,
					identity.RawEmployeeID,
				); probeErr != nil {
					slog.Info(
						"ASB warm sandbox lost task employee identity; creating a replacement",
						"runtime_id", util.UUIDToString(runtime.ID),
						"sandbox_id", session.SandboxID,
						"scope_type", scope.typ,
						"scope_id", util.UUIDToString(scope.id),
						"probe_stage", asbEnterpriseCLIIdentityProbeFailureStage(probeErr),
						"duration_ms", time.Since(probeStarted).Milliseconds(),
					)
					break
				}
				slog.Info(
					"ASB warm sandbox task employee identity verified",
					"runtime_id", util.UUIDToString(runtime.ID),
					"sandbox_id", session.SandboxID,
					"duration_ms", time.Since(probeStarted).Milliseconds(),
				)
			}
			return session.SandboxID, false, identity, nil
		}
		if err := l.deleteSupersededASBSandboxForScope(
			ctx,
			runtime,
			scope,
			excludedTaskID,
			identity.Fingerprint,
		); err != nil {
			return "", false, ASBResolvedIdentity{}, err
		}
	}

	for {
		chattrace.LogStage(slog.Default(), trace, "asb_sandbox_create", "started",
			"artifact_ref", metadata.ArtifactRef,
			"identity_mode", string(identity.Mode),
		)
		sandboxMetadata := map[string]string{
			"multica.runtime_id": util.UUIDToString(runtime.ID),
			"multica.backend":    string(SandboxBackendASB),
		}
		if agentID.Valid {
			sandboxMetadata["multica.agent_id"] = util.UUIDToString(agentID)
		}
		if excludedTaskID.Valid {
			sandboxMetadata["multica.task_id"] = util.UUIDToString(excludedTaskID)
		}
		sandbox, err := createASBSandboxWithCapacityOnConnection(
			ctx,
			l.Queries,
			l.Credentials,
			l.Client,
			runtime.ID,
			excludedTaskID,
			runtimeLockConn,
			ASBCreateSandboxInput{
				ImageURI:       metadata.ArtifactRef,
				TimeoutSeconds: l.Config.TimeoutSeconds,
				ResourceCPU:    l.Config.ResourceCPU,
				ResourceMemory: l.Config.ResourceMemory,
				Entrypoint:     []string{"sleep infinity"},
				Metadata:       sandboxMetadata,
				Extensions:     identity.sandboxExtensions(l.Config.WireGuardCredentials),
			},
		)
		if err != nil {
			return "", true, ASBResolvedIdentity{}, err
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
				cleanupErr := l.deleteASBSandboxAfterIdentityFailure(
					runtime,
					scope,
					false,
					identity.Fingerprint,
					sandbox.ID,
				)
				if cleanupErr != nil {
					return "", true, ASBResolvedIdentity{}, errors.Join(
						fmt.Errorf("record ASB sandbox session: %w", err),
						cleanupErr,
					)
				}
				return "", true, ASBResolvedIdentity{}, fmt.Errorf("record ASB sandbox session: %w", err)
			}
		}
		if err := l.waitSandboxRunning(ctx, sandbox.ID); err != nil {
			if cleanupErr := l.deleteASBSandboxAfterIdentityFailure(
				runtime,
				scope,
				scoped,
				identity.Fingerprint,
				sandbox.ID,
			); cleanupErr != nil {
				return "", true, ASBResolvedIdentity{}, errors.Join(err, cleanupErr)
			}
			return "", true, ASBResolvedIdentity{}, err
		}
		if identity.Mode == asbIdentityModeBound {
			if err := l.attachAndProbeASBTaskBUCIdentity(
				ctx,
				sandbox.ID,
				identity,
				l.Config.WireGuardReadyTimeout,
			); err != nil {
				cleanupErr := l.deleteASBSandboxAfterIdentityFailure(
					runtime,
					scope,
					scoped,
					identity.Fingerprint,
					sandbox.ID,
				)
				userDetail := "ASB BUC identity attach did not become ready before task start."
				if errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
					userDetail = "ASB enterprise identity did not match the bound employee after attach. Reauthorize the Agent enterprise identity and retry."
				}
				identityErr := withRuntimeStartUserDetail(
					fmt.Errorf("attach ASB enterprise identity before task start: %w", err),
					userDetail,
				)
				if cleanupErr != nil {
					return "", true, ASBResolvedIdentity{}, errors.Join(identityErr, cleanupErr)
				}
				return "", true, ASBResolvedIdentity{}, identityErr
			}
		}
		chattrace.LogStage(slog.Default(), trace, "asb_sandbox_create", "ready",
			"sandbox_id", sandbox.ID,
			"identity_mode", string(identity.Mode),
		)
		return sandbox.ID, true, identity, nil
	}
}

func inspectReusableASBSandbox(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
) (bool, string, error) {
	live, exists, err := getLiveASBSandbox(ctx, client, sandboxID)
	if err != nil {
		return false, "", err
	}
	if !exists {
		return false, "not_found", nil
	}
	state := strings.TrimSpace(live.Status.State)
	reusable := !isTerminalASBSandboxState(state) && !strings.EqualFold(state, "failed")
	return reusable, state, nil
}

// deleteSupersededASBSandboxForScope removes the control-plane sandbox that
// belongs to the same logical scope but no longer matches the current
// artifact, identity, or expiry requirements. UpsertCloudSandboxSession uses
// one row per scope and identity; deleting first prevents that upsert from
// losing the only handle to a still-running ASB instance.
func (l *ASBLauncher) deleteSupersededASBSandboxForScope(
	ctx context.Context,
	runtime db.AgentRuntime,
	scope fcE2BTaskScope,
	excludedTaskID pgtype.UUID,
	identityFingerprint string,
) error {
	sessions, err := l.Queries.ListActiveCloudSandboxSessionsByRuntimeAndBackend(
		ctx,
		db.ListActiveCloudSandboxSessionsByRuntimeAndBackendParams{
			RuntimeID:      runtime.ID,
			SandboxBackend: string(SandboxBackendASB),
		},
	)
	if err != nil {
		return fmt.Errorf("list superseded ASB sandbox sessions: %w", err)
	}
	var superseded *db.FcE2bSandboxSession
	for index := range sessions {
		session := &sessions[index]
		if session.ScopeType == scope.typ &&
			session.ScopeID == scope.id &&
			session.IdentityFingerprint == identityFingerprint {
			superseded = session
			break
		}
	}
	if superseded == nil {
		return nil
	}
	runtimeIDs, err := l.Credentials.RuntimeIDsSharingAPIKey(ctx, runtime.ID)
	if err != nil {
		return fmt.Errorf("resolve ASB tenant scope for superseded sandbox: %w", err)
	}
	idleSessions, err := l.Queries.ListIdleASBSandboxSessionsByRuntimes(
		ctx,
		db.ListIdleASBSandboxSessionsByRuntimesParams{
			RuntimeIds:     runtimeIDs,
			ExcludedTaskID: excludedTaskID,
		},
	)
	if err != nil {
		return fmt.Errorf("check superseded ASB sandbox usage: %w", err)
	}
	idle := false
	for _, candidate := range idleSessions {
		if candidate.ID == superseded.ID &&
			candidate.SandboxID == superseded.SandboxID {
			idle = true
			break
		}
	}
	if !idle {
		return errors.New("superseded ASB sandbox is still processing a task")
	}
	live, exists, err := getLiveASBSandbox(ctx, l.Client, superseded.SandboxID)
	if err != nil {
		return fmt.Errorf("query superseded ASB sandbox: %w", err)
	}
	if exists && !isTerminalASBSandboxState(live.Status.State) {
		if err := l.Client.DeleteSandbox(ctx, superseded.SandboxID); err != nil {
			return fmt.Errorf("delete superseded ASB sandbox: %w", err)
		}
		if err := waitForASBCapacityRelease(ctx, l.Client, superseded.SandboxID); err != nil {
			return err
		}
	}
	if err := l.Queries.MarkCloudSandboxSessionStale(
		ctx,
		db.MarkCloudSandboxSessionStaleParams{
			RuntimeID:           superseded.RuntimeID,
			ScopeType:           superseded.ScopeType,
			ScopeID:             superseded.ScopeID,
			SandboxID:           superseded.SandboxID,
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: superseded.IdentityFingerprint,
		},
	); err != nil {
		return fmt.Errorf("mark superseded ASB sandbox stale: %w", err)
	}
	slog.Info(
		"deleted superseded ASB task sandbox before replacement",
		"runtime_id", util.UUIDToString(runtime.ID),
		"sandbox_id", superseded.SandboxID,
		"scope_type", superseded.ScopeType,
		"scope_id", util.UUIDToString(superseded.ScopeID),
	)
	return nil
}

func (l *ASBLauncher) deleteASBSandboxAfterIdentityFailure(
	runtime db.AgentRuntime,
	scope fcE2BTaskScope,
	scoped bool,
	identityFingerprint string,
	sandboxID string,
) error {
	cleanupCtx, cancel := context.WithTimeout(
		context.Background(),
		asbCapacityReleaseTimeout+15*time.Second,
	)
	defer cancel()
	if err := deleteASBSandboxIfExists(cleanupCtx, l.Client, sandboxID); err != nil {
		return fmt.Errorf("delete ASB sandbox after employee identity failure: %w", err)
	}
	if err := waitForASBCapacityRelease(cleanupCtx, l.Client, sandboxID); err != nil {
		return err
	}
	if !scoped {
		return nil
	}
	if err := l.Queries.MarkCloudSandboxSessionStale(
		cleanupCtx,
		db.MarkCloudSandboxSessionStaleParams{
			RuntimeID:           runtime.ID,
			ScopeType:           scope.typ,
			ScopeID:             scope.id,
			SandboxID:           sandboxID,
			SandboxBackend:      string(SandboxBackendASB),
			IdentityFingerprint: identityFingerprint,
		},
	); err != nil {
		return fmt.Errorf("mark deleted ASB sandbox session stale: %w", err)
	}
	return nil
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
				detail := fmt.Sprintf("ASB sandbox entered terminal state %q", sandbox.Status.State)
				if reason := strings.TrimSpace(sandbox.Status.Reason); reason != "" {
					detail += ": " + reason
				}
				return withRuntimeStartUserDetail(errors.New(detail), detail)
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

func (l *ASBLauncher) waitSandboxCommandReady(
	ctx context.Context,
	sandboxID string,
) (*ASBEndpoint, error) {
	timeout := l.Config.CommandReadyTimeout
	if timeout <= 0 {
		return nil, errors.New("ASB command ready timeout is not configured")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	retryTicker := time.NewTicker(asbCommandReadyRetryInterval)
	defer retryTicker.Stop()
	started := time.Now()
	attempts := 0
	var lastErr error
	for {
		attempts++
		endpoint, err := l.Client.GetEndpoint(ctx, sandboxID, asbExecPort)
		if err == nil {
			probeCtx, cancel := context.WithTimeout(ctx, asbCommandProbeTimeout)
			result, probeErr := l.Client.Exec(probeCtx, endpoint, ASBExecInput{
				Command:    "/usr/bin/true",
				CWD:        "/workspace",
				Background: true,
				Timeout:    asbCommandProbeTimeout,
			})
			cancel()
			if probeErr == nil && result.ErrorName == "" {
				slog.Info(
					"ASB command service is ready",
					"sandbox_id", sandboxID,
					"attempts", attempts,
					"duration_ms", time.Since(started).Milliseconds(),
				)
				return endpoint, nil
			}
			if probeErr != nil {
				err = probeErr
			} else {
				err = errors.New("ASB command readiness probe returned an error")
			}
		}
		lastErr = err
		if !isASBCommandServiceConverging(err) {
			return nil, fmt.Errorf("ASB command service readiness probe failed: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf(
				"ASB command service for sandbox %s was not ready within %s: %w",
				sandboxID,
				timeout,
				lastErr,
			)
		case <-retryTicker.C:
		}
	}
}

func isASBCommandServiceConverging(err error) bool {
	var httpErr *ASBHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	switch httpErr.StatusCode {
	case http.StatusConflict,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (l *ASBLauncher) attachAndProbeASBTaskBUCIdentity(
	ctx context.Context,
	sandboxID string,
	identity ASBResolvedIdentity,
	timeout time.Duration,
) error {
	if l == nil || l.Client == nil {
		return errors.New("ASB launcher is unavailable")
	}
	if timeout <= 0 {
		return errors.New("ASB WireGuard ready timeout is not configured")
	}
	identityCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := attachASBBUCIdentityOnce(identityCtx, l.Client, sandboxID, ASBBUCIdentityGrant{
		EmployeeID:           identity.RawEmployeeID,
		BUCAccessToken:       identity.BUCTokens.AccessToken,
		BUCRefreshToken:      identity.BUCTokens.RefreshToken,
		BUCIDToken:           identity.BUCTokens.IDToken,
		WireGuardCredentials: l.Config.WireGuardCredentials,
	}); err != nil {
		return fmt.Errorf("attach BUC identity to ASB task sandbox: %w", err)
	}
	if err := waitForASBSandboxState(
		identityCtx,
		l.Client,
		sandboxID,
		timeout,
		"running",
	); err != nil {
		return fmt.Errorf("wait for attached ASB task sandbox to return running: %w", err)
	}
	return l.waitSandboxTaskBUCIdentityReady(
		identityCtx,
		sandboxID,
		identity.RawEmployeeID,
		timeout,
	)
}

func (l *ASBLauncher) waitSandboxTaskBUCIdentityReady(
	ctx context.Context,
	sandboxID string,
	employeeID string,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("ASB WireGuard ready timeout is not configured")
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(asbIdentityProbeInterval(timeout))
	defer ticker.Stop()
	var lastErr error
	for {
		if err := probeASBTaskBUCIdentity(
			probeCtx,
			l.Client,
			sandboxID,
			employeeID,
		); err == nil {
			return nil
		} else if errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
			// Exit 42 means the sandbox already presented a different empId.
			return err
		} else if !errors.Is(err, context.DeadlineExceeded) || lastErr == nil {
			lastErr = err
		}
		select {
		case <-probeCtx.Done():
			if !errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
				return probeCtx.Err()
			}
			return fmt.Errorf(
				"ASB task BUC identity did not become ready within %s: %w",
				timeout,
				lastErr,
			)
		case <-ticker.C:
		}
	}
}

// Task sandboxes attach persisted BUC tokens directly. Their zero-trust agentId
// can differ from the configured BUC agentId, so the probe only requires the
// exact employee and a present agentId. Source establishment still verifies the
// configured BUC agentId exactly.
func probeASBTaskBUCIdentity(
	ctx context.Context,
	client *ASBClient,
	sandboxID string,
	employeeID string,
) error {
	endpoint, err := client.GetEndpoint(ctx, sandboxID, asbExecPort)
	if err != nil {
		if sandbox, stateErr := client.GetSandbox(ctx, sandboxID); stateErr == nil {
			return fmt.Errorf(
				"resolve ASB command endpoint for task BUC probe (sandbox_state=%s): %w",
				strings.ToLower(strings.TrimSpace(sandbox.Status.State)),
				err,
			)
		}
		return fmt.Errorf("resolve ASB command endpoint for task BUC probe: %w", err)
	}
	result, err := client.Exec(ctx, endpoint, ASBExecInput{
		Command: asbTaskBUCIdentityProbeCommand(),
		CWD:     "/",
		Timeout: 15 * time.Second,
		Envs: map[string]string{
			"HOME":            asbRunnerHome,
			"USER":            "user",
			"LOGNAME":         "user",
			"EXPECTED_EMP_ID": employeeID,
		},
	})
	if err != nil {
		return err
	}
	if result.ExitCode != nil && *result.ExitCode == asbIdentityInvalidExitCode {
		return fmt.Errorf(
			"%w: task sandbox BUC identity does not match the bound employee",
			ErrEnterpriseIdentityNeedsReauth,
		)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.ErrorName != "" {
		return &asbEnterpriseCLIIdentityProbeError{stage: "buc"}
	}
	return nil
}

func asbTaskBUCIdentityProbeCommand() string {
	return "set -euo pipefail; " +
		"printf 'probe_stage=buc\\n' >&2; " +
		"curl -fsS --max-time 10 -X POST " +
		"'https://login.alibaba-inc.com/rpc/cli/v1/get_zt_identity.json' | " +
		"/opt/task-python/bin/python -c '" +
		"import json,os,sys; p=json.load(sys.stdin); d=(p.get(\"content\") or {}).get(\"data\") or {}; " +
		"emp=str(d.get(\"empId\") or \"\").strip(); agent=str(d.get(\"agentId\") or \"\").strip(); " +
		"expected=os.environ[\"EXPECTED_EMP_ID\"].strip(); " +
		"raise SystemExit(42 if emp and emp!=expected else 0 if p.get(\"success\") is True and str(p.get(\"errorCode\"))==\"0\" and emp==expected and agent else 1)" +
		"'"
}

func (l *ASBLauncher) attachSandboxIdentityAfterTaskStart(
	runtimeID pgtype.UUID,
	taskID pgtype.UUID,
	sandboxID string,
	identity ASBResolvedIdentity,
) {
	if l == nil || l.Client == nil || identity.Mode != asbIdentityModeBound {
		return
	}
	grant := ASBAgentIdentityGrant{
		RawEmployeeID: identity.RawEmployeeID,
		AgentToken:    identity.AgentIdentityToken,
		AgentID:       identity.AgentSPIFFEID,
	}
	go func() {
		attachCtx, cancel := context.WithTimeout(
			context.Background(),
			asbOptionalIdentityAttachTimeout,
		)
		defer cancel()
		if err := l.Client.AttachAgentIdentity(attachCtx, sandboxID, grant); err != nil {
			logASBIdentityAttachmentFailure(sandboxID, err)
			slog.Warn(
				"ASB optional employee identity attachment failed after task start",
				"runtime_id", util.UUIDToString(runtimeID),
				"task_id", util.UUIDToString(taskID),
				"sandbox_id", sandboxID,
				"error", redact.Text(err.Error()),
			)
			return
		}
		slog.Info(
			"ASB optional employee identity attachment accepted after task start",
			"runtime_id", util.UUIDToString(runtimeID),
			"task_id", util.UUIDToString(taskID),
			"sandbox_id", sandboxID,
		)
	}()
}

func logASBIdentityAttachmentFailure(sandboxID string, err error) {
	var httpErr *ASBHTTPError
	if !errors.As(err, &httpErr) {
		return
	}
	slog.Warn(
		"ASB identity attachment rejected",
		"sandbox_id", sandboxID,
		"operation", httpErr.Operation,
		"http_status", httpErr.StatusCode,
		"request_id", httpErr.RequestID,
		"error_code", httpErr.ErrorCode,
		"error_message", redact.Text(httpErr.ErrorMessage),
	)
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
	endpoint, err := l.waitSandboxCommandReady(ctx, sandboxID)
	if err != nil {
		return err
	}
	// Background Runner lifetime is governed by task cancellation and the
	// sandbox lifecycle. Command readiness is bounded separately; startup
	// timeouts must not terminate an active task after it has started.
	result, err := l.Client.Exec(ctx, endpoint, ASBExecInput{
		Command:    command,
		CWD:        "/workspace",
		Background: true,
		Envs:       envs,
	})
	if err != nil {
		return fmt.Errorf("ASB runner exec failed: %w", err)
	}
	if result.ErrorName != "" {
		detail := "ASB runner exec returned an error: " + result.ErrorName
		return withRuntimeStartUserDetail(errors.New(detail), detail)
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

func (l *ASBLauncher) waitForRunOnceClaim(
	ctx context.Context,
	task db.AgentTaskQueue,
	attempt db.AgentTaskRuntimeStartAttempt,
) (fcE2BRunnerClaimState, error) {
	if l.Common == nil {
		return "", errors.New("ASB task claim verification is unavailable")
	}
	common := *l.Common
	common.Queries = l.Queries
	return common.waitForRunOnceClaim(ctx, task, attempt)
}

func (l *ASBLauncher) failLaunch(
	ctx context.Context,
	task db.AgentTaskQueue,
	attemptID pgtype.UUID,
	failure RuntimeStartFailure,
) error {
	if cause := context.Cause(ctx); errors.Is(cause, errRuntimeLaunchLeaseLost) {
		return cause
	}
	_, err := l.Tasks.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, attemptID, failure)
	if err != nil {
		return err
	}
	return fmt.Errorf("ASB launch failed: %s (%s)", failure.Code, failure.InternalDetail)
}

func (l *ASBLauncher) VerifyStableArtifact(
	ctx context.Context,
	runtimeID pgtype.UUID,
	artifact ASBArtifact,
) (map[string]any, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.VerifyStableArtifact(ctx, runtimeID, artifact)
	}
	if l == nil || l.Credentials == nil {
		return nil, errors.New("ASB Runtime credential service is unavailable")
	}
	if l.Capacity == nil {
		return nil, errors.New("ASB stable validation capacity coordination is unavailable")
	}
	client, err := l.Credentials.ClientForRuntime(ctx, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("resolve ASB stable validation Runtime credential: %w", err)
	}
	return l.verifyStableArtifact(
		ctx,
		client,
		artifact,
		func(ctx context.Context, input ASBCreateSandboxInput) (*ASBSandbox, error) {
			return l.Capacity.Create(ctx, runtimeID, client, input)
		},
	)
}

func (l *ASBLauncher) VerifyArtifactWithAPIKey(
	ctx context.Context,
	artifact ASBArtifact,
	apiKey string,
) (map[string]any, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.VerifyArtifactWithAPIKey(ctx, artifact, apiKey)
	}
	if l == nil {
		return nil, errors.New("ASB launcher is unavailable")
	}
	client, err := NewASBClientForAPIKey(l.Config, apiKey)
	if err != nil {
		return nil, err
	}
	return l.verifyStableArtifact(ctx, client, artifact, client.CreateSandbox)
}

func (l *ASBLauncher) verifyStableArtifact(
	ctx context.Context,
	client *ASBClient,
	artifact ASBArtifact,
	createSandbox func(context.Context, ASBCreateSandboxInput) (*ASBSandbox, error),
) (map[string]any, error) {
	if err := validateASBArtifact(artifact); err != nil {
		return nil, err
	}
	if createSandbox == nil {
		return nil, errors.New("ASB stable validation sandbox creator is unavailable")
	}
	sandbox, err := createSandbox(ctx, ASBCreateSandboxInput{
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
		if deleteErr := client.DeleteSandbox(cleanupCtx, sandbox.ID); deleteErr != nil {
			slog.Warn("failed to delete ASB release validation sandbox",
				"sandbox_id", sandbox.ID,
				"error", deleteErr,
			)
		}
	}()
	if err := waitForASBSandboxRunning(ctx, client, sandbox.ID, l.Config.ReadyTimeout); err != nil {
		return nil, err
	}
	endpoint, err := client.GetEndpoint(ctx, sandbox.ID, asbExecPort)
	if err != nil {
		return nil, fmt.Errorf("resolve ASB validation command endpoint: %w", err)
	}
	smoke, err := client.Exec(ctx, endpoint, ASBExecInput{
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
		return nil, fmt.Errorf(
			"ASB runtime-smoke-test failed: %s",
			asbExecFailureDetail(smoke),
		)
	}
	manifestResult, err := client.Exec(ctx, endpoint, ASBExecInput{
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
	if err := validateASBReleaseManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func asbExecFailureDetail(result *ASBExecResult) string {
	if result == nil {
		return "execution result is missing"
	}
	const maxOutputRunes = 1024
	parts := make([]string, 0, 5)
	if result.ExitCode == nil {
		parts = append(parts, "exit_code=missing")
	} else {
		parts = append(parts, fmt.Sprintf("exit_code=%d", *result.ExitCode))
	}
	if value := strings.TrimSpace(result.ErrorName); value != "" {
		parts = append(parts, "error_name="+value)
	}
	for _, output := range []struct {
		name  string
		value string
	}{
		{name: "stderr_tail", value: result.Stderr},
		{name: "stdout_tail", value: result.Stdout},
		{name: "result_tail", value: result.Result},
	} {
		value := strings.TrimSpace(redact.Text(output.value))
		if value == "" {
			continue
		}
		runes := []rune(value)
		if len(runes) > maxOutputRunes {
			value = "…" + string(runes[len(runes)-maxOutputRunes:])
		}
		parts = append(parts, output.name+"="+strconv.Quote(value))
	}
	return strings.Join(parts, " ")
}

func (l *ASBLauncher) UpdateRuntimeArtifact(
	ctx context.Context,
	runtimeID pgtype.UUID,
	artifact ASBArtifact,
) (ASBRuntimeArtifactUpdateResult, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.UpdateRuntimeArtifact(ctx, runtimeID, artifact)
	}
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
	if configured := l.withCurrentConfig(); configured != l {
		return configured.UpdateRuntimeArtifactForStableRelease(ctx, runtimeID, artifact, releaseID, batchIndex)
	}
	return l.updateRuntimeArtifact(ctx, runtimeID, artifact, map[string]any{
		"artifact_channel":  CloudSandboxChannelStable,
		"stable_release_id": strings.TrimSpace(releaseID),
		"stable_batch":      batchIndex,
	})
}

func (l *ASBLauncher) UpdateRuntimeAPIKey(
	ctx context.Context,
	runtimeID pgtype.UUID,
	apiKey string,
) (ASBRuntimeCredentialUpdateResult, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.UpdateRuntimeAPIKey(ctx, runtimeID, apiKey)
	}
	if l == nil || l.Queries == nil || l.Pool == nil || l.Credentials == nil {
		return ASBRuntimeCredentialUpdateResult{}, errors.New("ASB Runtime credential service is unavailable")
	}
	if err := ValidateASBAPIKey(apiKey); err != nil {
		return ASBRuntimeCredentialUpdateResult{}, err
	}
	// Validate before taking the Runtime write lock. The quota endpoint is
	// read-only and proves both key ownership and control-plane reachability
	// without consuming a sandbox slot.
	quotas, err := l.Credentials.ValidateAPIKeyAndGetQuotas(ctx, apiKey)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, err
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("acquire connection for ASB API key update: %w", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("begin ASB API key update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1, $2)",
		fcE2BRuntimeLockClass,
		fcE2BRuntimeLockKey(runtimeID),
	); err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("acquire ASB Runtime write lock: %w", err)
	}
	qtx := l.Queries.WithTx(tx)
	runtime, err := qtx.LockAgentRuntime(ctx, runtimeID)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("lock ASB Runtime row: %w", err)
	}
	if !IsASBRuntime(runtime) {
		return ASBRuntimeCredentialUpdateResult{}, ErrCloudSandboxRuntimeRequired
	}
	newClient, err := NewASBClientForAPIKey(l.Config, apiKey)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, err
	}
	capacityKeys := []int32{newClient.capacityLockKey}
	var cleanupClient *ASBClient
	sameCredential := false
	currentCredential, currentCredentialErr := qtx.GetASBRuntimeCredential(
		ctx,
		runtimeID,
	)
	if currentCredentialErr == nil {
		currentPlain, openErr := l.Credentials.Secrets.Open(currentCredential.ApiKeyEncrypted)
		if openErr != nil {
			return ASBRuntimeCredentialUpdateResult{}, errors.New("decrypt current ASB Runtime API key")
		}
		newPlain := []byte(strings.TrimSpace(apiKey))
		sameCredential = len(currentPlain) == len(newPlain) &&
			subtle.ConstantTimeCompare(currentPlain, newPlain) == 1
		clear(currentPlain)
		cleanupClient, err = l.Credentials.clientForCredential(currentCredential)
		if err != nil {
			return ASBRuntimeCredentialUpdateResult{}, err
		}
		capacityKeys = append(capacityKeys, cleanupClient.capacityLockKey)
	} else if !errors.Is(currentCredentialErr, pgx.ErrNoRows) {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
			"load current ASB Runtime API key: %w",
			currentCredentialErr,
		)
	}
	if err := lockASBTenantCapacityInTransaction(
		ctx,
		tx,
		capacityKeys...,
	); err != nil {
		return ASBRuntimeCredentialUpdateResult{}, err
	}
	if sameCredential {
		if err := tx.Commit(ctx); err != nil {
			return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
				"commit idempotent ASB API key update: %w",
				err,
			)
		}
		return ASBRuntimeCredentialUpdateResult{
			APIKeyHint: currentCredential.ApiKeyHint,
			Quotas:     quotas,
		}, nil
	}
	activeSessions, err := qtx.ListActiveCloudSandboxSessionsByRuntimeAndBackend(
		ctx,
		db.ListActiveCloudSandboxSessionsByRuntimeAndBackendParams{
			RuntimeID:      runtimeID,
			SandboxBackend: string(SandboxBackendASB),
		},
	)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("list ASB sandboxes before API key update: %w", err)
	}
	if len(activeSessions) > 0 {
		idleSessions, err := qtx.ListIdleASBSandboxSessionsByRuntimes(
			ctx,
			db.ListIdleASBSandboxSessionsByRuntimesParams{
				RuntimeIds:     []pgtype.UUID{runtimeID},
				ExcludedTaskID: pgtype.UUID{},
			},
		)
		if err != nil {
			return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
				"check ASB sandbox usage before API key update: %w",
				err,
			)
		}
		idleIDs := make(map[pgtype.UUID]struct{}, len(idleSessions))
		for _, session := range idleSessions {
			idleIDs[session.ID] = struct{}{}
		}
		for _, session := range activeSessions {
			if _, idle := idleIDs[session.ID]; !idle {
				return ASBRuntimeCredentialUpdateResult{}, ErrASBRuntimeHasActiveTaskSandboxes
			}
		}
	}
	activeIdentities, err := qtx.ListActiveAgentEnterpriseIdentitiesByRuntime(ctx, runtimeID)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
			"list ASB enterprise identity sources before API key update: %w",
			err,
		)
	}
	if len(activeSessions) > 0 || len(activeIdentities) > 0 {
		// Delete with the credential that owns the existing sandboxes before
		// replacing it. Live resources without a stored owner key are an
		// inconsistent state; never guess ownership from the submitted key.
		if cleanupClient == nil {
			return ASBRuntimeCredentialUpdateResult{}, errors.New(
				"ASB Runtime has live resources but no owning API key",
			)
		}
		for _, session := range activeSessions {
			if err := deleteASBSandboxIfExists(
				ctx,
				cleanupClient,
				session.SandboxID,
			); err != nil {
				return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
					"delete ASB sandbox before API key update: %w",
					err,
				)
			}
		}
		type identitySourceCoordinate struct {
			workspaceID pgtype.UUID
			runtimeID   pgtype.UUID
			sandboxID   string
		}
		affectedSourceReferences := make(
			map[identitySourceCoordinate]int64,
			len(activeIdentities),
		)
		for _, identity := range activeIdentities {
			if !identity.BucIdentitySourceSandboxID.Valid ||
				strings.TrimSpace(identity.BucIdentitySourceSandboxID.String) == "" ||
				!identity.BucIdentitySourceRuntimeID.Valid {
				continue
			}
			coordinate := identitySourceCoordinate{
				workspaceID: identity.WorkspaceID,
				runtimeID:   identity.BucIdentitySourceRuntimeID,
				sandboxID:   strings.TrimSpace(identity.BucIdentitySourceSandboxID.String),
			}
			affectedSourceReferences[coordinate]++
		}
		for coordinate, affectedReferences := range affectedSourceReferences {
			activeReferences, err := qtx.CountActiveAgentEnterpriseIdentitySourceReferences(
				ctx,
				db.CountActiveAgentEnterpriseIdentitySourceReferencesParams{
					WorkspaceID: coordinate.workspaceID,
					RuntimeID:   coordinate.runtimeID,
					SandboxID: pgtype.Text{
						String: coordinate.sandboxID,
						Valid:  true,
					},
				},
			)
			if err != nil {
				return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
					"count ASB enterprise identity source references before API key update: %w",
					err,
				)
			}
			if activeReferences > affectedReferences {
				continue
			}
			if err := deleteASBSandboxIfExists(
				ctx,
				cleanupClient,
				coordinate.sandboxID,
			); err != nil {
				return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
					"delete ASB enterprise identity source before API key update: %w",
					err,
				)
			}
		}
	}
	credential, err := l.Credentials.StoreRuntimeAPIKey(ctx, qtx, runtimeID, apiKey)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, err
	}
	invalidated, err := qtx.MarkCloudSandboxSessionsStaleByRuntimeAndBackend(
		ctx,
		db.MarkCloudSandboxSessionsStaleByRuntimeAndBackendParams{
			RuntimeID:      runtimeID,
			SandboxBackend: string(SandboxBackendASB),
		},
	)
	if err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("invalidate ASB sandbox sessions: %w", err)
	}
	if _, err := qtx.MarkAgentEnterpriseIdentitiesNeedsReauthByRuntime(
		ctx,
		runtimeID,
	); err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf(
			"invalidate ASB enterprise identity source after API key update: %w",
			err,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return ASBRuntimeCredentialUpdateResult{}, fmt.Errorf("commit ASB API key update: %w", err)
	}
	return ASBRuntimeCredentialUpdateResult{
		APIKeyHint:              credential.ApiKeyHint,
		InvalidatedSandboxCount: invalidated,
		Quotas:                  quotas,
	}, nil
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
	activateCandidate := needsCandidateASBArtifactActivation(runtime, metadata, managedMetadata)
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
		!managedChanged &&
		!activateCandidate {
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
	if activateCandidate {
		updated, err = qtx.MarkAgentRuntimeOnline(ctx, runtimeID)
		if err != nil {
			return ASBRuntimeArtifactUpdateResult{}, fmt.Errorf("activate validated ASB candidate runtime: %w", err)
		}
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

func needsCandidateASBArtifactActivation(
	runtime db.AgentRuntime,
	metadata map[string]any,
	managedMetadata map[string]any,
) bool {
	if strings.ToLower(strings.TrimSpace(stringMetadataValue(
		managedMetadata,
		"artifact_channel",
	))) != CloudSandboxChannelCandidate {
		return false
	}
	return runtime.Status != "online" ||
		strings.ToUpper(strings.TrimSpace(stringMetadataValue(metadata, "artifact_status"))) != "READY" ||
		intMetadataValue(metadata, "manifest_version") <= 0
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
	schemaVersion := intMetadataValue(manifest, "schema_version")
	if (schemaVersion < 3 || schemaVersion > 7) ||
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
	if schemaVersion == 7 &&
		(!containsAllStrings(stringSliceMetadataValue(manifest, "providers"), "dsh", "opencode-v2", "claude", "codex") ||
			!containsAllStrings(
				manifestStringSliceForBackend(manifest, "capabilities_by_backend", "asb"),
				DSHTrajectoryCapability,
			)) {
		return errors.New("ASB schema v7 runtime manifest does not satisfy the seven-runner contract")
	}
	return nil
}

// Existing schema-v3 through schema-v6 ASB images remain valid runtime bindings
// during a rolling backend deployment. A newly promoted five-runner release
// must use schema v7 and advertise every current enterprise capability.
func validateASBReleaseManifest(manifest map[string]any) error {
	if err := validateASBRuntimeManifest(manifest); err != nil {
		return err
	}
	if intMetadataValue(manifest, "schema_version") != 7 {
		return errors.New("ASB five-runner release manifest must use schema version 7")
	}
	if !containsAllStrings(
		manifestStringSliceForBackend(manifest, "capabilities_by_backend", string(SandboxBackendASB)),
		RuntimeStartCapabilityEventsV1,
		LLMTraceCapability,
		A2AInvocationV2Capability,
		DSHTrajectoryCapability,
	) {
		return errors.New("ASB release manifest does not advertise current runtime capabilities")
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
