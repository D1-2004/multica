package service

import (
	"context"
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
	defaultASBReadyTimeout           = 90 * time.Second
	defaultASBWireGuardReadyTimeout  = 4 * time.Minute
	defaultASBWireGuardProbeInterval = 5 * time.Second
	defaultASBResourceCPU            = "2"
	defaultASBResourceMemory         = "4Gi"
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
	Enabled                bool
	APIURL                 string
	ServerURL              string
	LLMBaseURL             string
	LLMAPIKey              string
	LLMModels              []string
	TimeoutSeconds         int
	ReadyTimeout           time.Duration
	WireGuardReadyTimeout  time.Duration
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
		ServerURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_SERVER_URL")), "/"),
		LLMBaseURL:             strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_ASB_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:              strings.TrimSpace(os.Getenv("MULTICA_ASB_OPENAI_API_KEY")),
		TimeoutSeconds:         defaultASBTimeoutSeconds,
		ReadyTimeout:           defaultASBReadyTimeout,
		WireGuardReadyTimeout:  defaultASBWireGuardReadyTimeout,
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
	parsePositiveDurationEnv("MULTICA_ASB_READY_TIMEOUT", &cfg.ReadyTimeout, &cfg.ParseError)
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
		{"MULTICA_ASB_IDENTITY_ANCHOR_IMAGE", c.IdentityAnchorImageRef},
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
	if c.WireGuardReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_ASB_WIREGUARD_READY_TIMEOUT")
	}
	if strings.TrimSpace(c.IdentityAnchorImageRef) != "" &&
		!cloudSandboxOCIDigestPattern.MatchString(c.IdentityAnchorImageRef) {
		return errors.New("MULTICA_ASB_IDENTITY_ANCHOR_IMAGE must use an immutable sha256 OCI digest")
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
	Mode               asbIdentityMode
	RawEmployeeID      string
	BUCAgentID         string
	AgentSPIFFEID      string
	AIPID              string
	SourceSandboxID    string
	SourceRuntimeID    pgtype.UUID
	AgentIdentityToken string
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
			identity.SourceSandboxID,
			identity.AgentIdentityToken,
			identity.Fingerprint,
		} {
			if strings.TrimSpace(value) == "" {
				return errors.New("Agent enterprise identity is incomplete")
			}
		}
		if !identity.SourceRuntimeID.Valid {
			return errors.New("Agent enterprise identity source Runtime is incomplete")
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
			identity.AgentIdentityToken != "" {
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
	// With originalSandboxID the ASB create-time contract inherits the source
	// credential directory and intentionally omits the expiring buc.* tokens.
	return map[string]string{
		"spiffe.lazyAuth":          "true",
		"wireguard.worker":         identity.RawEmployeeID,
		"wireguard.uemCredentials": wireGuardCredentials,
		"buc.originalSandboxID":    identity.SourceSandboxID,
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

type ASBTaskIdentityResolver interface {
	ResolveASBTaskIdentity(
		context.Context,
		pgtype.UUID,
		pgtype.UUID,
		pgtype.UUID,
	) (ASBResolvedIdentity, error)
	AcquireASBTaskIdentitySource(
		context.Context,
		pgtype.UUID,
		pgtype.UUID,
		pgtype.UUID,
		string,
	) (func(context.Context) error, error)
}

type ASBLauncher struct {
	Queries     *db.Queries
	Tasks       *TaskService
	Common      *FCE2BLauncher
	Config      ASBConfig
	Client      *ASBClient
	Identity    ASBTaskIdentityResolver
	Credentials *ASBRuntimeClientProvider
	Capacity    ASBSandboxCapacity
	Pool        *pgxpool.Pool
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
		Queries:     queries,
		Tasks:       tasks,
		Common:      common,
		Config:      cfg,
		Identity:    identity,
		Credentials: credentials,
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
	if l.Credentials == nil {
		return l.failLaunch(ctx, task, "ASB Runtime API key is not configured")
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
	lockedCredentials := *locked.Credentials
	lockedCredentials.Store = locked.Queries
	locked.Credentials = &lockedCredentials
	identity, err := locked.resolveTaskIdentity(
		ctx,
		runtime.WorkspaceID,
		task.AgentID,
		task.RuntimeID,
	)
	if err != nil {
		releaseRuntimeLock()
		lockHeld = false
		return l.failLaunch(ctx, task, err.Error())
	}
	client, err := locked.Credentials.ClientForRuntime(ctx, task.RuntimeID)
	if err != nil {
		releaseRuntimeLock()
		lockHeld = false
		return l.failLaunch(ctx, task, err.Error())
	}
	locked.Client = client
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
	identity = effectiveIdentity
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
			if identity.Mode != asbIdentityModeBound {
				return session.SandboxID, false, identity, nil
			}
			if readyErr := l.ensureSandboxIdentityReady(ctx, session.SandboxID, identity); readyErr == nil {
				return session.SandboxID, false, identity, nil
			} else {
				_ = l.Queries.MarkCloudSandboxSessionStale(ctx, db.MarkCloudSandboxSessionStaleParams{
					RuntimeID:           runtime.ID,
					ScopeType:           scope.typ,
					ScopeID:             scope.id,
					SandboxID:           session.SandboxID,
					SandboxBackend:      string(SandboxBackendASB),
					IdentityFingerprint: identity.Fingerprint,
				})
				slog.Warn(
					"ASB warm sandbox employee identity is unavailable; starting task without employee identity",
					"runtime_id", util.UUIDToString(runtime.ID),
					"sandbox_id", session.SandboxID,
					"error", redact.Text(readyErr.Error()),
				)
				l.deleteASBSandboxAfterIdentityFailure(session.SandboxID)
				identity = unboundASBResolvedIdentity()
			}
		}
	}

	for {
		chattrace.LogStage(slog.Default(), trace, "asb_sandbox_create", "started",
			"artifact_ref", metadata.ArtifactRef,
			"identity_mode", string(identity.Mode),
		)
		var releaseIdentitySource func(context.Context) error
		if identity.Mode == asbIdentityModeBound {
			if l.Identity == nil {
				identity = unboundASBResolvedIdentity()
				continue
			}
			var err error
			releaseIdentitySource, err = l.Identity.AcquireASBTaskIdentitySource(
				ctx,
				runtime.WorkspaceID,
				agentID,
				runtime.ID,
				identity.SourceSandboxID,
			)
			if err != nil {
				slog.Warn(
					"ASB employee identity source is unavailable; starting task without employee identity",
					"runtime_id", util.UUIDToString(runtime.ID),
					"source_sandbox_id", identity.SourceSandboxID,
					"error", redact.Text(err.Error()),
				)
				identity = unboundASBResolvedIdentity()
				continue
			}
		}

		releaseSource := func(stage string) {
			if releaseIdentitySource == nil {
				return
			}
			releaseCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := releaseIdentitySource(releaseCtx); err != nil {
				logASBIdentitySourceFailure(stage, identity.SourceSandboxID, err)
			}
			releaseIdentitySource = nil
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
				Metadata: map[string]string{
					"multica.runtime_id": util.UUIDToString(runtime.ID),
					"multica.backend":    string(SandboxBackendASB),
				},
				Extensions: identity.sandboxExtensions(l.Config.WireGuardCredentials),
			},
		)
		if err != nil {
			releaseSource("release_source_after_failed_task_create")
			return "", true, ASBResolvedIdentity{}, err
		}
		if err := l.waitSandboxRunning(ctx, sandbox.ID); err != nil {
			releaseSource("release_source_after_failed_wait_running")
			l.deleteASBSandboxAfterIdentityFailure(sandbox.ID)
			return "", true, ASBResolvedIdentity{}, err
		}
		if identity.Mode == asbIdentityModeBound {
			if err := l.ensureSandboxIdentityReady(ctx, sandbox.ID, identity); err != nil {
				slog.Warn(
					"ASB inherited employee identity is unavailable; retrying task without employee identity",
					"runtime_id", util.UUIDToString(runtime.ID),
					"sandbox_id", sandbox.ID,
					"error", redact.Text(err.Error()),
				)
				releaseSource("release_source_after_identity_unavailable")
				l.deleteASBSandboxAfterIdentityFailure(sandbox.ID)
				identity = unboundASBResolvedIdentity()
				continue
			}
			releaseSource("release_source_after_identity_ready")
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
				l.deleteASBSandboxAfterIdentityFailure(sandbox.ID)
				return "", true, ASBResolvedIdentity{}, fmt.Errorf("record ASB sandbox session: %w", err)
			}
		}
		chattrace.LogStage(slog.Default(), trace, "asb_sandbox_create", "ready",
			"sandbox_id", sandbox.ID,
			"identity_mode", string(identity.Mode),
		)
		return sandbox.ID, true, identity, nil
	}
}

