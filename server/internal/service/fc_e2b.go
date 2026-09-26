package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/startupobs"
	"hash/fnv"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentitygithub"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/sandboxrelay"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/wsfs"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

const (
	FCE2BMetadataKind = "fc-e2b"
	// A2AInboundHermesCapability declares support for inbound A2A delivery
	// through the Hermes runtime provider.
	A2AInboundHermesCapability = "a2a_inbound_hermes_v1"
	// A2AInboundOpenCodeCapability declares support for inbound A2A delivery
	// through the OpenCode runtime provider.
	A2AInboundOpenCodeCapability = "a2a_inbound_opencode_v1"
	// A2AInboundPiCapability declares support for inbound A2A delivery through
	// the Pi runtime provider.
	A2AInboundPiCapability = "a2a_inbound_pi_v1"
	// A2AInvocationV2Capability requires the image and daemon to implement the
	// complete strict A2A execution contract.
	A2AInvocationV2Capability = "a2a-invocation-v2"
	// DSHTrajectoryCapability declares that a DSH runner persists its native
	// JSONL event ledger through the authenticated task trajectory endpoint.
	DSHTrajectoryCapability = "dsh_trajectory_v1"
	// FCE2BProvider is the first provider selected when the Diamond template
	// directory declares Hermes support and the request omits a provider.
	FCE2BProvider = "hermes"

	fcE2BScopeTypeChat  = "chat"
	fcE2BScopeTypeIssue = "issue"

	defaultFCE2BCLIPath = "e2b"
	// defaultFCE2BTimeoutSeconds is the FC/E2B sandbox lifetime used for both
	// create (--timeout) and the per-task POST /timeout renewal. Long agent
	// cells (clone + coding agent + PR + packaging) routinely need more than
	// one hour; the previous 3600s wall killed still-running tasks with
	// --lifecycle.ontimeout kill and no mid-task renewal. Config may raise
	// this, but values below 4800 are floored so Diamond leftover 3600 cannot
	// silently restore the old wall.
	defaultFCE2BTimeoutSeconds      = 4800
	defaultFCE2BSandboxReadyTimeout = 60 * time.Second
	defaultAgentIdentityTimeout     = 10 * time.Second
	fcE2BDaemonTokenTTL             = time.Hour
	fcE2BRunnerClaimTimeout         = 2 * time.Minute
	fcE2BRunnerClaimPollInterval    = 500 * time.Millisecond
	fcE2BSandboxCreateMaxAttempts   = 4
	fcE2BRunOnceHealthPortBase      = 20000
	// Keep daemon listeners below Linux's ephemeral range (32768-60999).
	// FC entrypoint connections can otherwise occupy a task's hashed port
	// before run-once starts, even when no other daemon is listening.
	fcE2BRunOnceHealthPortSpan  = 10000
	fcE2BRootRunnerInstallDir   = "/usr/local/libexec"
	fcE2BLegacyRunnerInstallDir = "/usr/local/bin"
	fcE2BChatSessionIDEnvKey    = "MULTICA_CHAT_SESSION_ID"
	fcE2BA2AIsolationRoot       = "/tmp/multica-dws"
)

var errAgentIdentityContextTokenRefreshRequired = errors.New("Agent Identity ContextToken refresh required")

type fcE2BRunnerLaunchMode string

const (
	fcE2BRunnerLaunchLegacyUser fcE2BRunnerLaunchMode = "legacy-user-v1"
	fcE2BRunnerLaunchRootLog    fcE2BRunnerLaunchMode = "root-log-v1"
)

type fcE2BRunnerLaunch struct {
	Mode    fcE2BRunnerLaunchMode
	Command string
	Home    string
}

type fcE2BRunnerClaimState string

const (
	fcE2BRunnerClaimObserved fcE2BRunnerClaimState = "observed"
	fcE2BRunnerClaimBlocked  fcE2BRunnerClaimState = "blocked"
	fcE2BRunnerClaimFailed   fcE2BRunnerClaimState = "failed"
	fcE2BRunnerClaimStalled  fcE2BRunnerClaimState = "stalled"
)

type FCE2BConfig struct {
	QuickWins RuntimeStartRecoveryConfig

	TaskModelResolver                 func(context.Context, string, string) (string, error)
	ModelResolver                     func(string) (string, error)
	Enabled                           bool
	Template                          string
	ServerURL                         string
	AppOrigin                         string
	APIKey                            string
	APIURL                            string
	Domain                            string
	LLMBaseURL                        string
	LLMAPIKey                         string
	LLMModels                         []string
	RuntimeProviderFingerprints       map[string][]string
	DWSMessagePolicyFingerprints      []string
	AgentIdentityControlBaseURL       string
	AgentIdentitySandboxBaseURL       string
	AgentIdentityBaseURL              string
	AgentIdentityTimeout              time.Duration
	AgentIdentityDebugLogContextToken bool
	AgentIdentityDebugContextAgentIDs []string
	DWSClientSecret                   string
	CLIPath                           string
	TimeoutSeconds                    int
	SandboxReadyTimeout               time.Duration
	ParseError                        error
}

func FCE2BConfigFromEnv() FCE2BConfig {
	legacyAgentIdentityBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_BASE_URL")), "/")
	controlAgentIdentityBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_CONTROL_BASE_URL")), "/")
	if controlAgentIdentityBaseURL == "" {
		controlAgentIdentityBaseURL = legacyAgentIdentityBaseURL
	}
	sandboxAgentIdentityBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_SANDBOX_BASE_URL")), "/")
	if sandboxAgentIdentityBaseURL == "" {
		sandboxAgentIdentityBaseURL = legacyAgentIdentityBaseURL
	}
	cfg := FCE2BConfig{
		Enabled:                           envBool("MULTICA_FC_E2B_ENABLED"),
		Template:                          strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TEMPLATE")),
		ServerURL:                         strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_SERVER_URL")), "/"),
		AppOrigin:                         strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/"),
		APIKey:                            strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_KEY")),
		APIURL:                            strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_URL")), "/"),
		Domain:                            strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_DOMAIN")),
		LLMBaseURL:                        strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:                         strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_API_KEY")),
		AgentIdentityControlBaseURL:       controlAgentIdentityBaseURL,
		AgentIdentitySandboxBaseURL:       sandboxAgentIdentityBaseURL,
		AgentIdentityBaseURL:              sandboxAgentIdentityBaseURL,
		AgentIdentityTimeout:              defaultAgentIdentityTimeout,
		AgentIdentityDebugLogContextToken: envBool("MULTICA_AGENT_IDENTITY_DEBUG_LOG_CONTEXT_TOKEN"),
		AgentIdentityDebugContextAgentIDs: parseCommaSeparatedEnv(os.Getenv("MULTICA_AGENT_IDENTITY_DEBUG_CONTEXT_TOKEN_AGENT_IDS")),
		DWSClientSecret:                   strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET")),
		CLIPath:                           strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_CLI_PATH")),
		TimeoutSeconds:                    defaultFCE2BTimeoutSeconds,
		SandboxReadyTimeout:               defaultFCE2BSandboxReadyTimeout,
	}
	models, err := parseFCE2BModels(os.Getenv("MULTICA_FC_E2B_OPENAI_MODELS"))
	if raw := strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_DWS_MESSAGE_POLICY_FINGERPRINTS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.DWSMessagePolicyFingerprints); err != nil {
			cfg.ParseError = errors.Join(cfg.ParseError, errors.New("invalid MULTICA_FC_E2B_DWS_MESSAGE_POLICY_FINGERPRINTS: expected a JSON string array"))
		} else {
			for _, fingerprint := range cfg.DWSMessagePolicyFingerprints {
				if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(fingerprint) {
					cfg.ParseError = errors.Join(cfg.ParseError, errors.New("invalid DWS message policy fingerprint"))
				}
			}
		}
	}
	if err != nil {
		cfg.ParseError = errors.Join(cfg.ParseError, err)
	} else {
		cfg.LLMModels = models
	}
	if cfg.CLIPath == "" {
		cfg.CLIPath = defaultFCE2BCLIPath
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TIMEOUT_SECONDS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			cfg.ParseError = errors.Join(cfg.ParseError, fmt.Errorf("invalid MULTICA_FC_E2B_TIMEOUT_SECONDS"))
		} else {
			cfg.TimeoutSeconds = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			cfg.ParseError = errors.Join(cfg.ParseError, fmt.Errorf("invalid MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT"))
		} else {
			cfg.SandboxReadyTimeout = d
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			cfg.ParseError = errors.Join(cfg.ParseError, fmt.Errorf("invalid MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS"))
		} else {
			cfg.AgentIdentityTimeout = time.Duration(n) * time.Second
		}
	}
	return cfg
}

func (c FCE2BConfig) agentIdentityControlBaseURL() string {
	if strings.TrimSpace(c.AgentIdentityControlBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.AgentIdentityControlBaseURL), "/")
	}
	return strings.TrimRight(strings.TrimSpace(c.AgentIdentityBaseURL), "/")
}

func (c FCE2BConfig) agentIdentitySandboxBaseURL() string {
	if strings.TrimSpace(c.AgentIdentitySandboxBaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.AgentIdentitySandboxBaseURL), "/")
	}
	return strings.TrimRight(strings.TrimSpace(c.AgentIdentityBaseURL), "/")
}

func envBool(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	return strings.EqualFold(v, "true") || v == "1"
}

func parseCommaSeparatedEnv(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func (c FCE2BConfig) shouldDebugLogAgentIdentityContext(agentID string) (bool, string) {
	if !c.AgentIdentityDebugLogContextToken {
		return false, "disabled"
	}
	if !agentIdentityContextDebugEnvironmentAllowed() {
		return false, "environment_not_allowed"
	}
	if len(c.AgentIdentityDebugContextAgentIDs) == 0 {
		return false, "agent_allowlist_empty"
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false, "agent_id_empty"
	}
	if !slices.Contains(c.AgentIdentityDebugContextAgentIDs, agentID) {
		return false, "agent_not_allowlisted"
	}
	return true, ""
}

func agentIdentityContextDebugEnvironmentAllowed() bool {
	for _, name := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "APP_ENV", "GO_ENV"} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		return agentIdentityContextDebugEnvironmentValueAllowed(raw)
	}
	return false
}

func agentIdentityContextDebugEnvironmentValueAllowed(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "prepub", "pre", "prepublish", "daily", "staging", "stage", "test", "testing", "local", "dev", "development":
		return true
	default:
		return false
	}
}

func (c FCE2BConfig) Validate() error {
	if c.ParseError != nil {
		return c.ParseError
	}
	var missing []string
	if strings.TrimSpace(c.ServerURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_SERVER_URL")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_KEY")
	}
	if strings.TrimSpace(c.APIURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_URL")
	}
	if strings.TrimSpace(c.Domain) == "" {
		missing = append(missing, "MULTICA_FC_E2B_DOMAIN")
	}
	if strings.TrimSpace(c.LLMBaseURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_BASE_URL")
	}
	if strings.TrimSpace(c.LLMAPIKey) == "" {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_API_KEY")
	}
	if len(c.LLMModels) == 0 {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_MODELS")
	}
	if strings.TrimSpace(c.CLIPath) == "" {
		missing = append(missing, "MULTICA_FC_E2B_CLI_PATH")
	}
	if c.TimeoutSeconds <= 0 {
		missing = append(missing, "MULTICA_FC_E2B_TIMEOUT_SECONDS")
	}
	if c.SandboxReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing FC/E2B config: %s", strings.Join(missing, ", "))
	}
	return nil
}

func parseFCE2BModels(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, errors.New("invalid MULTICA_FC_E2B_OPENAI_MODELS: expected a JSON string array")
	}
	if len(models) == 0 {
		return nil, errors.New("invalid MULTICA_FC_E2B_OPENAI_MODELS: array must not be empty")
	}
	seen := make(map[string]struct{}, len(models))
	for i, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, fmt.Errorf("invalid MULTICA_FC_E2B_OPENAI_MODELS: item %d is empty", i)
		}
		if _, ok := seen[model]; ok {
			return nil, fmt.Errorf("invalid MULTICA_FC_E2B_OPENAI_MODELS: duplicate model %q", model)
		}
		seen[model] = struct{}{}
		models[i] = model
	}
	return models, nil
}

func (c FCE2BConfig) ModelForAgent(model string) (string, error) {
	if c.ModelResolver != nil {
		return c.ModelResolver(model)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		if len(c.LLMModels) == 0 {
			return "", errors.New("MULTICA_FC_E2B_OPENAI_MODELS is empty")
		}
		return c.LLMModels[0], nil
	}
	for _, allowed := range c.LLMModels {
		if model == allowed {
			return model, nil
		}
	}
	return "", fmt.Errorf("agent model %q is not configured in MULTICA_FC_E2B_OPENAI_MODELS", model)
}

func (c FCE2BConfig) ValidateTemplateAPI() error {
	if c.ParseError != nil {
		return c.ParseError
	}
	var missing []string
	if strings.TrimSpace(c.APIKey) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_KEY")
	}
	if strings.TrimSpace(c.APIURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_URL")
	}
	if strings.TrimSpace(c.Domain) == "" {
		missing = append(missing, "MULTICA_FC_E2B_DOMAIN")
	}
	if strings.TrimSpace(c.CLIPath) == "" {
		missing = append(missing, "MULTICA_FC_E2B_CLI_PATH")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing FC/E2B template config: %s", strings.Join(missing, ", "))
	}
	return nil
}

func IsFCE2BRuntime(rt db.AgentRuntime) bool {
	metadata, err := ParseCloudSandboxRuntime(rt)
	return err == nil && metadata.SandboxBackend == SandboxBackendAliyunFC
}

// FCE2BSupportedProviders lists the agent providers an FC/E2B sandbox runtime
// can run. A template image may ship several of these CLIs side by side; the
// runtime's provider is chosen at creation time. FCE2BProvider (hermes) is
// the default when a request names none.
var FCE2BSupportedProviders = []string{FCE2BProvider, "opencode", "pi", "dsh", "opencode-v2", "claude", "codex"}

// IsFCE2BSupportedProvider reports whether provider can back an FC/E2B
// sandbox runtime.
func IsFCE2BSupportedProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, candidate := range FCE2BSupportedProviders {
		if candidate == provider {
			return true
		}
	}
	return false
}

func IsRuntimeSourceCommit(commit string) bool {
	commit = strings.ToLower(strings.TrimSpace(commit))
	return len(commit) == 40 && isLowerHex(commit)
}

func RuntimeProvidersForFingerprint(catalog map[string][]string, fingerprint string) ([]string, bool) {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	if !runtimeProviderFingerprintPattern.MatchString(fingerprint) {
		return nil, false
	}
	providers, found := catalog[fingerprint]
	if !found || len(providers) == 0 {
		return nil, false
	}
	return append([]string(nil), providers...), true
}

// FCE2BTemplateSupportsProvider reports whether provider is enabled in the
// deployment-wide Diamond provider list copied onto the template projection.
func FCE2BTemplateSupportsProvider(template FCE2BTemplate, provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !IsFCE2BSupportedProvider(provider) {
		return false
	}
	for _, candidate := range template.Providers {
		if strings.ToLower(strings.TrimSpace(candidate)) == provider {
			return true
		}
	}
	return false
}

// FCE2BProviderForTemplate returns the first server-supported provider from
// the Runtime provider catalog. A template without one is not usable for creation.
func FCE2BProviderForTemplate(template FCE2BTemplate) (string, bool) {
	for _, provider := range FCE2BSupportedProviders {
		if FCE2BTemplateSupportsProvider(template, provider) {
			return provider, true
		}
	}
	return "", false
}

// FCE2BRunnerCommandForProvider returns the protocol marker recorded for images
// that contain the fixed root log entrypoint. The stored value is never
// executed directly; the launcher maps an exact marker to a fixed path.
func FCE2BRunnerCommandForProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = FCE2BProvider
	}
	return "multica-fc-" + provider + "-container-log-entry"
}

func fcE2BLegacyRunnerCommandForProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = FCE2BProvider
	}
	return "multica-fc-" + provider + "-runner"
}

// FCE2BTemplateCapabilities returns the verified capabilities persisted on a
// runtime. The immutable runtime provider remains the first entry for existing
// capability consumers.
func FCE2BTemplateCapabilities(provider string, template FCE2BTemplate) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	capabilities := []string{provider}
	seen := map[string]struct{}{provider: {}}
	for _, capability := range template.Capabilities {
		capability = strings.ToLower(strings.TrimSpace(capability))
		if capability == "" {
			continue
		}
		switch capability {
		case A2AInboundHermesCapability:
			if provider != "hermes" {
				continue
			}
		case A2AInboundOpenCodeCapability:
			if !usesOpenCodeA2AInboundAdapter(provider) {
				continue
			}
		case A2AInboundPiCapability:
			if provider != "pi" {
				continue
			}
		case DSHTrajectoryCapability, DSHEmployeeHostCapability, DSHPluginBuildCapability:
			if provider != "dsh" {
				continue
			}
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

func ASBCapabilitiesForProviders(providers []string) []string {
	base := runtimeconfig.CapabilitiesForProviders(providers)
	if len(base) < 5 {
		return base
	}
	result := append([]string(nil), base[:5]...)
	result = append(result, "a1", "mw", "buc")
	return append(result, base[5:]...)
}

// DSH and OpenCode v2 intentionally speak the daemon's verified OpenCode
// JSON-event contract through their image-owned adapters. Their immutable
// runner entrypoints rewrite only the daemon provider argument while retaining
// the provider-specific executable, so they share the OpenCode inbound
// capability instead of inventing unimplemented protocol adapters.
func usesOpenCodeA2AInboundAdapter(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "opencode", "dsh", "opencode-v2":
		return true
	default:
		return false
	}
}

// IsFCE2BTemplateReady reports whether the template catalog has completed the
// build. Template rotation never submits a sandbox from a transitional state.
func IsFCE2BTemplateReady(template FCE2BTemplate) bool {
	return strings.EqualFold(strings.TrimSpace(template.Status), "ready")
}

// IsFCE2BTemplatePublished reports whether the template has an admitted alias
// and at least one provider from the Diamond provider catalog. Template ID is
// the only launch identity; manifest and component versions are not checked.
func IsFCE2BTemplatePublished(template FCE2BTemplate) bool {
	return strings.TrimSpace(template.ID) != "" &&
		strings.TrimSpace(template.Template) != "" &&
		len(template.Providers) > 0
}

func fcE2BRunnerLaunchForMode(provider string, mode fcE2BRunnerLaunchMode) (fcE2BRunnerLaunch, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !IsFCE2BSupportedProvider(provider) {
		return fcE2BRunnerLaunch{}, fmt.Errorf("unsupported FC/E2B runtime provider %q", provider)
	}
	switch mode {
	case fcE2BRunnerLaunchRootLog:
		return fcE2BRunnerLaunch{
			Mode:    mode,
			Command: fcE2BRootRunnerInstallDir + "/" + FCE2BRunnerCommandForProvider(provider),
			Home:    "/root",
		}, nil
	case fcE2BRunnerLaunchLegacyUser:
		if provider != "hermes" && provider != "opencode" {
			return fcE2BRunnerLaunch{}, fmt.Errorf("%s FC/E2B runtimes require the root-log-v1 runner protocol", provider)
		}
		return fcE2BRunnerLaunch{
			Mode:    mode,
			Command: fcE2BLegacyRunnerInstallDir + "/" + fcE2BLegacyRunnerCommandForProvider(provider),
			Home:    "/home/user",
		}, nil
	default:
		return fcE2BRunnerLaunch{}, fmt.Errorf("unsupported FC/E2B runner launch mode %q", mode)
	}
}

// FCE2BRuntimeProvider returns the agent provider recorded on an FC/E2B
// runtime row. Legacy rows created before providers were derived from the
// template carry the provider column already ("hermes"), so the fallback only
// covers rows with an empty column (test fixtures, pre-provider data).
func FCE2BRuntimeProvider(rt db.AgentRuntime) string {
	if provider := strings.ToLower(strings.TrimSpace(rt.Provider)); provider != "" {
		return provider
	}
	return FCE2BProvider
}

// fcE2BRunnerLaunchForRuntime maps the runtime's exact, server-written protocol
// marker to one of two fixed commands. Legacy images run their shell entrypoint
// as the sandbox user. New images start the root-owned log entrypoint as root so
// it can attach to FC PID 1 stdout before permanently dropping to uid 1000.
// Runtime metadata can select only a known protocol; it can never choose the
// executable path or inject command arguments.
func fcE2BRunnerLaunchForRuntime(rt db.AgentRuntime) (fcE2BRunnerLaunch, error) {
	provider := FCE2BRuntimeProvider(rt)
	if !IsFCE2BSupportedProvider(provider) {
		return fcE2BRunnerLaunch{}, fmt.Errorf("unsupported FC/E2B runtime provider %q", provider)
	}

	var metadata struct {
		Runner json.RawMessage `json:"runner"`
	}
	if len(rt.Metadata) > 0 {
		if err := json.Unmarshal(rt.Metadata, &metadata); err != nil {
			return fcE2BRunnerLaunch{}, errors.New("invalid FC/E2B runtime metadata")
		}
	}

	// Rows created before runner markers were introduced used the legacy user
	// entrypoint, so an absent marker is an explicit legacy protocol value.
	legacyMarker := fcE2BLegacyRunnerCommandForProvider(provider)
	rootMarker := FCE2BRunnerCommandForProvider(provider)
	if len(metadata.Runner) == 0 {
		return fcE2BRunnerLaunchForMode(provider, fcE2BRunnerLaunchLegacyUser)
	}
	if bytes.Equal(bytes.TrimSpace(metadata.Runner), []byte("null")) {
		return fcE2BRunnerLaunch{}, errors.New("invalid FC/E2B runner protocol marker")
	}
	var runnerMarker string
	if err := json.Unmarshal(metadata.Runner, &runnerMarker); err != nil || runnerMarker == "" {
		return fcE2BRunnerLaunch{}, errors.New("invalid FC/E2B runner protocol marker")
	}
	switch runnerMarker {
	case legacyMarker:
		return fcE2BRunnerLaunchForMode(provider, fcE2BRunnerLaunchLegacyUser)
	case rootMarker:
		return fcE2BRunnerLaunchForMode(provider, fcE2BRunnerLaunchRootLog)
	default:
		return fcE2BRunnerLaunch{}, fmt.Errorf("unsupported FC/E2B runner protocol %q for provider %q", runnerMarker, provider)
	}
}

// detectFCE2BRunnerLaunch validates the stored protocol marker, then selects
// the image-owned entrypoint that is actually executable in this sandbox. The
// capability checks run as the sandbox user and inspect only server-derived,
// fixed absolute paths. Metadata can neither provide a path nor make a command
// run as root.
func (l *FCE2BLauncher) detectFCE2BRunnerLaunch(ctx context.Context, sandboxID string, rt db.AgentRuntime) (fcE2BRunnerLaunch, error) {
	if l.Config.QuickWins.CoalescedHotExec {
		if receipt, ok := ctx.Value(hotRunnerProbeKey{}).(*hotRunnerProbe); ok && receipt.sandboxID == sandboxID && (receipt.launch.Command != "" || receipt.err != nil) {
			startupobs.Start(ctx, "fc_hot_probe_reused")(receipt.err)
			return receipt.launch, receipt.err
		}
	}
	hint, err := fcE2BRunnerLaunchForRuntime(rt)
	if err != nil {
		return fcE2BRunnerLaunch{}, err
	}

	provider := FCE2BRuntimeProvider(rt)
	rootLaunch, err := fcE2BRunnerLaunchForMode(provider, fcE2BRunnerLaunchRootLog)
	if err != nil {
		return fcE2BRunnerLaunch{}, err
	}
	candidates := []fcE2BRunnerLaunch{rootLaunch}
	legacyLaunch, legacyErr := fcE2BRunnerLaunchForMode(provider, fcE2BRunnerLaunchLegacyUser)
	if legacyErr == nil {
		candidates = append(candidates, legacyLaunch)
	}
	if len(candidates) == 2 && hint.Mode == fcE2BRunnerLaunchLegacyUser {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}

	probeErrors := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		_, err = l.runE2BCommand(ctx, []string{
			"sandbox", "exec",
			"--user", "user",
			sandboxID,
			"--",
			"/usr/bin/test", "-x", candidate.Command,
		})
		if err == nil {
			if candidate.Mode != hint.Mode {
				slog.Warn("FC/E2B runner protocol adjusted to sandbox capability",
					"sandbox_id", sandboxID,
					"provider", provider,
					"metadata_protocol", string(hint.Mode),
					"selected_protocol", string(candidate.Mode),
					"unavailable_error", probeErrors[0],
				)
			}
			return candidate, nil
		}
		probeErrors = append(probeErrors, err)
	}

	attrs := []any{
		"sandbox_id", sandboxID,
		"provider", provider,
		"first_probe_error", probeErrors[0],
	}
	if len(probeErrors) > 1 {
		attrs = append(attrs, "second_probe_error", probeErrors[1])
	}
	slog.Error("FC/E2B runner capability probe failed", attrs...)
	return fcE2BRunnerLaunch{}, fmt.Errorf("FC/E2B sandbox %s has no executable runner entrypoint for provider %s", sandboxID, provider)
}

func FCE2BRuntimeHasCapability(rt db.AgentRuntime, capability string) bool {
	return IsFCE2BRuntime(rt) && CloudSandboxRuntimeHasCapability(rt, capability)
}

// FCE2BRuntimeTemplateChannel treats rows created before stable-channel
// metadata existed as stable-managed.
func FCE2BRuntimeTemplateChannel(rt db.AgentRuntime) string {
	if !IsFCE2BRuntime(rt) {
		return ""
	}
	return CloudSandboxRuntimeChannel(rt)
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string) (string, error)
}