func (l *ASBLauncher) deleteASBSandboxAfterIdentityFailure(sandboxID string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := l.Client.DeleteSandbox(cleanupCtx, sandboxID); err != nil {
		slog.Warn(
			"failed to delete ASB sandbox after employee identity failure",
			"sandbox_id", sandboxID,
			"error", redact.Text(err.Error()),
		)
	}
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
	// The enterprise CLI probe calls a1, which needs the SPIFFE auth headers
	// installed by AttachAgentIdentity. Attach them before probing the inherited
	// BUC credential directory to avoid a circular readiness dependency.
	if err := l.Client.AttachAgentIdentity(ctx, sandboxID, ASBAgentIdentityGrant{
		RawEmployeeID: identity.RawEmployeeID,
		AgentToken:    identity.AgentIdentityToken,
		AgentID:       identity.AgentSPIFFEID,
	}); err != nil {
		logASBIdentityAttachmentFailure(sandboxID, err)
		if !isASBAgentIdentityAttachmentConverging(err) {
			return fmt.Errorf("attach ASB Agent Identity: %w", err)
		}
		slog.Info(
			"ASB Agent Identity attachment is converging after asynchronous submission",
			"sandbox_id", sandboxID,
		)
	}
	if err := l.waitSandboxBUCIdentityReady(
		ctx,
		sandboxID,
		identity.RawEmployeeID,
		identity.BUCAgentID,
	); err != nil {
		return err
	}
	return nil
}