type FCE2BTemplate struct {
	ID              string   `json:"id,omitempty"`
	SourceRevision  string   `json:"source_revision,omitempty"`
	Name            string   `json:"name,omitempty"`
	Template        string   `json:"template"`
	Status          string   `json:"status,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	ManifestVersion int      `json:"manifest_version"`
	Providers       []string `json:"providers"`
	Capabilities    []string `json:"capabilities"`
	RunnerProtocol  string   `json:"runner_protocol"`
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if bounded, _ := ctx.Value(boundedExecKey{}).(bool); bounded {
		cmd.WaitDelay = 2 * time.Second
	}
	cmd.Env = append(os.Environ(), env...)
	var output, diagnostics bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("command failed: %w: %s", err, redact.Text(output.String()+diagnostics.String()))
	}
	return output.String(), nil
}

func ListFCE2BTemplates(ctx context.Context, cfg FCE2BConfig, runner CommandRunner) ([]FCE2BTemplate, error) {
	if runner == nil {
		runner = OSCommandRunner{}
	}
	if err := cfg.ValidateTemplateAPI(); err != nil {
		return nil, err
	}
	out, err := runner.Run(ctx, cfg.CLIPath, []string{"template", "list", "--format", "json"}, fcE2BEnv(cfg))
	if err != nil {
		return nil, fmt.Errorf("FC/E2B template list failed: %w", err)
	}
	templates, err := parseFCE2BTemplates(out, cfg.RuntimeProviderFingerprints, cfg.DWSMessagePolicyFingerprints)
	if err != nil {
		return nil, err
	}
	sort.Slice(templates, func(i, j int) bool {
		return templates[i].UpdatedAt > templates[j].UpdatedAt
	})
	return templates, nil
}

func parseFCE2BTemplates(
	output string,
	providerFingerprints map[string][]string,
	dwsMessagePolicyFingerprints ...[]string,
) ([]FCE2BTemplate, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, errors.New("FC/E2B template list returned empty output")
	}

	var raw any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("FC/E2B template list returned invalid JSON: %w", err)
	}

	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		for _, key := range []string{"templates", "items", "data"} {
			if arr, ok := v[key].([]any); ok {
				items = arr
				break
			}
		}
	default:
		return nil, errors.New("FC/E2B template list returned unexpected JSON")
	}
	if items == nil {
		return nil, errors.New("FC/E2B template list returned no templates")
	}

	templates := make([]FCE2BTemplate, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t := FCE2BTemplate{
			ID:           firstString(obj, "id", "template_id", "templateID"),
			Status:       firstString(obj, "status", "state", "buildStatus", "build_status"),
			CreatedAt:    firstString(obj, "created_at", "createdAt", "create_time", "createTime"),
			UpdatedAt:    firstString(obj, "updated_at", "updatedAt", "update_time", "updateTime"),
			Providers:    []string{},
			Capabilities: []string{},
		}
		if strings.TrimSpace(t.ID) == "" {
			continue
		}
		displayAlias := t.ID
		aliases := stringValues(obj, "aliases", "names")
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" && alias != "default" {
				displayAlias = alias
				break
			}
		}
		t.Name = displayAlias
		t.Template = displayAlias
		for _, alias := range aliases {
			matches := fcE2BTemplateProviderFingerprintAliasPattern.FindStringSubmatch(strings.TrimSpace(alias))
			if matches == nil {
				continue
			}
			providers, found := RuntimeProvidersForFingerprint(providerFingerprints, matches[1])
			if !found {
				continue
			}
			t.ManifestVersion = 7
			t.Providers = append([]string(nil), providers...)
			t.Capabilities = runtimeconfig.CapabilitiesForProviders(providers)
			// Provider support alone does not attest the new send hook. Only
			// exact fingerprints whose image-level canary has passed may opt in.
			if hasDWSMessagePolicyFingerprint(matches[1], dwsMessagePolicyFingerprints) {
				t.Capabilities = append(t.Capabilities, protocol.DWSMessagePolicyCapability)
			}
			break
		}
		t.RunnerProtocol = string(fcE2BRunnerLaunchRootLog)
		templates = append(templates, t)
	}
	return templates, nil
}

func hasDWSMessagePolicyFingerprint(fingerprint string, allowlists [][]string) bool {
	for _, allowlist := range allowlists {
		for _, candidate := range allowlist {
			if candidate == fingerprint {
				return true
			}
		}
	}
	return false
}

var fcE2BTemplateProviderFingerprintAliasPattern = regexp.MustCompile(`^multica-m7-v([0-9a-f]{16})-r1-[0-9a-f]{6}$`)
var runtimeProviderFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

func stringValues(obj map[string]any, keys ...string) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	appendValue := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	for _, key := range keys {
		switch value := obj[key].(type) {
		case string:
			appendValue(value)
		case []string:
			for _, item := range value {
				appendValue(item)
			}
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok {
					appendValue(text)
				}
			}
		}
	}
	return values
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := obj[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case fmt.Stringer:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		case []any:
			for _, item := range v {
				if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
					return s
				}
			}
		case []string:
			for _, item := range v {
				if s := strings.TrimSpace(item); s != "" {
					return s
				}
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

type FCE2BLauncher struct {
	ReadDSHProfileSource  dshprofile.ReadSource
	ProvisionDSHStorage   func(context.Context, dshhost.Database, dshhost.Key) (dshhost.Host, error)
	PrepareWorkspaceMount func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error)
	// ReadWorkspaceMount is the launch-time lookup. It must not provision NAS
	// or RAM. PrepareWorkspaceMount remains the explicit grant flow.
	ReadWorkspaceMount func(context.Context, wsfs.Database, uuid.UUID, uuid.UUID, *dshhost.Host) (wsfs.MountDecision, error)
	DSHArtifactSigner  interface {
		PresignGet(context.Context, string, time.Duration) (string, error)
	}
	Queries            *db.Queries
	Tasks              *TaskService
	ObjectStorage      storage.Storage
	Config             FCE2BConfig
	ConfigProvider     func() FCE2BConfig
	Runner             CommandRunner
	AgentIdentity      AgentIdentityContextCreator
	GitHubIdentity     AgentIdentityGithubBindingReader
	IdentityBindings   AgentIdentityBindingReader
	SandboxRelaySigner SandboxRelayTokenSigner
	sleep              func(context.Context, time.Duration) error
	jitter             func(time.Duration) time.Duration
	dshProvider        func(dshhost.Storage) (dshhost.Provider, error)

	// LLMTraceCaptureAlways turns on sandbox model request/response capture
	// for every task on a capable runtime image, independent of Router
	// telemetry or the Agent static sink. Set when the server exports LLM
	// traces itself (Langfuse).
	LLMTraceCaptureAlways bool

	// Pool backs the cross-replica runtime and sandbox advisory locks.
	Pool *pgxpool.Pool
}

// withCurrentConfig freezes one dynamic configuration snapshot for the whole
// operation. Long-running launches therefore cannot mix two Diamond versions
// halfway through sandbox creation.
func (l *FCE2BLauncher) withCurrentConfig() *FCE2BLauncher {
	if l == nil || l.ConfigProvider == nil {
		return l
	}
	configured := *l
	configured.Config = l.ConfigProvider()
	configured.ConfigProvider = nil
	return &configured
}

var ErrFCE2BRuntimeRequired = errors.New("runtime is not an FC/E2B cloud runtime")
var ErrFCE2BTemplateProviderUnsupported = errors.New("FC/E2B template does not support the runtime provider")

type FCE2BRuntimeTemplateUpdateResult struct {
	Runtime                 db.AgentRuntime
	PreviousTemplate        string
	PreviousTemplateID      string
	InvalidatedSandboxCount int64
	Changed                 bool
}

type AgentIdentityContextCreator interface {
	CreateContext(context.Context, agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error)
	ExtendContext(context.Context, agentidentityhsf.ExtendContextRequest) (agentidentityhsf.ExtendContextResult, error)
}

type AgentIdentityGithubBindingReader interface {
	GetStatus(context.Context, string, string, string) (agentidentitygithub.Connection, error)
}

type AgentIdentityBindingReader interface {
	GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
}

type SandboxRelayTokenSigner interface {
	Mint(sandboxrelay.MintRequest) (string, error)
}

// Advisory lock classes keep runtime rotation and per-scope sandbox creation
// separate from other process-wide coordination. The runtime lock must always
// be acquired before the scope lock.
const (
	fcE2BSandboxLockClass int32 = 0x46434532 // "FCE2"
	fcE2BRuntimeLockClass int32 = 0x46525432 // "FRT2"
)

// lockSandboxScope serializes the read-then-create in resolveSandbox across
// replicas. The launch is driven by whichever replica served the enqueue (or
// the pending-chat poll, which the load balancer spreads freely), and the
// in-process launch guard only dedups within one replica — so without this two
// replicas both miss the session row, both boot a sandbox, and the second
// Upsert clobbers the first, leaving an orphaned microVM billed until timeout.
//
// The lock is held across the sandbox boot (seconds), so it holds one pooled
// connection for that long. The loser blocks, then re-reads and reuses the
// winner's sandbox.
func (l *FCE2BLauncher) lockSandboxScope(ctx context.Context, rt db.AgentRuntime, scope fcE2BTaskScope) (func(), error) {
	return l.lockSandboxScopeOnConnection(ctx, rt, scope, nil)
}

func (l *FCE2BLauncher) lockSandboxScopeOnConnection(ctx context.Context, rt db.AgentRuntime, scope fcE2BTaskScope, existing *pgxpool.Conn) (func(), error) {
	if l == nil || l.Pool == nil {
		return nil, errors.New("FC/E2B sandbox coordination requires a database pool")
	}
	key := fcE2BScopeLockKey(rt.ID, scope)
	conn := existing
	owned := conn == nil
	if owned {
		var err error
		conn, err = l.Pool.Acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("acquire connection for sandbox lock: %w", err)
		}
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", fcE2BSandboxLockClass, key); err != nil {
		if owned {
			conn.Release()
		}
		return nil, fmt.Errorf("acquire sandbox lock: %w", err)
	}
	return func() {
		releaseFCE2BAdvisoryLock(conn, false, fcE2BSandboxLockClass, key, "sandbox")
		if owned {
			conn.Release()
		}
	}, nil
}

func (l *FCE2BLauncher) lockRuntimeShared(ctx context.Context, runtimeID pgtype.UUID) (*pgxpool.Conn, func(), error) {
	if l == nil || l.Pool == nil {
		return nil, nil, errors.New("FC/E2B runtime coordination requires a database pool")
	}
	key := fcE2BRuntimeLockKey(runtimeID)
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire connection for FC/E2B runtime lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock_shared($1, $2)", fcE2BRuntimeLockClass, key); err != nil {
		conn.Release()
		return nil, nil, fmt.Errorf("acquire FC/E2B runtime read lock: %w", err)
	}
	return conn, func() {
		releaseFCE2BAdvisoryLock(conn, true, fcE2BRuntimeLockClass, key, "runtime read")
		conn.Release()
	}, nil
}

func releaseFCE2BAdvisoryLock(conn *pgxpool.Conn, shared bool, class, key int32, name string) {
	if conn == nil {
		return
	}
	unlockFunction := "pg_advisory_unlock"
	if shared {
		unlockFunction = "pg_advisory_unlock_shared"
	}
	unlocked := false
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := conn.QueryRow(unlockCtx, "SELECT "+unlockFunction+"($1, $2)", class, key).Scan(&unlocked)
	if err == nil && unlocked {
		return
	}
	slog.Error("FC/E2B advisory lock release failed", "lock", name, "error", err, "unlocked", unlocked)
	_ = conn.Conn().Close(unlockCtx)
}

func fcE2BScopeLockKey(runtimeID pgtype.UUID, scope fcE2BTaskScope) int32 {
	h := fnv.New32a()
	_, _ = h.Write(runtimeID.Bytes[:])
	_, _ = io.WriteString(h, scope.typ)
	_, _ = h.Write(scope.id.Bytes[:])
	return int32(h.Sum32())
}

func fcE2BRuntimeLockKey(runtimeID pgtype.UUID) int32 {
	h := fnv.New32a()
	_, _ = h.Write(runtimeID.Bytes[:])
	return int32(h.Sum32())
}

type fcE2BTaskScope struct {
	typ string
	id  pgtype.UUID
}

func NewFCE2BLauncher(q *db.Queries, tasks *TaskService, cfg FCE2BConfig, runner CommandRunner) *FCE2BLauncher {
	if runner == nil {
		runner = OSCommandRunner{}
	}
	return &FCE2BLauncher{
		Queries:          q,
		Tasks:            tasks,
		Config:           cfg,
		Runner:           runner,
		AgentIdentity:    agentidentityhsf.NewClient(),
		IdentityBindings: q,
		sleep:            sleepWithContext,
		jitter:           runtimeStartRetryJitter,
	}
}

// SetPool wires the database pool required for cross-replica runtime rotation
// and sandbox creation coordination.
func (l *FCE2BLauncher) SetPool(pool *pgxpool.Pool) {
	if l != nil {
		l.Pool = pool
	}
}

func (l *FCE2BLauncher) SetObjectStorage(store storage.Storage) {
	if l != nil {
		l.ObjectStorage = store
	}
}

func (l *FCE2BLauncher) SetSandboxRelaySigner(signer *sandboxrelay.Signer) {
	if l == nil {
		return
	}
	if signer == nil {
		l.SandboxRelaySigner = nil
		return
	}
	l.SandboxRelaySigner = signer
}

// UpdateRuntimeTemplate atomically rotates an FC/E2B runtime to a catalogued,
// ready template and makes every previously reusable sandbox session stale.
// The exclusive runtime advisory lock conflicts with LaunchTask's shared lock,
// so the returned runtime is the cutover boundary for later launches.
func (l *FCE2BLauncher) UpdateRuntimeTemplate(ctx context.Context, runtimeID pgtype.UUID, selected FCE2BTemplate) (FCE2BRuntimeTemplateUpdateResult, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.UpdateRuntimeTemplate(ctx, runtimeID, selected)
	}
	return l.updateRuntimeTemplate(ctx, runtimeID, selected, nil)
}

// UpdateRuntimeTemplateForStableRelease uses the same atomic rotation boundary
// as an ordinary template update and records which stable release owns the
// resulting binding.
func (l *FCE2BLauncher) UpdateRuntimeTemplateForStableRelease(
	ctx context.Context,
	runtimeID pgtype.UUID,
	selected FCE2BTemplate,
	releaseID string,
	batchIndex int,
) (FCE2BRuntimeTemplateUpdateResult, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.UpdateRuntimeTemplateForStableRelease(ctx, runtimeID, selected, releaseID, batchIndex)
	}
	return l.updateRuntimeTemplate(ctx, runtimeID, selected, map[string]any{
		"template_channel":  "stable",
		"stable_release_id": strings.TrimSpace(releaseID),
		"stable_batch":      batchIndex,
	})
}

func (l *FCE2BLauncher) updateRuntimeTemplate(
	ctx context.Context,
	runtimeID pgtype.UUID,
	selected FCE2BTemplate,
	managedMetadata map[string]any,
) (FCE2BRuntimeTemplateUpdateResult, error) {
	if l == nil || l.Queries == nil || l.Pool == nil {
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B runtime coordination requires a database pool")
	}
	selected.ID = strings.TrimSpace(selected.ID)
	selected.Template = strings.TrimSpace(selected.Template)
	selected.Name = strings.TrimSpace(selected.Name)
	selected.Status = strings.TrimSpace(selected.Status)
	if selected.ID == "" {
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B template has no template ID")
	}
	if selected.Template == "" {
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B template has no launch template")
	}
	if !IsFCE2BTemplateReady(selected) {
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B template is not ready")
	}
	if !IsFCE2BTemplatePublished(selected) {
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B template is not present in the Runtime provider catalog")
	}

	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("acquire connection for FC/E2B template update: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("begin FC/E2B template update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", fcE2BRuntimeLockClass, fcE2BRuntimeLockKey(runtimeID)); err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("acquire FC/E2B runtime write lock: %w", err)
	}
	qtx := l.Queries.WithTx(tx)
	runtime, err := qtx.LockAgentRuntime(ctx, runtimeID)
	if err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("lock FC/E2B runtime row: %w", err)
	}
	if !IsFCE2BRuntime(runtime) {
		return FCE2BRuntimeTemplateUpdateResult{}, ErrFCE2BRuntimeRequired
	}
	provider := FCE2BRuntimeProvider(runtime)
	if !FCE2BTemplateSupportsProvider(selected, provider) {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("%w: %s", ErrFCE2BTemplateProviderUnsupported, provider)
	}

	var metadata map[string]any
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("decode FC/E2B runtime metadata: %w", err)
	}
	if metadata == nil {
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B runtime metadata is empty")
	}
	previousTemplate, _ := metadata["template"].(string)
	previousTemplateID, _ := metadata["template_id"].(string)
	result := FCE2BRuntimeTemplateUpdateResult{
		Runtime:            runtime,
		PreviousTemplate:   strings.TrimSpace(previousTemplate),
		PreviousTemplateID: strings.TrimSpace(previousTemplateID),
	}
	managedMetadataChanged := false
	for key, value := range managedMetadata {
		if metadata[key] != value {
			managedMetadataChanged = true
			break
		}
	}
	legacyArtifactMetadataChanged := false
	for key := range metadata {
		if strings.HasPrefix(key, "artifact_") {
			legacyArtifactMetadataChanged = true
			break
		}
	}
	if result.PreviousTemplateID == selected.ID &&
		!managedMetadataChanged &&
		!legacyArtifactMetadataChanged {
		if err := tx.Commit(ctx); err != nil {
			return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("commit idempotent FC/E2B template update: %w", err)
		}
		return result, nil
	}

	// FC/E2B templates are represented by the template_* fields. Older
	// generalized cloud-runtime migrations also wrote artifact_* aliases into
	// some FC/E2B rows. Keeping both copies lets the aliases drift and makes a
	// successfully rotated runtime appear pinned to its previous template.
	for key := range metadata {
		if strings.HasPrefix(key, "artifact_") {
			delete(metadata, key)
		}
	}
	metadata["template"] = selected.ID
	metadata["template_id"] = selected.ID
	delete(metadata, "template_build_id")
	metadata["template_name"] = selected.Name
	metadata["template_alias"] = selected.Template
	metadata["template_status"] = selected.Status
	metadata["manifest_version"] = selected.ManifestVersion
	metadata["capabilities"] = FCE2BTemplateCapabilities(provider, selected)
	delete(metadata, "component_versions")
	metadata["runner_protocol"] = selected.RunnerProtocol
	metadata["runner"] = FCE2BRunnerCommandForProvider(provider)
	for key, value := range managedMetadata {
		metadata[key] = value
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("encode FC/E2B runtime metadata: %w", err)
	}
	updated, err := qtx.UpdateFCE2BRuntimeMetadata(ctx, db.UpdateFCE2BRuntimeMetadataParams{
		ID:       runtimeID,
		Metadata: encoded,
	})
	if err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("update FC/E2B runtime metadata: %w", err)
	}
	invalidated, err := qtx.MarkFCE2BSandboxSessionsStaleByRuntime(ctx, runtimeID)
	if err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("invalidate FC/E2B sandbox sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("commit FC/E2B template update: %w", err)
	}
	result.Runtime = updated
	result.InvalidatedSandboxCount = invalidated
	result.Changed = true
	return result, nil
}

type fcE2BLaunchSubmission struct {
	runtime   db.AgentRuntime
	sandboxID string
	coldStart bool
	scope     fcE2BTaskScope
	scoped    bool
}

func (l *FCE2BLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.LaunchTask(ctx, task)
	}
	if l != nil && l.Config.QuickWins.CoalescedHotExec {
		ctx = context.WithValue(ctx, hotRunnerProbeKey{}, &hotRunnerProbe{})
	}
	if l == nil || l.Queries == nil || l.Tasks == nil || !task.RuntimeID.Valid {
		return nil
	}
	taskID := util.UUIDToString(task.ID)
	runtimeID := util.UUIDToString(task.RuntimeID)
	agentID := util.UUIDToString(task.AgentID)
	started := time.Now()
	slog.Info("FC/E2B launch started", "task_id", taskID, "runtime_id", runtimeID, "agent_id", agentID)

	// Avoid taking a runtime lock for local runtimes. The authoritative launch
	// snapshot is loaded again from the lock connection below.
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return fmt.Errorf("load runtime for FC/E2B launch: %w", err)
	}
	if !IsFCE2BRuntime(runtime) {
		return nil
	}
	trace, traceErr := chattrace.ForTask(task.Context, taskID, task.CreatedAt.Time)
	if traceErr != nil {
		failure := ClassifyRuntimeStartFailure(SandboxBackendAliyunFC, "invalid task trace: "+traceErr.Error())
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_launch", "started",
		"task_id", taskID,
		"runtime_id", runtimeID,
	)
	if !l.Config.Enabled {
		failure := ClassifyRuntimeStartFailure(SandboxBackendAliyunFC, "FC/E2B runtime is disabled")
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	if err := l.Config.Validate(); err != nil {
		failure := ClassifyRuntimeStartError(SandboxBackendAliyunFC, err)
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}
	attempt, err := l.Tasks.BeginRuntimeStartAttempt(
		ctx,
		task,
		SandboxBackendAliyunFC,
		runtimeStartProtocolForRuntime(runtime),
	)
	if err != nil {
		if errors.Is(err, errRuntimeLaunchLeaseLost) {
			return err
		}
		failure := NewRuntimeStartFailure(
			SandboxBackendAliyunFC,
			"FCE2B-ATTEMPT-CREATE-FAILED",
			"launch_started",
			false,
			"无法创建 Runtime 启动记录。",
			err.Error(),
		)
		return l.failLaunch(ctx, task, pgtype.UUID{}, failure)
	}

	runtimeLockConn, releaseRuntimeLock, err := l.lockRuntimeShared(ctx, task.RuntimeID)
	if err != nil {
		failure := ClassifyRuntimeStartError(SandboxBackendAliyunFC, err)
		failure.Phase = "runtime_lock"
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	runtimeLockHeld := true
	defer func() {
		if runtimeLockHeld {
			releaseRuntimeLock()
		}
	}()

	// Every database operation before sandbox exec uses the same connection
	// that owns the runtime lock. This prevents a pool-exhaustion deadlock when
	// several launches each reserve one connection for their shared lock.
	lockedLauncher := *l
	lockedLauncher.Queries = db.New(runtimeLockConn)
	lockedLauncher.Tasks = &TaskService{Queries: lockedLauncher.Queries}
	submission, deferred, submitErr := lockedLauncher.submitTaskUnderRuntimeLock(ctx, task, runtimeLockConn, trace, attempt)
	releaseRuntimeLock()
	runtimeLockHeld = false
	if submitErr != nil {
		failure := ClassifyRuntimeStartError(SandboxBackendAliyunFC, submitErr)
		failure = runtimeStartFailureAtLastStage(ctx, l.Queries, attempt, failure)
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	if deferred {
		if err := l.Tasks.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
			failure := NewRuntimeStartFailure(
				SandboxBackendAliyunFC,
				"FCE2B-BLOCKED-ATTEMPT-RECORD-FAILED",
				"task_serialization",
				true,
				"Runtime 启动排队状态记录失败。",
				err.Error(),
			)
			return l.failLaunch(ctx, task, attempt.ID, failure)
		}
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_launch", "waiting",
			"task_id", taskID,
			"runtime_id", runtimeID,
		)
		return nil
	}

	slog.Info("FC/E2B run-once submitted",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"sandbox_id", submission.sandboxID,
		"cold_start", submission.coldStart,
		"duration", time.Since(started).String(),
	)
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_run_once", "submitted",
		"task_id", taskID,
		"sandbox_id", submission.sandboxID,
	)
	claimState, err := l.waitForRunOnceClaim(ctx, task, attempt)
	if err != nil {
		if errors.Is(err, errRuntimeLaunchLeaseLost) {
			return err
		}
		failure := ClassifyRuntimeStartError(SandboxBackendAliyunFC, err)
		failure = runtimeStartFailureAtLastStage(ctx, l.Queries, attempt, failure)
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	switch claimState {
	case fcE2BRunnerClaimObserved:
		slog.Info("FC/E2B run-once claim observed", "task_id", taskID, "runtime_id", runtimeID, "sandbox_id", submission.sandboxID)
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_claim", "observed", "task_id", taskID, "sandbox_id", submission.sandboxID)
	case fcE2BRunnerClaimBlocked:
		if err := l.Tasks.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
			failure := NewRuntimeStartFailure(
				SandboxBackendAliyunFC,
				"FCE2B-BLOCKED-ATTEMPT-RECORD-FAILED",
				"task_serialization",
				true,
				"Runtime 启动排队状态记录失败。",
				err.Error(),
			)
			return l.failLaunch(ctx, task, attempt.ID, failure)
		}
		slog.Info("FC/E2B run-once claim blocked by active task", "task_id", taskID, "runtime_id", runtimeID, "sandbox_id", submission.sandboxID)
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_claim", "blocked", "task_id", taskID, "sandbox_id", submission.sandboxID)
	case fcE2BRunnerClaimFailed:
		return fmt.Errorf("FC/E2B launch failed: Runtime reported a startup failure")
	case fcE2BRunnerClaimStalled:
		detail := fmt.Sprintf("FC/E2B runner did not claim task within %s after sandbox exec", fcE2BRunnerClaimTimeout)
		failure := ClassifyRuntimeStartFailure(SandboxBackendAliyunFC, detail)
		if current, lookupErr := l.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
			ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
		}); lookupErr == nil && current.LastStage != "" {
			failure.Phase = current.LastStage
			failure.InternalDetail = detail + "; last_stage=" + current.LastStage
		}
		return l.failLaunch(ctx, task, attempt.ID, failure)
	}
	if submission.scoped {
		_ = l.Queries.TouchFCE2BSandboxSession(ctx, db.TouchFCE2BSandboxSessionParams{
			RuntimeID: submission.runtime.ID,
			ScopeType: submission.scope.typ,
			ScopeID:   submission.scope.id,
			SandboxID: submission.sandboxID,
		})
	}
	return nil
}

func (l *FCE2BLauncher) submitTaskUnderRuntimeLock(ctx context.Context, task db.AgentTaskQueue, runtimeLockConn *pgxpool.Conn, trace chattrace.Trace, attempt db.AgentTaskRuntimeStartAttempt) (fcE2BLaunchSubmission, bool, error) {
	taskID := util.UUIDToString(task.ID)
	runtimeID := util.UUIDToString(task.RuntimeID)
	agentID := util.UUIDToString(task.AgentID)
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("reload runtime under FC/E2B runtime lock: %w", err)
	}
	if !IsFCE2BRuntime(runtime) {
		return fcE2BLaunchSubmission{}, false, ErrFCE2BRuntimeRequired
	}
	if !runtime.DaemonID.Valid || strings.TrimSpace(runtime.DaemonID.String) == "" {
		return fcE2BLaunchSubmission{}, false, errors.New("FC/E2B runtime has no daemon_id")
	}
	if !runtime.OwnerID.Valid {
		return fcE2BLaunchSubmission{}, false, errors.New("FC/E2B runtime has no owner_id")
	}
	template, err := fcE2BTemplateForRuntime(runtime)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, err
	}
	slog.Info("FC/E2B launch template resolved", "task_id", taskID, "runtime_id", runtimeID, "template", template)
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_template", "resolved",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"template", template,
	)
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "template_resolved"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B template stage: %w", err)
	}
	if runtimeLockConn == nil {
		return fcE2BLaunchSubmission{}, false, errors.New("employee filesystem requires a database admission connection")
	}
	// DSH always uses persistent employee storage; other providers opt in.
	var useEmployeeFilesystem bool
	if err := runtimeLockConn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM dsh_employee_host WHERE workspace_id=$1 AND agent_id=$2)`, runtime.WorkspaceID, task.AgentID).Scan(&useEmployeeFilesystem); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("read employee filesystem binding: %w", err)
	}
	filesystemScope := dshExecutionScope(dshhost.Key{WorkspaceID: uuid.UUID(runtime.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes)}, task)
	if !useEmployeeFilesystem && FCE2BRuntimeProvider(runtime) == "dsh" {
		err := l.prepareDSHTaskFilesystem(ctx, runtimeLockConn, filesystemScope.Key)
		if errors.Is(err, errDSHHostWaiting) {
			return l.deferDSHHostWaiting(ctx, runtimeLockConn, task, attempt, err)
		}
		if err != nil {
			return fcE2BLaunchSubmission{}, false, err
		}
		useEmployeeFilesystem = true
	}
	if useEmployeeFilesystem && FCE2BRuntimeProvider(runtime) == "dsh" && task.TriggerEvidenceKind.String == dshschedule.EvidenceKind {
		execution, loadErr := dshschedule.LoadExecution(ctx, runtimeLockConn, filesystemScope.Key, uuid.UUID(task.ID.Bytes))
		if loadErr != nil || !ScheduleExecutionMatches(task, execution) {
			return fcE2BLaunchSubmission{}, false, errors.New("DSH schedule execution binding is invalid")
		}
		filesystemScope = execution.SessionScope
	}
	filesystemScopeID := employeeFilesystemScopeID(filesystemScope)
	var nativeBinding dshhost.Execution
	if useEmployeeFilesystem && FCE2BRuntimeProvider(runtime) == "dsh" {
		store := dshhost.PostgresStore{DB: runtimeLockConn}
		filesystemScope, err = store.TaskScope(ctx, filesystemScope, uuid.UUID(task.ID.Bytes))
		if err != nil {
			return fcE2BLaunchSubmission{}, false, fmt.Errorf("resolve DSH context epoch: %w", err)
		}
		nativeBinding, err = store.BindExecution(ctx, filesystemScope, uuid.UUID(task.ID.Bytes))
		if err != nil {
			return fcE2BLaunchSubmission{}, false, fmt.Errorf("bind DSH native execution: %w", err)
		}
		filesystemScopeID, err = store.BindSandboxScope(ctx, filesystemScope, filesystemScopeID)
		if err != nil {
			return fcE2BLaunchSubmission{}, false, fmt.Errorf("bind DSH sandbox scope: %w", err)
		}
	}
	if useEmployeeFilesystem {
		// Only this execution scope is coordinated. Other sessions may create
		// sandboxes and mount the same filesystem while this one is starting.
		var release func()
		if filesystemScopeID == uuid.Nil {
			release, err = lockDSHEmployee(ctx, runtimeLockConn, runtime.WorkspaceID, task.AgentID)
		} else {
			release, err = lockEmployeeFilesystemScope(ctx, runtimeLockConn, filesystemScope.Key, filesystemScopeID)
		}
		if err != nil {
			return fcE2BLaunchSubmission{}, false, err
		}
		defer release()
	}

	tasks, err := l.Queries.ListAgentPendingTasks(ctx, task.AgentID)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("check FC/E2B launch serialization: %w", err)
	}
	if blocker, reason, blocked := fcE2BTaskLaunchBlocker(task, tasks); blocked {
		slog.Info("FC/E2B launch deferred by serialized task",
			"event", "fc_e2b_launch_deferred",
			"task_id", taskID,
			"runtime_id", runtimeID,
			"agent_id", agentID,
			"blocker_task_id", util.UUIDToString(blocker.ID),
			"blocker_status", blocker.Status,
			"defer_reason", reason,
		)
		return fcE2BLaunchSubmission{}, true, nil
	}

	scope, scoped := fcE2BScopeForTask(task)
	sandboxResolveStarted := time.Now()
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "sandbox_resolving"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B sandbox stage: %w", err)
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_resolve", "started", "task_id", taskID)
	var sandboxID string
	var coldStart bool
	var employeeHost *dshhost.Host
	if useEmployeeFilesystem {
		var host dshhost.Host
		host, coldStart, err = l.resolveFilesystemScopeSandbox(ctx, filesystemScope.Key, filesystemScopeID, task.ID, runtime, template, runtimeLockConn, trace)
		if errors.Is(err, errDSHHostWaiting) {
			return l.deferDSHHostWaiting(ctx, runtimeLockConn, task, attempt, err)
		}
		sandboxID = host.SandboxID
		employeeHost = &host
		// The filesystem scope store owns this sandbox lifecycle; the ordinary
		// ephemeral sandbox cache must not independently delete or reassign it.
		scoped = false
	} else {
		sandboxID, coldStart, err = l.resolveSandboxOnConnection(ctx, runtime, scope, scoped, template, runtimeLockConn, trace)
	}
	if err != nil {
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_resolve", "failed",
			"task_id", taskID,
			"stage_elapsed_ms", time.Since(sandboxResolveStarted).Milliseconds(),
			"error", err,
		)
		return fcE2BLaunchSubmission{}, false, err
	}
	scopeType := ""
	scopeID := ""
	if scoped {
		scopeType = scope.typ
		scopeID = util.UUIDToString(scope.id)
	}
	slog.Info("FC/E2B sandbox resolved",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"sandbox_id", sandboxID,
		"template", template,
		"cold_start", coldStart,
		"scope_type", scopeType,
		"scope_id", scopeID,
	)
	lifecycleScope := "task"
	if useEmployeeFilesystem {
		lifecycleScope = "employee_" + filesystemScope.Kind
	} else if scoped {
		lifecycleScope = scope.typ
	}
	lifecycleAction := fcE2BSandboxReused
	if coldStart {
		lifecycleAction = fcE2BSandboxCreated
	}
	logFCE2BSandboxLifecycle(lifecycleAction, "task_start", task, sandboxID, lifecycleScope, nil)
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_resolve", "succeeded",
		"task_id", taskID,
		"sandbox_id", sandboxID,
		"cold_start", coldStart,
		"stage_elapsed_ms", time.Since(sandboxResolveStarted).Milliseconds(),
	)
	attempt, err = l.Tasks.UpdateRuntimeStartSandbox(ctx, attempt, sandboxID, coldStart, "sandbox_ready")
	if err != nil {
		return fcE2BLaunchSubmission{}, false, err
	}

	runnerProbeStarted := time.Now()
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "runner_probing"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B runner probe stage: %w", err)
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_runner_probe", "started", "task_id", taskID, "sandbox_id", sandboxID)
	launch, err := l.detectFCE2BRunnerLaunch(ctx, sandboxID, runtime)
	if err != nil {
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_runner_probe", "failed",
			"task_id", taskID,
			"sandbox_id", sandboxID,
			"stage_elapsed_ms", time.Since(runnerProbeStarted).Milliseconds(),
			"error", err,
		)
		return fcE2BLaunchSubmission{}, false, err
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_runner_probe", "succeeded",
		"task_id", taskID,
		"sandbox_id", sandboxID,
		"runner_protocol", string(launch.Mode),
		"stage_elapsed_ms", time.Since(runnerProbeStarted).Milliseconds(),
	)
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "runner_probe_succeeded"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B runner probe stage: %w", err)
	}

	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "task_environment_preparing"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B task environment stage: %w", err)
	}
	extraEnv, err := l.extraEnvForTask(ctx, task, runtime, sandboxID)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, err
	}
	extraEnv = hardenCloudSandboxA2ARunnerEnv(task, runtime, extraEnv)
	if employeeHost != nil {
		if extraEnv == nil {
			extraEnv = make(map[string]string)
		}
		extraEnv["MULTICA_FS_ROOT"] = dshhost.MountPath
	}
	// Inject shared-disk env only when FC actually volume-mounted it.
	// The value is the mount in force for this launch, not the desired grant.
	if access := effectiveWorkspaceFSAccess(employeeHost); access != "" {
		if extraEnv == nil {
			extraEnv = make(map[string]string)
		}
		extraEnv["MULTICA_WORKSPACE_FS_ROOT"] = dshhost.WorkspaceSharedRoot
		extraEnv["MULTICA_WORKSPACE_FS_ACCESS"] = access
	}
	if employeeHost != nil && FCE2BRuntimeProvider(runtime) == "dsh" {
		binding := nativeBinding
		if extraEnv == nil {
			extraEnv = make(map[string]string)
		}
		extraEnv["DSH_HOME"] = dshhost.MountPath + "/home"
		extraEnv["MULTICA_DSH_WORKSPACE_ID"] = employeeHost.WorkspaceID.String()
		extraEnv["MULTICA_DSH_AGENT_ID"] = employeeHost.AgentID.String()
		extraEnv["MULTICA_DSH_HOST_GENERATION"] = strconv.FormatInt(employeeHost.Generation, 10)
		extraEnv["MULTICA_DSH_SESSION_ID"] = binding.SessionID
		extraEnv["MULTICA_DSH_REQUEST_ID"] = binding.RequestID.String()
		extraEnv["MULTICA_DSH_WORKDIR"] = binding.Workdir
	}
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "daemon_token_preparing"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B daemon token stage: %w", err)
	}
	token, err := auth.GenerateDaemonToken()
	if err != nil {
		return fcE2BLaunchSubmission{}, false, errors.New("failed to mint FC/E2B daemon token")
	}
	tokenExpiresAt := time.Now().Add(fcE2BDaemonTokenTTL)
	extraEnv, err = l.withSandboxRelayToken(
		extraEnv,
		task.ID,
		task.AgentID,
		runtime.ID,
		sandboxID,
		token,
		tokenExpiresAt,
	)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, err
	}
	if _, err := l.Queries.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(token),
		WorkspaceID: runtime.WorkspaceID,
		DaemonID:    runtime.DaemonID.String,
		ExpiresAt:   pgtype.Timestamptz{Time: tokenExpiresAt, Valid: true},
	}); err != nil {
		return fcE2BLaunchSubmission{}, false, errors.New("failed to persist FC/E2B daemon token")
	}
	if attempt.Protocol == RuntimeStartProtocolHTTPJSONV1 {
		if extraEnv == nil {
			extraEnv = make(map[string]string)
		}
		extraEnv["MULTICA_RUNTIME_START_ATTEMPT_ID"] = util.UUIDToString(attempt.ID)
		extraEnv["MULTICA_RUNTIME_START_PROTOCOL"] = attempt.Protocol
	}
	if _, err := l.Tasks.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "runner_exec_submitting"); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B runner exec submission stage: %w", err)
	}
	runOnceStarted := time.Now()
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_run_once", "started", "task_id", taskID, "sandbox_id", sandboxID)
	if err := l.execRunOnce(ctx, sandboxID, runtime, launch.Mode, task.ID, token, coldStart, extraEnv); err != nil {
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_run_once", "failed",
			"task_id", taskID,
			"sandbox_id", sandboxID,
			"stage_elapsed_ms", time.Since(runOnceStarted).Milliseconds(),
			"error", err,
		)
		return fcE2BLaunchSubmission{}, false, err
	}
	if err := l.Tasks.RecordRuntimeStartRunnerExecSubmitted(ctx, attempt.ID, task.ID, task.RuntimeID); err != nil {
		return fcE2BLaunchSubmission{}, false, fmt.Errorf("record FC/E2B runner exec stage: %w", err)
	}
	return fcE2BLaunchSubmission{runtime: runtime, sandboxID: sandboxID, coldStart: coldStart, scope: scope, scoped: scoped}, false, nil
}

func (l *FCE2BLauncher) withSandboxRelayToken(
	extraEnv map[string]string,
	taskID pgtype.UUID,
	agentID pgtype.UUID,
	runtimeID pgtype.UUID,
	sandboxID string,
	daemonToken string,
	expiresAt time.Time,
) (map[string]string, error) {
	if extraEnv == nil {
		extraEnv = make(map[string]string)
	}
	if extraEnv[llmTraceEnabledEnvKey] == "true" && strings.TrimSpace(extraEnv[llmTraceSinkURLEnvKey]) != "" {
		extraEnv[llmTraceTokenEnvKey] = daemonToken
		extraEnv[llmTraceExpiresAtEnvKey] = strconv.FormatInt(expiresAt.UnixMilli(), 10)
	}
	if l == nil || l.SandboxRelaySigner == nil {
		return extraEnv, nil
	}
	relayToken, err := l.SandboxRelaySigner.Mint(sandboxrelay.MintRequest{
		TaskID:             util.UUIDToString(taskID),
		AgentID:            util.UUIDToString(agentID),
		RuntimeID:          util.UUIDToString(runtimeID),
		SandboxID:          sandboxID,
		DaemonToken:        daemonToken,
		AgentIdentityToken: extraEnv[protocol.AgentIdentityContextTokenEnvKey],
		ExpiresAt:          expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("mint FC/E2B sandbox relay token: %w", err)
	}
	extraEnv[protocol.SandboxRelayTokenEnvKey] = relayToken
	return extraEnv, nil
}

func fcE2BScopeForTask(task db.AgentTaskQueue) (fcE2BTaskScope, bool) {
	if task.ChatSessionID.Valid {
		return fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: task.ChatSessionID}, true
	}
	if task.IssueID.Valid {
		return fcE2BTaskScope{typ: fcE2BScopeTypeIssue, id: task.IssueID}, true
	}
	return fcE2BTaskScope{}, false
}