func isASBAgentIdentityAttachmentConverging(err error) bool {
	var httpErr *ASBHTTPError
	if !errors.As(err, &httpErr) ||
		httpErr.Operation != "attach_agent_identity" ||
		httpErr.StatusCode != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(httpErr.ErrorMessage))
	return strings.Contains(message, "failed to attach sandbox spiffe identity") &&
		strings.Contains(message, "got status code 502")
}

func (l *ASBLauncher) waitSandboxBUCIdentityReady(
	ctx context.Context,
	sandboxID string,
	employeeID string,
	bucAgentID string,
) error {
	timeout := l.Config.WireGuardReadyTimeout
	if timeout <= 0 {
		return errors.New("ASB WireGuard ready timeout is not configured")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(defaultASBWireGuardProbeInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := probeASBBUCIdentity(
			ctx,
			l.Client,
			sandboxID,
			employeeID,
			bucAgentID,
		); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf(
				"ASB inherited BUC identity did not become ready within %s: %w",
				timeout,
				lastErr,
			)
		case <-ticker.C:
		}
	}
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

func (l *ASBLauncher) VerifyStableArtifact(
	ctx context.Context,
	runtimeID pgtype.UUID,
	artifact ASBArtifact,
) (map[string]any, error) {
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
	if err := validateASBRuntimeManifest(manifest); err != nil {
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

func (l *ASBLauncher) UpdateRuntimeAPIKey(
	ctx context.Context,
	runtimeID pgtype.UUID,
	apiKey string,
) (ASBRuntimeCredentialUpdateResult, error) {
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
	currentCredential, currentCredentialErr := qtx.GetASBRuntimeCredential(
		ctx,
		runtimeID,
	)
	if currentCredentialErr == nil {
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