func (l *FCE2BLauncher) waitForRunOnceClaim(
	ctx context.Context,
	task db.AgentTaskQueue,
	attempt db.AgentTaskRuntimeStartAttempt,
) (fcE2BRunnerClaimState, error) {
	deadline := time.NewTimer(fcE2BRunnerClaimTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(fcE2BRunnerClaimPollInterval)
	defer ticker.Stop()
	checkedInitialBlocker := false

	for {
		// A task token is written only at the ordinary-task final claim boundary.
		// Besides the current handler's in-transaction finalization, this CAS lets
		// a new launcher observe an ordinary claim completed by an older server
		// replica during a rolling deployment. It must never opt into tokenless A2A
		// finalization here: dispatched precedes response construction, so only the
		// handler transaction may mark a tokenless claim as complete.
		if _, err := l.Queries.FinalizeAgentTaskRuntimeStartAttemptForTask(ctx, db.FinalizeAgentTaskRuntimeStartAttemptForTaskParams{
			TaskID:            task.ID,
			RuntimeID:         task.RuntimeID,
			AllowTokenlessA2a: false,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("finalize Runtime runner claim from task token: %w", err)
		}
		currentAttempt, err := l.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
			ID:        attempt.ID,
			TaskID:    task.ID,
			RuntimeID: task.RuntimeID,
		})
		if err != nil {
			return "", fmt.Errorf("verify Runtime runner claim: %w", err)
		}
		switch currentAttempt.Status {
		case "claimed":
			return fcE2BRunnerClaimObserved, nil
		case "blocked":
			return fcE2BRunnerClaimBlocked, nil
		case "failed", "timed_out":
			return fcE2BRunnerClaimFailed, nil
		case "superseded":
			return "", errRuntimeLaunchLeaseLost
		case "starting":
		default:
			return "", fmt.Errorf("verify Runtime runner claim: unexpected attempt status %q", currentAttempt.Status)
		}
		if !checkedInitialBlocker {
			checkedInitialBlocker = true
			current, err := l.Queries.GetAgentTask(ctx, task.ID)
			if err != nil {
				return "", fmt.Errorf("load task while checking Runtime runner blockers: %w", err)
			}
			tasks, err := l.Queries.ListAgentPendingTasks(ctx, current.AgentID)
			if err != nil {
				return "", fmt.Errorf("check FC/E2B runner claim blockers: %w", err)
			}
			if fcE2BTaskHasActiveBlocker(current, tasks) {
				return fcE2BRunnerClaimBlocked, nil
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			freshAttempt, err := l.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
				ID:        attempt.ID,
				TaskID:    task.ID,
				RuntimeID: task.RuntimeID,
			})
			if err != nil {
				return "", fmt.Errorf("verify Runtime runner claim at deadline: %w", err)
			}
			switch freshAttempt.Status {
			case "claimed":
				return fcE2BRunnerClaimObserved, nil
			case "blocked":
				return fcE2BRunnerClaimBlocked, nil
			case "failed", "timed_out":
				return fcE2BRunnerClaimFailed, nil
			case "superseded":
				return "", errRuntimeLaunchLeaseLost
			}
			fresh, err := l.Queries.GetAgentTask(ctx, task.ID)
			if err != nil {
				return "", fmt.Errorf("load task while checking Runtime runner blockers at deadline: %w", err)
			}
			tasks, err := l.Queries.ListAgentPendingTasks(ctx, fresh.AgentID)
			if err != nil {
				return "", fmt.Errorf("check FC/E2B runner claim blockers: %w", err)
			}
			if fcE2BTaskHasActiveBlocker(fresh, tasks) {
				return fcE2BRunnerClaimBlocked, nil
			}
			return fcE2BRunnerClaimStalled, nil
		case <-ticker.C:
		}
	}
}

func fcE2BTaskHasActiveBlocker(target db.AgentTaskQueue, tasks []db.AgentTaskQueue) bool {
	for _, active := range tasks {
		if active.ID == target.ID || active.AgentID != target.AgentID {
			continue
		}
		if !fcE2BTaskStatusBlocksClaim(active.Status) {
			continue
		}
		if target.IssueID.Valid && active.IssueID == target.IssueID {
			return true
		}
		if target.ChatSessionID.Valid && active.ChatSessionID == target.ChatSessionID {
			return true
		}
		if !target.IssueID.Valid && !target.ChatSessionID.Valid && !target.AutopilotRunID.Valid &&
			!active.IssueID.Valid && !active.ChatSessionID.Valid && !active.AutopilotRunID.Valid {
			return true
		}
	}
	return false
}

func fcE2BTaskLaunchBlocker(target db.AgentTaskQueue, tasks []db.AgentTaskQueue) (db.AgentTaskQueue, string, bool) {
	for _, candidate := range tasks {
		if candidate.ID == target.ID || candidate.AgentID != target.AgentID ||
			!sameTaskSerializationGroup(target, candidate) {
			continue
		}
		if fcE2BTaskStatusBlocksClaim(candidate.Status) {
			return candidate, "active_task", true
		}
	}

	for _, candidate := range tasks {
		if candidate.ID == target.ID || candidate.AgentID != target.AgentID ||
			candidate.Status != "queued" || !sameTaskSerializationGroup(target, candidate) {
			continue
		}
		if fcE2BQueuedTaskPrecedes(candidate, target) {
			return candidate, "queued_predecessor", true
		}
	}
	return db.AgentTaskQueue{}, "", false
}

func fcE2BQueuedTaskPrecedes(candidate, target db.AgentTaskQueue) bool {
	if candidate.Priority != target.Priority {
		return candidate.Priority > target.Priority
	}
	if !candidate.CreatedAt.Valid || !target.CreatedAt.Valid {
		return false
	}
	return candidate.CreatedAt.Time.Before(target.CreatedAt.Time)
}

func fcE2BTaskStatusBlocksClaim(status string) bool {
	switch status {
	case "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

func (l *FCE2BLauncher) extraEnvForTask(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	sandboxID string,
) (map[string]string, error) {
	resolver := l.Config.ModelForAgent
	if l.Config.TaskModelResolver != nil {
		resolver = func(model string) (string, error) {
			return l.Config.TaskModelResolver(ctx, util.UUIDToString(task.ID), model)
		}
	}
	return l.extraEnvForTaskWithModel(ctx, task, runtime, sandboxID, resolver)
}

func (l *FCE2BLauncher) extraEnvForTaskWithModel(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	sandboxID string,
	modelForAgent func(string) (string, error),
) (map[string]string, error) {
	if modelForAgent == nil {
		return nil, errors.New("cloud sandbox model resolver is unavailable")
	}
	agentRow, err := l.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return nil, fmt.Errorf("load agent for cloud sandbox launch: %w", err)
	}
	model, err := modelForAgent(agentRow.Model.String)
	if err != nil {
		return nil, err
	}
	env, err := sandboxSourceEnv(task.Context)
	if err != nil {
		return nil, err
	}
	if chatSessionID, ok := fcE2BChatSessionID(task); ok {
		env[fcE2BChatSessionIDEnvKey] = chatSessionID
	}
	if task.IssueID.Valid {
		env["MULTICA_ISSUE_ID"] = util.UUIDToString(task.IssueID)
	}
	env["OPENAI_MODEL"] = model
	for key, value := range llmTraceEnv(
		runtime,
		agentRow.RuntimeConfig,
		task.Context,
		l.Config.ServerURL,
		util.UUIDToString(task.ID),
		l.LLMTraceCaptureAlways,
	) {
		env[key] = value
	}
	traceEnv, err := fcE2BTaskTraceEnv(task)
	if err != nil {
		return nil, err
	}
	for key, value := range traceEnv {
		env[key] = value
	}
	agentIdentityEnv, err := l.identityEnvForTask(ctx, task, runtime, sandboxID, agentRow)
	if err != nil {
		return nil, err
	}
	for key, value := range agentIdentityEnv {
		env[key] = value
	}
	slog.Info("FC/E2B sandbox source selected",
		"event", "fc_e2b_sandbox_source_selected",
		"task_id", util.UUIDToString(task.ID),
		"sandbox_source_hostname", env[protocol.SandboxSourceHostnameEnvKey],
		"dingtalk_stream_hostname", env[protocol.DingTalkStreamHostnameEnvKey],
		"dingtalk_stream_node_id", env[protocol.DingTalkStreamNodeIDEnvKey],
		"dingtalk_stream_connection_id", env[protocol.DingTalkStreamConnectionIDEnvKey],
	)
	return env, nil
}

func fcE2BChatSessionID(task db.AgentTaskQueue) (string, bool) {
	if !task.ChatSessionID.Valid {
		return "", false
	}
	return util.UUIDToString(task.ChatSessionID), true
}

func (l *FCE2BLauncher) identityEnvForTask(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	sandboxID string,
	agentRow db.Agent,
) (map[string]string, error) {
	if IsA2ATaskOrigin(task.Context) {
		if requiresA2ADEAPDWSToken(task.Context) {
			if !CloudSandboxRuntimeHasCapability(runtime, "dws") {
				return nil, errors.New("A2A DEAP DWS identity requires a DWS-capable Runtime")
			}
			identity, ok := a2aintegration.InvocationIdentityFromContext(ctx)
			if !ok || strings.TrimSpace(identity.DEAPDWSToken) == "" {
				return nil, errors.New("A2A DEAP DWS identity is unavailable outside its request")
			}
			if identity.ContextToken != "" {
				return nil, errors.New("A2A DEAP DWS identity conflicts with ContextToken identity")
			}
			return map[string]string{protocol.DEAPDWSTokenEnvKey: identity.DEAPDWSToken}, nil
		}
		// A2A may use only the explicitly supplied task-local external token.
		// fcE2BAgentIdentityExtraEnv validates the paired expiry and external
		// source marker; owner bindings and connected identities stay skipped.
		return fcE2BAgentIdentityExtraEnv(task, l.Config)
	}
	resolved, err := l.resolveIdentityForTask(ctx, task, runtime, sandboxID, agentRow)
	if err != nil {
		return nil, err
	}
	if resolved.ContextToken == "" {
		return nil, nil
	}
	return fcE2BAgentIdentityEnvForToken(resolved.ContextToken, l.Config)
}

type fcE2BResolvedIdentity struct {
	ContextToken string
	Source       string
}

type fcE2BDWSIdentity struct {
	UID    string
	OrgID  string
	Source string
}

func (l *FCE2BLauncher) resolveIdentityForTask(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	sandboxID string,
	agentRow db.Agent,
) (fcE2BResolvedIdentity, error) {
	taskID := util.UUIDToString(task.ID)
	// This resolver is shared by both Aliyun FC and ASB launchers. Capability
	// detection must therefore use the common cloud-sandbox metadata model.
	hasDWSCapability := CloudSandboxRuntimeHasCapability(runtime, "dws")
	githubConnection, hasGithubConnection := l.githubConnectionForAgent(ctx, task, runtime, agentRow)

	stableDWS := fcE2BDWSIdentity{}
	if hasDWSCapability {
		if l.IdentityBindings == nil {
			return fcE2BResolvedIdentity{}, errors.New("Agent identity binding reader is not configured")
		}
		identity, err := l.IdentityBindings.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
			WorkspaceID: runtime.WorkspaceID,
			AgentID:     task.AgentID,
		})
		if err == nil {
			stableDWS = fcE2BDWSIdentity{
				UID:    identity.DwsUid,
				OrgID:  identity.OrgID,
				Source: "agent_binding_fallback",
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fcE2BResolvedIdentity{}, fmt.Errorf("load Agent DingTalk identity: %w", err)
		}
		if stableDWS.Source == "" {
			externalDWS, present, err := fcE2BExternalDWSIdentity(task)
			if err != nil {
				return fcE2BResolvedIdentity{}, err
			}
			if present {
				stableDWS = externalDWS
			}
		}
	}
	if stableDWS.Source != "" {
		return l.createResolvedIdentityContext(ctx, task, sandboxID, stableDWS, githubConnection, hasGithubConnection)
	}

	prepared, err := fcE2BAgentIdentityExtraEnv(task, l.Config)
	if err != nil && !errors.Is(err, errAgentIdentityContextTokenRefreshRequired) {
		return fcE2BResolvedIdentity{}, err
	}
	if len(prepared) > 0 {
		contextToken := prepared[protocol.AgentIdentityContextTokenEnvKey]
		if hasGithubConnection {
			l.extendPreparedContextWithGithub(ctx, contextToken, task, runtime, sandboxID, githubConnection)
		}
		slog.Info("FC/E2B task identity selected",
			"task_id", taskID,
			"identity_source", "prepared_context_token",
			"github_identity", hasGithubConnection,
		)
		return fcE2BResolvedIdentity{ContextToken: contextToken, Source: "prepared_context_token"}, nil
	}

	refreshRequired := errors.Is(err, errAgentIdentityContextTokenRefreshRequired)
	if refreshRequired {
		slog.Info("FC/E2B cached task identity requires refresh",
			"task_id", taskID,
			"identity_source", "task_context_cache",
		)
	}
	if hasGithubConnection {
		return l.createResolvedIdentityContext(ctx, task, sandboxID, fcE2BDWSIdentity{Source: "agent_github_binding"}, githubConnection, true)
	}
	if refreshRequired {
		if !hasDWSCapability {
			return fcE2BResolvedIdentity{}, errors.New("DWS capability is required to refresh the cached Agent Identity ContextToken")
		}
		return fcE2BResolvedIdentity{}, errors.New("Multica Agent DingTalk identity binding is required to refresh the cached ContextToken")
	}
	slog.Info("FC/E2B task identity selected",
		"task_id", taskID,
		"identity_source", "none",
		"dws_capability", hasDWSCapability,
		"dws_identity", false,
		"github_identity", false,
	)
	return fcE2BResolvedIdentity{}, nil
}

func (l *FCE2BLauncher) createResolvedIdentityContext(
	ctx context.Context,
	task db.AgentTaskQueue,
	sandboxID string,
	dwsIdentity fcE2BDWSIdentity,
	githubConnection agentidentitygithub.Connection,
	hasGithubConnection bool,
) (fcE2BResolvedIdentity, error) {
	if l.AgentIdentity == nil {
		return fcE2BResolvedIdentity{}, errors.New("Agent Identity HSF client is not configured")
	}
	taskID := util.UUIDToString(task.ID)
	agentID := util.UUIDToString(task.AgentID)
	source := map[string]string{
		"app":             "dt-fde-multica",
		"identity_source": dwsIdentity.Source,
	}
	if hasGithubConnection {
		source["github_identity_source"] = "agent_github_binding"
	}
	result, err := l.AgentIdentity.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
		RequestID:          "multica-task-" + taskID,
		TaskID:             taskID,
		AgentID:            agentID,
		RuntimeType:        "E2B",
		RuntimeID:          sandboxID,
		Reason:             "Multica Agent runtime authorization",
		Source:             source,
		UID:                dwsIdentity.UID,
		OrgID:              dwsIdentity.OrgID,
		GithubConnectionID: githubConnection.ConnectionID,
		TTLSeconds:         900,
	})
	if err != nil {
		return fcE2BResolvedIdentity{}, fmt.Errorf("create Agent Identity context for task: %w", err)
	}
	hasDWSIdentity := dwsIdentity.UID != ""
	l.debugLogAgentIdentityContextToken(taskID, agentID, sandboxID, result, dwsIdentity.Source, hasDWSIdentity, hasGithubConnection)
	slog.Info("FC/E2B task identity selected",
		"task_id", taskID,
		"identity_source", dwsIdentity.Source,
		"dws_identity", hasDWSIdentity,
		"github_identity", hasGithubConnection,
	)
	return fcE2BResolvedIdentity{ContextToken: result.ContextToken, Source: dwsIdentity.Source}, nil
}

func (l *FCE2BLauncher) debugLogAgentIdentityContextToken(
	taskID string,
	agentID string,
	sandboxID string,
	result agentidentityhsf.CreateContextResult,
	identitySource string,
	hasDWSBinding bool,
	hasGithubConnection bool,
) {
	if l == nil {
		return
	}
	enabled, reason := l.Config.shouldDebugLogAgentIdentityContext(agentID)
	if !enabled {
		if l.Config.AgentIdentityDebugLogContextToken && reason == "environment_not_allowed" {
			slog.Warn("FC/E2B Agent Identity ContextToken debug log blocked",
				"event", "fc_e2b_identity_context_debug_blocked",
				"task_id", taskID,
				"agent_id", agentID,
				"reason", reason,
			)
		}
		return
	}
	slog.Warn("FC/E2B Agent Identity ContextToken debug",
		"event", "fc_e2b_identity_context_debug",
		"task_id", taskID,
		"agent_id", agentID,
		"sandbox_id", sandboxID,
		"identity_source", identitySource,
		"dws_identity", hasDWSBinding,
		"github_identity", hasGithubConnection,
		"expires_at", result.ExpiresAt,
		"context_value", result.ContextToken,
	)
}

func fcE2BExternalDWSIdentity(task db.AgentTaskQueue) (fcE2BDWSIdentity, bool, error) {
	if len(bytes.TrimSpace(task.Context)) == 0 {
		return fcE2BDWSIdentity{}, false, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(task.Context, &payload); err != nil {
		return fcE2BDWSIdentity{}, false, fmt.Errorf("parse task context for external DWS identity: %w", err)
	}
	raw, present := payload["external_identity"]
	if !present {
		return fcE2BDWSIdentity{}, false, nil
	}
	var external struct {
		DWS *struct {
			UID   string `json:"uid"`
			OrgID string `json:"orgId"`
		} `json:"dws"`
	}
	if err := json.Unmarshal(raw, &external); err != nil {
		return fcE2BDWSIdentity{}, false, errors.New("parse external DWS identity from task context")
	}
	if external.DWS == nil {
		return fcE2BDWSIdentity{}, false, nil
	}
	uid := strings.TrimSpace(external.DWS.UID)
	orgID := strings.TrimSpace(external.DWS.OrgID)
	if uid != external.DWS.UID || orgID != external.DWS.OrgID ||
		!validFCE2BDWSIdentifier(uid) || !validFCE2BDWSIdentifier(orgID) {
		return fcE2BDWSIdentity{}, false, errors.New("external DWS identity in task context is invalid")
	}
	return fcE2BDWSIdentity{UID: uid, OrgID: orgID, Source: "external_dws"}, true, nil
}

func validFCE2BDWSIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (l *FCE2BLauncher) githubConnectionForAgent(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	agentRow db.Agent,
) (agentidentitygithub.Connection, bool) {
	if l == nil || l.GitHubIdentity == nil || !agentRow.OwnerID.Valid {
		return agentidentitygithub.Connection{}, false
	}
	if enabled, ok := l.GitHubIdentity.(interface{ Enabled() bool }); ok && !enabled.Enabled() {
		return agentidentitygithub.Connection{}, false
	}
	connection, err := l.GitHubIdentity.GetStatus(
		ctx,
		util.UUIDToString(runtime.WorkspaceID),
		util.UUIDToString(task.AgentID),
		util.UUIDToString(agentRow.OwnerID),
	)
	if err != nil {
		slog.Warn("FC/E2B GitHub identity lookup skipped",
			"task_id", util.UUIDToString(task.ID),
			"agent_id", util.UUIDToString(task.AgentID),
			"error", err,
		)
		return agentidentitygithub.Connection{}, false
	}
	if strings.TrimSpace(connection.ConnectionID) == "" || !strings.EqualFold(strings.TrimSpace(connection.Status), "ACTIVE") {
		return agentidentitygithub.Connection{}, false
	}
	return connection, true
}

func (l *FCE2BLauncher) extendPreparedContextWithGithub(
	ctx context.Context,
	contextToken string,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	sandboxID string,
	connection agentidentitygithub.Connection,
) {
	if l == nil || l.AgentIdentity == nil || strings.TrimSpace(contextToken) == "" || strings.TrimSpace(connection.ConnectionID) == "" {
		return
	}
	taskID := util.UUIDToString(task.ID)
	_, err := l.AgentIdentity.ExtendContext(ctx, agentidentityhsf.ExtendContextRequest{
		ContextToken:       contextToken,
		TaskID:             taskID,
		AgentID:            util.UUIDToString(task.AgentID),
		RuntimeType:        "E2B",
		RuntimeID:          sandboxID,
		Reason:             "Multica Agent GitHub authorization",
		GithubConnectionID: connection.ConnectionID,
		Source: map[string]string{
			"app":             "dt-fde-multica",
			"identity_source": "prepared_context_token",
		},
	})
	if err != nil {
		slog.Warn("FC/E2B GitHub identity extend skipped",
			"task_id", taskID,
			"agent_id", util.UUIDToString(task.AgentID),
			"connection_id", connection.ConnectionID,
			"error", err,
		)
		return
	}
	slog.Info("FC/E2B GitHub identity appended to prepared ContextToken",
		"task_id", taskID,
		"agent_id", util.UUIDToString(task.AgentID),
		"connection_id", connection.ConnectionID,
	)
}

func fcE2BTaskTraceEnv(task db.AgentTaskQueue) (map[string]string, error) {
	trace, err := chattrace.ForTask(task.Context, util.UUIDToString(task.ID), task.CreatedAt.Time)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		chattrace.TraceIDEnvKey:              trace.TraceID,
		chattrace.TraceStartedAtUnixMSEnvKey: strconv.FormatInt(trace.StartedAtUnixMS, 10),
	}, nil
}

func sandboxSourceEnv(taskContext []byte) (map[string]string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox source hostname: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return nil, errors.New("sandbox source hostname is empty")
	}
	env := map[string]string{
		protocol.SandboxSourceHostnameEnvKey: hostname,
	}
	streamSource, present, err := dingTalkStreamSourceFromTask(taskContext)
	if err != nil {
		return nil, err
	}
	if present {
		env[protocol.DingTalkStreamHostnameEnvKey] = streamSource.Hostname
		env[protocol.DingTalkStreamNodeIDEnvKey] = streamSource.NodeID
		env[protocol.DingTalkStreamConnectionIDEnvKey] = streamSource.ConnectionID
	}
	return env, nil
}

func dingTalkStreamSourceFromTask(taskContext []byte) (protocol.DingTalkStreamSource, bool, error) {
	if len(bytes.TrimSpace(taskContext)) == 0 {
		return protocol.DingTalkStreamSource{}, false, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(taskContext, &payload); err != nil {
		return protocol.DingTalkStreamSource{}, false, errors.New("decode chat task context")
	}
	raw, present := payload[protocol.DingTalkStreamSourceJSONKey]
	if !present {
		return protocol.DingTalkStreamSource{}, false, nil
	}
	var source protocol.DingTalkStreamSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return protocol.DingTalkStreamSource{}, true, errors.New("invalid DingTalk Stream source in task context")
	}
	source.Hostname = strings.TrimSpace(source.Hostname)
	source.NodeID = strings.TrimSpace(source.NodeID)
	source.ConnectionID = strings.TrimSpace(source.ConnectionID)
	if source.Hostname == "" || source.NodeID == "" || source.ConnectionID == "" {
		return protocol.DingTalkStreamSource{}, true, errors.New("invalid DingTalk Stream source in task context")
	}
	return source, true, nil
}

func fcE2BAgentIdentityExtraEnv(task db.AgentTaskQueue, cfg FCE2BConfig) (map[string]string, error) {
	if len(bytes.TrimSpace(task.Context)) == 0 {
		return nil, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(task.Context, &payload); err != nil {
		return nil, fmt.Errorf("parse task context for Agent Identity: %w", err)
	}
	rawToken, tokenPresent := payload[protocol.AgentIdentityContextTokenJSONKey]
	rawExpiresAt, expiresAtPresent := payload[protocol.AgentIdentityContextTokenExpiresAtJSONKey]
	rawSource, sourcePresent := payload[protocol.AgentIdentityContextTokenSourceJSONKey]
	source := ""
	if sourcePresent {
		if err := json.Unmarshal(rawSource, &source); err != nil {
			return nil, fmt.Errorf("parse Agent Identity ContextToken source: %w", err)
		}
		source = strings.TrimSpace(source)
		if source != protocol.AgentIdentityContextTokenSourceExternal {
			return nil, fmt.Errorf("Agent Identity ContextToken source %q is invalid", source)
		}
	}
	if !tokenPresent && !expiresAtPresent {
		if sourcePresent {
			return nil, errors.New("Agent Identity ContextToken source is present without a ContextToken")
		}
		return nil, nil
	}
	if !tokenPresent {
		return nil, errors.New("Agent Identity ContextToken expiry is present without a ContextToken")
	}
	if !expiresAtPresent {
		return nil, errors.New("Agent Identity ContextToken expiry is missing")
	}
	var token string
	if err := json.Unmarshal(rawToken, &token); err != nil {
		return nil, fmt.Errorf("parse Agent Identity ContextToken: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("Agent Identity ContextToken is empty")
	}
	var expiresAt int64
	if err := json.Unmarshal(rawExpiresAt, &expiresAt); err != nil {
		return nil, fmt.Errorf("parse Agent Identity ContextToken expiry: %w", err)
	}
	now := time.Now()
	if err := validateAgentIdentityContextExpiry(expiresAt, now); err != nil {
		if source != protocol.AgentIdentityContextTokenSourceExternal {
			return nil, fmt.Errorf("%w: %v", errAgentIdentityContextTokenRefreshRequired, err)
		}
		return nil, err
	}
	return fcE2BAgentIdentityEnvForToken(token, cfg)
}

func validateAgentIdentityContextExpiry(expiresAt int64, now time.Time) error {
	if expiresAt <= 0 {
		return errors.New("Agent Identity ContextToken expiry is invalid")
	}
	expiry := time.UnixMilli(expiresAt)
	if !expiry.After(now) {
		return fmt.Errorf("Agent Identity ContextToken expired at %s", expiry.UTC().Format(time.RFC3339Nano))
	}
	if expiry.Sub(now) <= time.Minute {
		return fmt.Errorf("Agent Identity ContextToken expires within the one-minute execution safety window at %s", expiry.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

func fcE2BAgentIdentityEnvForToken(token string, cfg FCE2BConfig) (map[string]string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("Agent Identity returned an empty ContextToken")
	}
	baseURL := cfg.agentIdentitySandboxBaseURL()
	if baseURL == "" {
		return nil, errors.New("MULTICA_AGENT_IDENTITY_BASE_URL is required for ContextToken tasks")
	}
	timeout := cfg.AgentIdentityTimeout
	if timeout <= 0 {
		timeout = defaultAgentIdentityTimeout
	}
	seconds := int(timeout / time.Second)
	if seconds <= 0 {
		seconds = int(defaultAgentIdentityTimeout / time.Second)
	}
	env := map[string]string{
		protocol.AgentIdentityContextTokenEnvKey: token,
		"MULTICA_AGENT_IDENTITY_BASE_URL":        baseURL,
		"MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS": strconv.Itoa(seconds),
	}
	if cfg.DWSClientSecret != "" {
		env["DWS_CLIENT_SECRET"] = cfg.DWSClientSecret
	}
	return env, nil
}

func (l *FCE2BLauncher) resolveSandbox(ctx context.Context, rt db.AgentRuntime, scope fcE2BTaskScope, scoped bool, template string, trace chattrace.Trace) (string, bool, error) {
	return l.resolveSandboxOnConnection(ctx, rt, scope, scoped, template, nil, trace)
}

func (l *FCE2BLauncher) resolveSandboxOnConnection(ctx context.Context, rt db.AgentRuntime, scope fcE2BTaskScope, scoped bool, template string, runtimeLockConn *pgxpool.Conn, trace chattrace.Trace) (string, bool, error) {
	var sandboxID string
	coldStart := true
	if scoped {
		// Serialize the lookup-or-create with the other replicas before reading:
		// a check outside the lock is exactly the race that orphans sandboxes.
		release, err := l.lockSandboxScopeOnConnection(ctx, rt, scope, runtimeLockConn)
		if err != nil {
			return "", false, err
		}
		defer release()

		session, err := l.Queries.GetActiveFCE2BSandboxSession(ctx, db.GetActiveFCE2BSandboxSessionParams{
			RuntimeID: rt.ID,
			ScopeType: scope.typ,
			ScopeID:   scope.id,
			Template:  template,
		})
		if err == nil {
			sandboxID, coldStart = session.SandboxID, false
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, fmt.Errorf("load FC/E2B sandbox session: %w", err)
		}
	}

	// One candidate plus one replacement bounds platform failures. Both warm
	// reuse and cold creation must renew before the task can be submitted.
	var lastErr error
	for candidate := 0; candidate < 2; candidate++ {
		if err := ctx.Err(); err != nil {
			return "", coldStart, err
		}
		if sandboxID == "" {
			coldStart = true
			createStarted := time.Now()
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_create", "started", "template", template)
			var err error
			sandboxID, err = l.createSandbox(ctx, template)
			if err != nil {
				chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_create", "failed", "template", template, "stage_elapsed_ms", time.Since(createStarted).Milliseconds(), "error", err)
				return "", true, err
			}
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_create", "succeeded", "sandbox_id", sandboxID, "template", template, "stage_elapsed_ms", time.Since(createStarted).Milliseconds())
		}
		readyStarted := time.Now()
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "checking", "sandbox_id", sandboxID, "cold_start", coldStart)
		var readyErr error
		if coldStart {
			readyErr = l.waitSandboxReady(ctx, sandboxID)
		} else {
			readyErr = l.checkReusedSandboxReady(ctx, sandboxID, rt)
		}
		var expiresAt time.Time
		if readyErr == nil {
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "succeeded", "sandbox_id", sandboxID, "cold_start", coldStart, "stage_elapsed_ms", time.Since(readyStarted).Milliseconds())
			expiresAt, readyErr = l.renewSandboxForTask(ctx, sandboxID, trace)
		} else {
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "failed", "sandbox_id", sandboxID, "cold_start", coldStart, "stage_elapsed_ms", time.Since(readyStarted).Milliseconds(), "error", readyErr)
		}
		if readyErr != nil {
			// A cancelled launch must not replace a healthy warm sandbox. A new
			// instance has no task yet and can be released even on cancellation.
			if err := ctx.Err(); err != nil {
				if coldStart {
					l.releaseUnusedSandbox(ctx, sandboxID, trace)
				}
				return "", coldStart, err
			}
			if scoped && !coldStart {
				if err := l.Queries.MarkFCE2BSandboxSessionStale(ctx, db.MarkFCE2BSandboxSessionStaleParams{
					RuntimeID: rt.ID, ScopeType: scope.typ, ScopeID: scope.id, SandboxID: sandboxID,
				}); err != nil {
					return "", false, fmt.Errorf("invalidate FC/E2B sandbox before replacement: %w", err)
				}
			}
			l.releaseUnusedSandbox(ctx, sandboxID, trace)
			lastErr = fmt.Errorf("prepare FC/E2B sandbox %s: %w", sandboxID, readyErr)
			if candidate == 0 {
				chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_replace", "started", "sandbox_id", sandboxID, "error", readyErr)
			}
			sandboxID = ""
			continue
		}
		if scoped {
			_, err := l.Queries.UpsertFCE2BSandboxSession(ctx, db.UpsertFCE2BSandboxSessionParams{
				WorkspaceID: rt.WorkspaceID, RuntimeID: rt.ID, ScopeType: scope.typ, ScopeID: scope.id,
				SandboxID: sandboxID, Template: template,
				ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
			})
			if err != nil {
				if coldStart {
					l.releaseUnusedSandbox(ctx, sandboxID, trace)
				}
				return "", coldStart, fmt.Errorf("record FC/E2B sandbox session: %w", err)
			}
		}
		return sandboxID, coldStart, nil
	}
	return "", coldStart, lastErr
}

func (l *FCE2BLauncher) createSandbox(ctx context.Context, template string) (sandboxID string, resultErr error) {
	finish := startupobs.Start(ctx, "fc_create")
	defer func() { finish(resultErr) }()
	template = strings.TrimSpace(template)
	if template == "" {
		return "", errors.New("FC/E2B runtime has no template")
	}
	args := []string{
		"sandbox", "create",
		"--detach",
		"--timeout", strconv.Itoa(l.sandboxTaskTimeoutSeconds()),
		"--lifecycle.ontimeout", "kill",
		template,
	}
	for attempt := 1; attempt <= fcE2BSandboxCreateMaxAttempts; attempt++ {
		out, err := l.runE2BCommand(ctx, args)
		if err == nil {
			id, parseErr := parseE2BSandboxID(out)
			if parseErr != nil {
				responseErr := errors.New("FC/E2B sandbox create returned no sandbox id")
				return "", withRuntimeStartUserDetail(responseErr, responseErr.Error())
			}
			return id, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !isFCE2BSandboxCapacityRateLimitText(err.Error()) || attempt == fcE2BSandboxCreateMaxAttempts {
			createErr := fmt.Errorf("FC/E2B sandbox create failed: %w", err)
			return "", withRuntimeStartUserDetail(createErr, createErr.Error())
		}
		baseDelay := time.Second << (attempt - 1)
		delay := l.sandboxCreateRetryDelay(baseDelay)
		slog.Warn("FC/E2B sandbox create capacity limited; retry scheduled",
			"backend", SandboxBackendAliyunFC,
			"stage", "sandbox_create",
			"error_code", "FCE2B-SANDBOX-CAPACITY-429",
			"error", redact.Text(err.Error()),
			"attempt", attempt,
			"max_attempts", fcE2BSandboxCreateMaxAttempts,
			"retry_delay_ms", delay.Milliseconds(),
		)
		if err := l.sleepBeforeSandboxCreateRetry(ctx, delay); err != nil {
			return "", err
		}
	}
	exhaustedErr := errors.New("FC/E2B sandbox create exhausted retries")
	return "", withRuntimeStartUserDetail(exhaustedErr, exhaustedErr.Error())
}

func isFCE2BSandboxCapacityRateLimitText(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "429") &&
		strings.Contains(lower, "resourceexhausted") &&
		strings.Contains(lower, "function concurrent request count exceeded")
}

func (l *FCE2BLauncher) sandboxCreateRetryDelay(base time.Duration) time.Duration {
	if l != nil && l.jitter != nil {
		return l.jitter(base)
	}
	return runtimeStartRetryJitter(base)
}

func (l *FCE2BLauncher) sleepBeforeSandboxCreateRetry(ctx context.Context, delay time.Duration) error {
	if l != nil && l.sleep != nil {
		return l.sleep(ctx, delay)
	}
	return sleepWithContext(ctx, delay)
}

func runtimeStartRetryJitter(base time.Duration) time.Duration {
	return time.Duration(float64(base) * (0.8 + rand.Float64()*0.4))
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type boundedExecKey struct{}

func (l *FCE2BLauncher) checkSandboxReady(ctx context.Context, sandboxID string) error {
	if l.Config.QuickWins.BoundedReadyExec {
		ctx = context.WithValue(ctx, boundedExecKey{}, true)
		_, err := l.runE2BCommandWithTimeout(ctx, 5*time.Second, []string{"sandbox", "exec", sandboxID, "true"})
		return err
	}
	_, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", sandboxID, "true"})
	return err
}

func (l *FCE2BLauncher) waitSandboxReady(ctx context.Context, sandboxID string) error {
	deadline := time.Now().Add(l.Config.SandboxReadyTimeout)
	if l.Config.QuickWins.BoundedReadyExec {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	var lastErr error
	for {
		err := l.checkSandboxReady(ctx, sandboxID)
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			readyErr := fmt.Errorf("FC/E2B sandbox was not ready within %s: %w", l.Config.SandboxReadyTimeout, lastErr)
			return withRuntimeStartUserDetail(readyErr, readyErr.Error())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (l *FCE2BLauncher) execRunOnce(ctx context.Context, sandboxID string, rt db.AgentRuntime, launchMode fcE2BRunnerLaunchMode, taskID pgtype.UUID, token string, coldStart bool, extraEnv map[string]string) error {
	runtimeID := util.UUIDToString(rt.ID)
	healthPort := fcE2BHealthPortForTask(taskID)
	launch, err := fcE2BRunnerLaunchForMode(FCE2BRuntimeProvider(rt), launchMode)
	if err != nil {
		return err
	}
	for key := range extraEnv {
		if !isAllowedFCE2BRunnerExtraEnv(key) {
			return fmt.Errorf("FC/E2B runner environment key %q is not allowed", key)
		}
	}
	args := []string{
		"sandbox", "exec",
		"--background",
	}
	if launch.Mode == fcE2BRunnerLaunchRootLog {
		args = append(args,
			"--user", "root",
			"-e", "LD_PRELOAD=",
			"-e", "LD_LIBRARY_PATH=",
			"-e", "LD_AUDIT=",
			"-e", "GCONV_PATH=",
			"-e", "BASH_ENV=",
			"-e", "ENV=",
		)
	}
	dwsConfigDir := "/home/user/.dws"
	if overridden := strings.TrimSpace(extraEnv["DWS_CONFIG_DIR"]); overridden != "" {
		dwsConfigDir = overridden
	}
	llmURL, llmKey := l.Config.LLMBaseURL, l.Config.LLMAPIKey
	if os.Getenv("MULTICA_MODEL_GATEWAY_ENABLED") == "true" {
		llmURL = strings.TrimRight(l.Config.ServerURL, "/") + "/api/daemon/runtimes/" + runtimeID + "/model-tasks/" + util.UUIDToString(taskID) + "/v1"
		llmKey = token
	}
	args = append(args,
		"-e", "MULTICA_SERVER_URL="+l.Config.ServerURL,
		"-e", "MULTICA_DAEMON_TOKEN="+token,
		"-e", "MULTICA_RUNTIME_ID="+runtimeID,
		"-e", "MULTICA_TASK_ID="+util.UUIDToString(taskID),
		"-e", "MULTICA_DAEMON_ID="+rt.DaemonID.String,
		"-e", "MULTICA_AGENT_RUNTIME_NAME="+rt.Name,
		"-e", "MULTICA_CLOUD_SANDBOX_BACKEND="+string(SandboxBackendAliyunFC),
		"-e", "HOME="+launch.Home,
		"-e", "DWS_CONFIG_DIR="+dwsConfigDir,
		"-e", "OPENAI_BASE_URL="+llmURL,
		"-e", "OPENAI_API_KEY="+llmKey,
	)
	if coldStart {
		args = append(args, "-e", "MULTICA_FC_E2B_COLD_START=true")
	}
	for _, key := range sortedEnvKeys(extraEnv) {
		if key == "DWS_CONFIG_DIR" {
			continue
		}
		args = append(args, "-e", key+"="+extraEnv[key])
	}
	args = append(args,
		sandboxID,
		"--",
		launch.Command,
		"--runtime-id", runtimeID,
		"--provider", FCE2BRuntimeProvider(rt),
		"--health-port", strconv.Itoa(healthPort),
	)
	slog.Info("FC/E2B runner launch selected",
		"task_id", util.UUIDToString(taskID),
		"runtime_id", runtimeID,
		"sandbox_id", sandboxID,
		"provider", FCE2BRuntimeProvider(rt),
		"runner_protocol", string(launch.Mode),
	)
	if _, err := l.runE2BCommand(ctx, args); err != nil {
		execErr := fmt.Errorf("FC/E2B runner exec failed: %w", err)
		return withRuntimeStartUserDetail(execErr, execErr.Error())
	}
	return nil
}

func fcE2BHealthPortForTask(taskID pgtype.UUID) int {
	sum := 0
	for _, b := range taskID.Bytes {
		sum = (sum*31 + int(b)) % fcE2BRunOnceHealthPortSpan
	}
	return fcE2BRunOnceHealthPortBase + sum
}

func (l *FCE2BLauncher) runE2BCommand(ctx context.Context, args []string) (string, error) {
	timeout := l.Config.SandboxReadyTimeout
	if timeout <= 0 {
		timeout = defaultFCE2BSandboxReadyTimeout
	}
	return l.runE2BCommandWithTimeout(ctx, timeout, args)
}

func (l *FCE2BLauncher) runE2BCommandWithTimeout(ctx context.Context, timeout time.Duration, args []string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stage := "fc_command"
	if len(args) > 1 {
		stage += "_" + args[0] + "_" + args[1]
	}
	finish := startupobs.Start(ctx, stage)
	out, err := l.Runner.Run(cmdCtx, l.Config.CLIPath, args, l.e2bEnv())
	finish(err)
	return out, err
}

func (l *FCE2BLauncher) e2bEnv() []string {
	return fcE2BEnv(l.Config)
}

func fcE2BEnv(cfg FCE2BConfig) []string {
	return []string{
		"E2B_API_KEY=" + cfg.APIKey,
		"E2B_API_URL=" + cfg.APIURL,
		"E2B_DOMAIN=" + cfg.Domain,
	}
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isAllowedFCE2BRunnerExtraEnv(key string) bool {
	switch key {
	case "OPENAI_MODEL",
		"MULTICA_FS_ROOT",
		"MULTICA_WORKSPACE_FS_ROOT",
		"MULTICA_WORKSPACE_FS_ACCESS",
		"DSH_HOME",
		"MULTICA_DSH_WORKSPACE_ID",
		"MULTICA_DSH_AGENT_ID",
		"MULTICA_DSH_HOST_GENERATION",
		"MULTICA_DSH_SESSION_ID",
		"MULTICA_DSH_REQUEST_ID",
		"MULTICA_DSH_WORKDIR",
		"DWS_CONFIG_DIR",
		"GH_CONFIG_DIR",
		"XDG_CONFIG_HOME",
		"XDG_DATA_HOME",
		"XDG_STATE_HOME",
		"XDG_CACHE_HOME",
		"OPENCODE_CONFIG",
		"OPENCODE_CONFIG_DIR",
		"OPENCODE_CONFIG_CONTENT",
		"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT",
		"MULTICA_A2A_INVOCATION",
		llmTraceEnabledEnvKey,
		llmTraceSinkURLEnvKey,
		llmTraceTokenEnvKey,
		llmTraceExpiresAtEnvKey,
		fcE2BChatSessionIDEnvKey,
		"MULTICA_ISSUE_ID",
		chattrace.TraceIDEnvKey,
		chattrace.TraceStartedAtUnixMSEnvKey,
		protocol.SandboxSourceHostnameEnvKey,
		protocol.DingTalkStreamHostnameEnvKey,
		protocol.DingTalkStreamNodeIDEnvKey,
		protocol.DingTalkStreamConnectionIDEnvKey,
		protocol.AgentIdentityContextTokenEnvKey,
		protocol.DEAPDWSTokenEnvKey,
		protocol.SandboxRelayTokenEnvKey,
		"MULTICA_AGENT_IDENTITY_BASE_URL",
		"MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS",
		"MULTICA_RUNTIME_START_ATTEMPT_ID",
		"MULTICA_RUNTIME_START_PROTOCOL",
		"DWS_CLIENT_SECRET":
		return true
	default:
		return false
	}
}

// hardenCloudSandboxA2ARunnerEnv gives every admitted FC/ASB OpenCode A2A run a task-local
// DWS/GitHub/XDG/OpenCode state root before the v2 daemon starts. Runtime and
// daemon capability checks remain mandatory; this runner layer prevents state
// reuse across successive tasks in the same sandbox.
func hardenCloudSandboxA2ARunnerEnv(task db.AgentTaskQueue, runtime db.AgentRuntime, env map[string]string) map[string]string {
	if !IsA2ATaskOrigin(task.Context) {
		return env
	}
	metadata, err := ParseCloudSandboxRuntime(runtime)
	if err != nil || (metadata.SandboxBackend != SandboxBackendAliyunFC && metadata.SandboxBackend != SandboxBackendASB) || metadata.Provider != "opencode" {
		return env
	}
	if env == nil {
		env = make(map[string]string)
	}
	root := filepath.Join(fcE2BA2AIsolationRoot, util.UUIDToString(task.ID))
	xdgRoot := filepath.Join(root, "xdg")
	env["DWS_CONFIG_DIR"] = root
	env["GH_CONFIG_DIR"] = filepath.Join(root, "gh")
	env["XDG_CONFIG_HOME"] = filepath.Join(xdgRoot, "config")
	env["XDG_DATA_HOME"] = filepath.Join(xdgRoot, "data")
	env["XDG_STATE_HOME"] = filepath.Join(xdgRoot, "state")
	env["XDG_CACHE_HOME"] = filepath.Join(xdgRoot, "cache")
	env["OPENCODE_CONFIG_DIR"] = filepath.Join(xdgRoot, "config", "opencode")
	env["OPENCODE_CONFIG"] = "/home/user/.config/opencode/opencode.json"
	env["OPENCODE_CONFIG_CONTENT"] = ""
	env["OPENCODE_DISABLE_CLAUDE_CODE_PROMPT"] = "true"
	env["MULTICA_A2A_INVOCATION"] = "1"
	return env
}

func fcE2BTemplateForRuntime(rt db.AgentRuntime) (string, error) {
	var metadata struct {
		TemplateID string `json:"template_id"`
	}
	if len(rt.Metadata) > 0 {
		_ = json.Unmarshal(rt.Metadata, &metadata)
	}
	templateID := strings.TrimSpace(metadata.TemplateID)
	if templateID == "" {
		return "", errors.New("FC/E2B runtime has no template_id")
	}
	return templateID, nil
}

func (l *FCE2BLauncher) failLaunch(
	ctx context.Context,
	task db.AgentTaskQueue,
	attemptID pgtype.UUID,
	failure RuntimeStartFailure,
) error {
	if cause := context.Cause(ctx); errors.Is(cause, errRuntimeLaunchLeaseLost) || errors.Is(cause, errRuntimeLaunchShutdown) {
		return cause
	}
	_, err := l.Tasks.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, attemptID, failure)
	if err != nil {
		return err
	}
	return fmt.Errorf("FC/E2B launch failed: %s (%s)", failure.Code, failure.InternalDetail)
}

var e2bSandboxIDPattern = regexp.MustCompile(`Sandbox created with ID ([A-Za-z0-9_-]+) using template`)

const runtimeStartFailurePersistTimeout = 10 * time.Second

func parseE2BSandboxID(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", errors.New("empty sandbox output")
	}
	if match := e2bSandboxIDPattern.FindStringSubmatch(trimmed); len(match) == 2 {
		return match[1], nil
	}
	return "", errors.New("unrecognized sandbox output")
}

func (s *TaskService) FailTaskRuntimeStart(
	ctx context.Context,
	taskID pgtype.UUID,
	runtimeID pgtype.UUID,
	attemptID pgtype.UUID,
	failure RuntimeStartFailure,
) (*db.AgentTaskQueue, error) {
	if cause := context.Cause(ctx); errors.Is(cause, errRuntimeLaunchLeaseLost) || errors.Is(cause, errRuntimeLaunchShutdown) {
		return nil, cause
	}
	// A request-scoped A2A identity intentionally cancels Runtime startup when
	// its HTTP request ends. Persist that cancellation as a terminal task state
	// with an independent bounded context; otherwise the same canceled context
	// aborts this transaction and leaves the task indefinitely queued.
	persistCtx, cancelPersist := context.WithTimeout(
		context.Background(),
		runtimeStartFailurePersistTimeout,
	)
	defer cancelPersist()
	ctx = persistCtx
	if attemptID.Valid {
		failure = runtimeStartFailureAtLastStage(ctx, s.Queries, db.AgentTaskRuntimeStartAttempt{
			ID:        attemptID,
			TaskID:    taskID,
			RuntimeID: runtimeID,
		}, failure)
	}
	userMessage := FormatRuntimeStartUserMessage(taskID, failure)
	var task db.AgentTaskQueue
	var assistantMsg *db.ChatMessage
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if attemptID.Valid {
			t, err := qtx.FailAgentTaskForRuntimeStartAttempt(ctx, db.FailAgentTaskForRuntimeStartAttemptParams{
				Error:     pgtype.Text{String: userMessage, Valid: true},
				TaskID:    taskID,
				RuntimeID: runtimeID,
				AttemptID: attemptID,
			})
			if err != nil {
				return err
			}
			task = t
			status := "failed"
			if strings.HasSuffix(failure.Code, "-TIMEOUT") {
				status = "timed_out"
			}
			if _, err := qtx.FailAgentTaskRuntimeStartAttempt(ctx, db.FailAgentTaskRuntimeStartAttemptParams{
				Status:      status,
				Stage:       failure.Phase,
				ErrorCode:   failure.Code,
				ErrorDetail: failure.InternalDetail,
				ID:          attemptID,
				TaskID:      taskID,
				RuntimeID:   runtimeID,
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("fail runtime start attempt: %w", err)
			}
		} else {
			t, err := qtx.FailAgentTaskRuntimeStart(ctx, db.FailAgentTaskRuntimeStartParams{
				ID:        taskID,
				RuntimeID: runtimeID,
				Error:     pgtype.Text{String: userMessage, Valid: true},
			})
			if err != nil {
				return err
			}
			task = t
		}

		seq := int32(1)
		messages, err := qtx.ListTaskMessages(ctx, taskID)
		if err != nil {
			return err
		}
		for _, msg := range messages {
			if msg.Seq >= seq {
				seq = msg.Seq + 1
			}
		}
		_, err = qtx.CreateTaskMessage(ctx, db.CreateTaskMessageParams{
			TaskID:  taskID,
			Seq:     seq,
			Type:    "error",
			Content: pgtype.Text{String: userMessage, Valid: true},
		})
		if err != nil {
			return err
		}
		if task.ChatSessionID.Valid {
			row, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
				ChatSessionID: task.ChatSessionID,
				Role:          "assistant",
				Content:       userMessage,
				TaskID:        task.ID,
				FailureReason: pgtype.Text{String: "runtime_start_failed", Valid: true},
				ElapsedMs:     computeChatElapsedMs(task),
			})
			if err != nil {
				return fmt.Errorf("create runtime-start-failed chat message: %w", err)
			}
			assistantMsg = &row
			// Unread tracking moved to the last_read_at read cursor (migration
			// 151): a fresh assistant message is unread by construction, so the
			// old SetUnreadSinceIfNull stamp is no longer needed here.
		}
		return nil
	}); err != nil {
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil && errors.Is(err, pgx.ErrNoRows) {
			return &existing, nil
		}
		return nil, fmt.Errorf("fail task runtime start: %w", err)
	}

	slog.Warn("task failed before runtime start",
		"task_id", util.UUIDToString(task.ID),
		"runtime_id", util.UUIDToString(task.RuntimeID),
		"agent_id", util.UUIDToString(task.AgentID),
		"runtime_start_attempt_id", util.UUIDToString(attemptID),
		"backend", failure.Backend,
		"stage", failure.Phase,
		"error_code", failure.Code,
		"retryable", failure.Retryable,
		"error", failure.InternalDetail,
		"failure_reason", "runtime_start_failed",
	)
	s.captureTaskFailed(ctx, task)
	s.ReconcileAgentStatus(ctx, task.AgentID)
	if task.ChatSessionID.Valid {
		s.broadcastChatDone(ctx, task, assistantMsg, false)
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskFailed, task)
	return &task, nil
}
