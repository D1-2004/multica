package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/sandboxrelay"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	FCE2BMetadataKind = "fc-e2b"
	// FCE2BProvider is the first provider selected when a verified template
	// manifest declares Hermes support and the request omits a provider.
	FCE2BProvider = "hermes"

	fcE2BScopeTypeChat  = "chat"
	fcE2BScopeTypeIssue = "issue"

	defaultFCE2BCLIPath             = "e2b"
	defaultFCE2BTimeoutSeconds      = 3600
	defaultFCE2BSandboxReadyTimeout = 60 * time.Second
	defaultAgentIdentityTimeout     = 10 * time.Second
	fcE2BDaemonTokenTTL             = time.Hour
	fcE2BRunnerClaimTimeout         = 2 * time.Minute
	fcE2BRunnerClaimPollInterval    = 500 * time.Millisecond
	fcE2BRunOnceHealthPortBase      = 20000
	fcE2BRunOnceHealthPortSpan      = 30000
	fcE2BRootRunnerInstallDir       = "/usr/local/libexec"
	fcE2BLegacyRunnerInstallDir     = "/usr/local/bin"
	fcE2BTemplateManifestVersion    = 2
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
	fcE2BRunnerClaimStalled  fcE2BRunnerClaimState = "stalled"
)

type FCE2BConfig struct {
	Enabled              bool
	Template             string
	ServerURL            string
	APIKey               string
	APIURL               string
	Domain               string
	LLMBaseURL           string
	LLMAPIKey            string
	LLMModels            []string
	AgentIdentityBaseURL string
	AgentIdentityTimeout time.Duration
	DWSClientSecret      string
	CLIPath              string
	TimeoutSeconds       int
	SandboxReadyTimeout  time.Duration
	ParseError           error
}

func FCE2BConfigFromEnv() FCE2BConfig {
	cfg := FCE2BConfig{
		Enabled:              envBool("MULTICA_FC_E2B_ENABLED"),
		Template:             strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TEMPLATE")),
		ServerURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_SERVER_URL")), "/"),
		APIKey:               strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_KEY")),
		APIURL:               strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_URL")), "/"),
		Domain:               strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_DOMAIN")),
		LLMBaseURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:            strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_API_KEY")),
		AgentIdentityBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_BASE_URL")), "/"),
		AgentIdentityTimeout: defaultAgentIdentityTimeout,
		DWSClientSecret:      strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET")),
		CLIPath:              strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_CLI_PATH")),
		TimeoutSeconds:       defaultFCE2BTimeoutSeconds,
		SandboxReadyTimeout:  defaultFCE2BSandboxReadyTimeout,
	}
	models, err := parseFCE2BModels(os.Getenv("MULTICA_FC_E2B_OPENAI_MODELS"))
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

func envBool(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	return strings.EqualFold(v, "true") || v == "1"
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
	if rt.RuntimeMode != "cloud" {
		return false
	}
	var metadata struct {
		Kind string `json:"kind"`
	}
	if len(rt.Metadata) == 0 || json.Unmarshal(rt.Metadata, &metadata) != nil {
		return false
	}
	return metadata.Kind == FCE2BMetadataKind
}

// FCE2BSupportedProviders lists the agent providers an FC/E2B sandbox runtime
// can run. A template image may ship several of these CLIs side by side; the
// runtime's provider is chosen at creation time. FCE2BProvider (hermes) is
// the default when a request names none.
var FCE2BSupportedProviders = []string{FCE2BProvider, "opencode", "pi"}

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

// FCE2BTemplateSupportsProvider reports whether the verified template manifest
// declares provider. Template identifiers are deliberately ignored.
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
// the verified manifest. A template without one is not usable for creation.
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
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

// IsFCE2BTemplateReady reports whether the template catalog has completed the
// build. Template rotation never submits a sandbox from a transitional state.
func IsFCE2BTemplateReady(template FCE2BTemplate) bool {
	return strings.EqualFold(strings.TrimSpace(template.Status), "ready")
}

// IsFCE2BTemplatePublished reports whether the current build carries a valid
// current manifest alias required for safe runtime creation and rotation.
func IsFCE2BTemplatePublished(template FCE2BTemplate) bool {
	return template.ManifestVersion == fcE2BTemplateManifestVersion &&
		strings.TrimSpace(template.BuildID) != "" &&
		strings.TrimSpace(template.RunnerProtocol) == string(fcE2BRunnerLaunchRootLog) &&
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
		if provider == "pi" {
			return fcE2BRunnerLaunch{}, errors.New("Pi FC/E2B runtimes require the root-log-v1 runner protocol")
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
	if !IsFCE2BRuntime(rt) {
		return false
	}
	capability = strings.ToLower(strings.TrimSpace(capability))
	if capability == "" {
		return false
	}
	var metadata struct {
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(rt.Metadata, &metadata) != nil {
		return false
	}
	for _, candidate := range metadata.Capabilities {
		if strings.ToLower(strings.TrimSpace(candidate)) == capability {
			return true
		}
	}
	return false
}

// FCE2BRuntimeTemplateChannel treats rows created before stable-channel
// metadata existed as stable-managed.
func FCE2BRuntimeTemplateChannel(rt db.AgentRuntime) string {
	if !IsFCE2BRuntime(rt) {
		return ""
	}
	var metadata struct {
		TemplateChannel string `json:"template_channel"`
	}
	if json.Unmarshal(rt.Metadata, &metadata) != nil {
		return ""
	}
	channel := strings.ToLower(strings.TrimSpace(metadata.TemplateChannel))
	if channel == "" {
		return "stable"
	}
	return channel
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string) (string, error)
}

type FCE2BTemplate struct {
	ID                string            `json:"id,omitempty"`
	BuildID           string            `json:"build_id,omitempty"`
	SourceRevision    string            `json:"source_revision,omitempty"`
	Name              string            `json:"name,omitempty"`
	Template          string            `json:"template"`
	Status            string            `json:"status,omitempty"`
	CreatedAt         string            `json:"created_at,omitempty"`
	UpdatedAt         string            `json:"updated_at,omitempty"`
	ManifestVersion   int               `json:"manifest_version"`
	Providers         []string          `json:"providers"`
	Capabilities      []string          `json:"capabilities"`
	ComponentVersions map[string]string `json:"component_versions"`
	RunnerProtocol    string            `json:"runner_protocol"`
	Metadata          map[string]any    `json:"metadata,omitempty"`
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("command failed: %w: %s", err, redact.Text(output.String()))
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
	templates, err := parseFCE2BTemplates(out)
	if err != nil {
		return nil, err
	}
	sort.Slice(templates, func(i, j int) bool {
		return templates[i].UpdatedAt > templates[j].UpdatedAt
	})
	return templates, nil
}

func parseFCE2BTemplates(output string) ([]FCE2BTemplate, error) {
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
			ID:        firstString(obj, "id", "template_id", "templateID"),
			BuildID:   firstString(obj, "build_id", "buildID"),
			Status:    firstString(obj, "status", "state", "buildStatus", "build_status"),
			CreatedAt: firstString(obj, "created_at", "createdAt", "create_time", "createTime"),
			UpdatedAt: firstString(obj, "updated_at", "updatedAt", "update_time", "updateTime"),
			Metadata:  obj,
		}
		var manifestAlias string
		for _, alias := range stringValues(obj, "aliases", "names") {
			candidate := t
			published, err := applyFCE2BTemplateManifestAlias(&candidate, alias)
			if err != nil {
				return nil, err
			}
			if !published {
				continue
			}
			if manifestAlias != "" && manifestAlias != alias {
				return nil, fmt.Errorf("FC/E2B template %s has multiple manifest aliases", t.ID)
			}
			manifestAlias = alias
			t = candidate
		}
		if manifestAlias == "" {
			continue
		}
		templates = append(templates, t)
	}
	return templates, nil
}

var fcE2BTemplateManifestAliasPattern = regexp.MustCompile(`^multica-m([12])-h([0-9]+_[0-9]+_[0-9]+)-o([0-9]+_[0-9]+_[0-9]+)-p([0-9]+_[0-9]+_[0-9]+)-d([0-9]+_[0-9]+_[0-9]+)b([0-9]+)-c(di|dim)-r1-([0-9a-f]{6})$`)

func applyFCE2BTemplateManifestAlias(template *FCE2BTemplate, alias string) (bool, error) {
	if template == nil {
		return false, errors.New("FC/E2B template is nil")
	}
	if strings.TrimSpace(template.BuildID) == "" {
		return false, nil
	}
	alias = strings.TrimSpace(alias)
	matches := fcE2BTemplateManifestAliasPattern.FindStringSubmatch(alias)
	if matches == nil {
		return false, nil
	}
	manifestVersion, err := strconv.Atoi(matches[1])
	if err != nil {
		return false, nil
	}
	capabilityCode := matches[7]
	if (manifestVersion == 1 && capabilityCode != "di") || (manifestVersion == 2 && capabilityCode != "dim") {
		return false, nil
	}
	hermesVersion, hermesOK := parseFCE2BUnderscoreSemver(matches[2])
	opencodeVersion, opencodeOK := parseFCE2BUnderscoreSemver(matches[3])
	piVersion, piOK := parseFCE2BUnderscoreSemver(matches[4])
	dwsVersion, dwsOK := parseFCE2BUnderscoreSemver(matches[5])
	if !hermesOK || !opencodeOK || !piOK || !dwsOK || !isCanonicalNumericIdentifier(matches[6]) {
		return false, nil
	}
	template.Name = alias
	template.Template = alias
	template.ManifestVersion = manifestVersion
	template.Providers = []string{"hermes", "opencode", "pi"}
	template.Capabilities = []string{"dws", "dws.im_event"}
	if manifestVersion >= 2 {
		template.Capabilities = append(template.Capabilities, "mcp")
	}
	template.ComponentVersions = map[string]string{
		"hermes":   hermesVersion,
		"opencode": "v" + opencodeVersion,
		"pi":       piVersion,
		"dws":      "v" + dwsVersion + "-beta." + matches[6],
	}
	template.RunnerProtocol = string(fcE2BRunnerLaunchRootLog)
	template.SourceRevision = matches[8]
	return true, nil
}

func parseFCE2BUnderscoreSemver(value string) (string, bool) {
	parts := strings.Split(value, "_")
	if len(parts) != 3 {
		return "", false
	}
	for _, part := range parts {
		if !isCanonicalNumericIdentifier(part) {
			return "", false
		}
	}
	return strings.Join(parts, "."), true
}

func isCanonicalNumericIdentifier(value string) bool {
	if value == "0" {
		return true
	}
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, digit := range value[1:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

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
	Queries            *db.Queries
	Tasks              *TaskService
	Config             FCE2BConfig
	Runner             CommandRunner
	AgentIdentity      AgentIdentityContextCreator
	IdentityBindings   AgentIdentityBindingReader
	SandboxRelaySigner SandboxRelayTokenSigner

	// Pool backs the cross-replica runtime and sandbox advisory locks.
	Pool *pgxpool.Pool
}

var ErrFCE2BRuntimeRequired = errors.New("runtime is not an FC/E2B cloud runtime")
var ErrFCE2BTemplateProviderUnsupported = errors.New("FC/E2B template does not support the runtime provider")

type FCE2BRuntimeTemplateUpdateResult struct {
	Runtime                 db.AgentRuntime
	PreviousTemplate        string
	PreviousTemplateID      string
	PreviousTemplateBuildID string
	InvalidatedSandboxCount int64
	Changed                 bool
}

type AgentIdentityContextCreator interface {
	CreateContext(context.Context, agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error)
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
	}
}

// SetPool wires the database pool required for cross-replica runtime rotation
// and sandbox creation coordination.
func (l *FCE2BLauncher) SetPool(pool *pgxpool.Pool) {
	if l != nil {
		l.Pool = pool
	}
}

func (l *FCE2BLauncher) SetSandboxRelaySigner(signer SandboxRelayTokenSigner) {
	if l != nil {
		l.SandboxRelaySigner = signer
	}
}

// UpdateRuntimeTemplate atomically rotates an FC/E2B runtime to a catalogued,
// ready template and makes every previously reusable sandbox session stale.
// The exclusive runtime advisory lock conflicts with LaunchTask's shared lock,
// so the returned runtime is the cutover boundary for later launches.
func (l *FCE2BLauncher) UpdateRuntimeTemplate(ctx context.Context, runtimeID pgtype.UUID, selected FCE2BTemplate) (FCE2BRuntimeTemplateUpdateResult, error) {
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
		return FCE2BRuntimeTemplateUpdateResult{}, errors.New("FC/E2B template has no verified manifest")
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
	previousTemplateBuildID, _ := metadata["template_build_id"].(string)
	result := FCE2BRuntimeTemplateUpdateResult{
		Runtime:                 runtime,
		PreviousTemplate:        strings.TrimSpace(previousTemplate),
		PreviousTemplateID:      strings.TrimSpace(previousTemplateID),
		PreviousTemplateBuildID: strings.TrimSpace(previousTemplateBuildID),
	}
	managedMetadataChanged := false
	for key, value := range managedMetadata {
		if metadata[key] != value {
			managedMetadataChanged = true
			break
		}
	}
	if result.PreviousTemplateID == selected.ID && result.PreviousTemplateBuildID == selected.BuildID && !managedMetadataChanged {
		if err := tx.Commit(ctx); err != nil {
			return FCE2BRuntimeTemplateUpdateResult{}, fmt.Errorf("commit idempotent FC/E2B template update: %w", err)
		}
		return result, nil
	}

	metadata["template"] = selected.Template
	metadata["template_id"] = selected.ID
	metadata["template_build_id"] = selected.BuildID
	metadata["template_name"] = selected.Name
	metadata["template_status"] = selected.Status
	metadata["manifest_version"] = selected.ManifestVersion
	metadata["capabilities"] = FCE2BTemplateCapabilities(provider, selected)
	metadata["component_versions"] = selected.ComponentVersions
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

// VerifyStableTemplate rebuilds trust in a catalog entry from a fresh native
// sandbox. A READY catalog status is insufficient because the smoke test runs
// after the E2B build becomes ready.
func (l *FCE2BLauncher) VerifyStableTemplate(ctx context.Context, selected FCE2BTemplate) (map[string]any, error) {
	if l == nil {
		return nil, errors.New("FC/E2B launcher is unavailable")
	}
	if !IsFCE2BTemplateReady(selected) || !IsFCE2BTemplatePublished(selected) {
		return nil, errors.New("FC/E2B template is not ready with a verified manifest alias")
	}
	sandboxID, err := l.createSandbox(ctx, selected.Template)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, killErr := l.runE2BCommand(context.Background(), []string{"sandbox", "kill", sandboxID})
		if killErr != nil {
			slog.Warn("failed to kill FC/E2B stable validation sandbox", "sandbox_id", sandboxID, "error", killErr)
		}
	}()
	if err := l.waitSandboxReady(ctx, sandboxID); err != nil {
		return nil, err
	}
	if _, err := l.runE2BCommand(ctx, []string{
		"sandbox", "exec",
		"--user", "user",
		sandboxID,
		"--",
		"/usr/local/bin/runtime-smoke-test",
	}); err != nil {
		return nil, fmt.Errorf("runtime-smoke-test failed: %w", err)
	}
	out, err := l.runE2BCommand(ctx, []string{
		"sandbox", "exec",
		"--user", "user",
		sandboxID,
		"--",
		"/bin/cat", "/usr/local/share/multica/runtime-manifest.json",
	})
	if err != nil {
		return nil, fmt.Errorf("read runtime manifest: %w", err)
	}
	var manifest struct {
		SchemaVersion     int               `json:"schema_version"`
		Providers         []string          `json:"providers"`
		Capabilities      []string          `json:"capabilities"`
		ComponentVersions map[string]string `json:"component_versions"`
		RunnerProtocol    string            `json:"runner_protocol"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &manifest); err != nil {
		return nil, fmt.Errorf("decode runtime manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 ||
		!slices.Equal(manifest.Providers, []string{"hermes", "opencode", "pi"}) ||
		!slices.Equal(manifest.Capabilities, []string{"dws", "dws.im_event", "mcp"}) ||
		manifest.RunnerProtocol != string(fcE2BRunnerLaunchRootLog) {
		return nil, errors.New("runtime manifest does not satisfy the stable channel contract")
	}
	for component, expected := range selected.ComponentVersions {
		if manifest.ComponentVersions[component] != expected {
			return nil, fmt.Errorf("runtime manifest component %s does not match catalog alias", component)
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode verified runtime manifest: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, fmt.Errorf("normalize verified runtime manifest: %w", err)
	}
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
		return l.failLaunch(ctx, task, "invalid task trace: "+traceErr.Error())
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_launch", "started",
		"task_id", taskID,
		"runtime_id", runtimeID,
	)
	if !l.Config.Enabled {
		return l.failLaunch(ctx, task, "FC/E2B runtime is disabled")
	}
	if err := l.Config.Validate(); err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}

	runtimeLockConn, releaseRuntimeLock, err := l.lockRuntimeShared(ctx, task.RuntimeID)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
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
	submission, deferred, submitErr := lockedLauncher.submitTaskUnderRuntimeLock(ctx, task, runtimeLockConn, trace)
	releaseRuntimeLock()
	runtimeLockHeld = false
	if submitErr != nil {
		return l.failLaunch(ctx, task, submitErr.Error())
	}
	if deferred {
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
	claimState, err := l.waitForRunOnceClaim(ctx, task)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	switch claimState {
	case fcE2BRunnerClaimObserved:
		slog.Info("FC/E2B run-once claim observed", "task_id", taskID, "runtime_id", runtimeID, "sandbox_id", submission.sandboxID)
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_claim", "observed", "task_id", taskID, "sandbox_id", submission.sandboxID)
	case fcE2BRunnerClaimBlocked:
		slog.Info("FC/E2B run-once claim blocked by active task", "task_id", taskID, "runtime_id", runtimeID, "sandbox_id", submission.sandboxID)
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_claim", "blocked", "task_id", taskID, "sandbox_id", submission.sandboxID)
	case fcE2BRunnerClaimStalled:
		return l.failLaunch(ctx, task, fmt.Sprintf("FC/E2B runner did not claim task within %s after sandbox exec", fcE2BRunnerClaimTimeout))
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

func (l *FCE2BLauncher) submitTaskUnderRuntimeLock(ctx context.Context, task db.AgentTaskQueue, runtimeLockConn *pgxpool.Conn, trace chattrace.Trace) (fcE2BLaunchSubmission, bool, error) {
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
	template, err := fcE2BTemplateForRuntime(runtime, l.Config.Template)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, err
	}
	slog.Info("FC/E2B launch template resolved", "task_id", taskID, "runtime_id", runtimeID, "template", template)
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_template", "resolved",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"template", template,
	)

	tasks, err := l.Queries.ListAgentTasks(ctx, task.AgentID)
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
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_resolve", "started", "task_id", taskID)
	sandboxID, coldStart, err := l.resolveSandboxOnConnection(ctx, runtime, scope, scoped, template, runtimeLockConn, trace)
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
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_resolve", "succeeded",
		"task_id", taskID,
		"sandbox_id", sandboxID,
		"cold_start", coldStart,
		"stage_elapsed_ms", time.Since(sandboxResolveStarted).Milliseconds(),
	)

	runnerProbeStarted := time.Now()
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

	extraEnv, err := l.extraEnvForTask(ctx, task, runtime, sandboxID)
	if err != nil {
		return fcE2BLaunchSubmission{}, false, err
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
	if extraEnv == nil {
		extraEnv = make(map[string]string)
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

func (l *FCE2BLauncher) waitForRunOnceClaim(ctx context.Context, task db.AgentTaskQueue) (fcE2BRunnerClaimState, error) {
	deadline := time.NewTimer(fcE2BRunnerClaimTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(fcE2BRunnerClaimPollInterval)
	defer ticker.Stop()
	checkedInitialBlocker := false

	for {
		current, err := l.Queries.GetAgentTask(ctx, task.ID)
		if err != nil {
			return "", fmt.Errorf("verify FC/E2B runner claim: %w", err)
		}
		if current.Status != "queued" {
			return fcE2BRunnerClaimObserved, nil
		}
		if !checkedInitialBlocker {
			checkedInitialBlocker = true
			tasks, err := l.Queries.ListAgentTasks(ctx, current.AgentID)
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
			fresh, err := l.Queries.GetAgentTask(ctx, task.ID)
			if err != nil {
				return "", fmt.Errorf("verify FC/E2B runner claim: %w", err)
			}
			if fresh.Status != "queued" {
				return fcE2BRunnerClaimObserved, nil
			}
			tasks, err := l.Queries.ListAgentTasks(ctx, fresh.AgentID)
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
	agentRow, err := l.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return nil, fmt.Errorf("load agent for FC/E2B launch: %w", err)
	}
	model, err := l.Config.ModelForAgent(agentRow.Model.String)
	if err != nil {
		return nil, err
	}
	env, err := sandboxSourceEnv(task.Context)
	if err != nil {
		return nil, err
	}
	env["OPENAI_MODEL"] = model
	traceEnv, err := fcE2BTaskTraceEnv(task)
	if err != nil {
		return nil, err
	}
	for key, value := range traceEnv {
		env[key] = value
	}
	agentIdentityEnv, err := l.identityEnvForTask(ctx, task, runtime, sandboxID)
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

func (l *FCE2BLauncher) identityEnvForTask(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtime db.AgentRuntime,
	sandboxID string,
) (map[string]string, error) {
	hasDWSCapability := FCE2BRuntimeHasCapability(runtime, "dws")
	if hasDWSCapability {
		if l.IdentityBindings == nil {
			return nil, errors.New("Agent identity binding reader is not configured")
		}
		identity, err := l.IdentityBindings.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
			WorkspaceID: runtime.WorkspaceID,
			AgentID:     task.AgentID,
		})
		if err == nil {
			if l.AgentIdentity == nil {
				return nil, errors.New("Agent Identity HSF client is not configured")
			}
			taskID := util.UUIDToString(task.ID)
			result, err := l.AgentIdentity.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
				RequestID:   "multica-task-" + taskID,
				TaskID:      taskID,
				AgentID:     util.UUIDToString(task.AgentID),
				RuntimeType: "E2B",
				RuntimeID:   sandboxID,
				Reason:      "Multica Agent DWS authorization",
				Source: map[string]string{
					"app":             "dt-fde-multica",
					"identity_source": "agent_binding_fallback",
				},
				UID:        identity.DwsUid,
				OrgID:      identity.OrgID,
				TTLSeconds: 900,
			})
			if err != nil {
				return nil, fmt.Errorf("create Agent Identity context for task: %w", err)
			}
			slog.Info("FC/E2B task identity selected",
				"task_id", taskID,
				"identity_source", "agent_binding_fallback",
			)
			return fcE2BAgentIdentityEnvForToken(result.ContextToken, l.Config)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load Agent DingTalk identity: %w", err)
		}
	}

	prepared, err := fcE2BAgentIdentityExtraEnv(task, l.Config)
	if err != nil && !errors.Is(err, errAgentIdentityContextTokenRefreshRequired) {
		return nil, err
	}
	if len(prepared) > 0 {
		slog.Info("FC/E2B task identity selected",
			"task_id", util.UUIDToString(task.ID),
			"identity_source", "prepared_context_token",
		)
		return prepared, nil
	}
	refreshRequired := errors.Is(err, errAgentIdentityContextTokenRefreshRequired)
	if refreshRequired {
		slog.Info("FC/E2B cached task identity requires refresh",
			"task_id", util.UUIDToString(task.ID),
			"identity_source", "task_context_cache",
		)
	}
	if !hasDWSCapability {
		if refreshRequired {
			return nil, errors.New("DWS capability is required to refresh the cached Agent Identity ContextToken")
		}
		return nil, nil
	}
	if refreshRequired {
		return nil, errors.New("Multica Agent DingTalk identity binding is required to refresh the cached ContextToken")
	}
	return nil, nil
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
	if cfg.AgentIdentityBaseURL == "" {
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
		"MULTICA_AGENT_IDENTITY_BASE_URL":        cfg.AgentIdentityBaseURL,
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
			readyStarted := time.Now()
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "checking", "sandbox_id", session.SandboxID, "cold_start", false)
			if err := l.checkSandboxReady(ctx, session.SandboxID); err != nil {
				chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "failed", "sandbox_id", session.SandboxID, "cold_start", false, "stage_elapsed_ms", time.Since(readyStarted).Milliseconds(), "error", err)
				_ = l.Queries.MarkFCE2BSandboxSessionStale(ctx, db.MarkFCE2BSandboxSessionStaleParams{
					RuntimeID: rt.ID,
					ScopeType: scope.typ,
					ScopeID:   scope.id,
					SandboxID: session.SandboxID,
				})
				return "", false, fmt.Errorf("FC/E2B warm sandbox %s is unavailable: %w", session.SandboxID, err)
			}
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "succeeded", "sandbox_id", session.SandboxID, "cold_start", false, "stage_elapsed_ms", time.Since(readyStarted).Milliseconds())
			return session.SandboxID, false, nil
		}
		if err != pgx.ErrNoRows {
			return "", false, fmt.Errorf("load FC/E2B sandbox session: %w", err)
		}
	}

	createStarted := time.Now()
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_create", "started", "template", template)
	sandboxID, err := l.createSandbox(ctx, template)
	if err != nil {
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_create", "failed", "template", template, "stage_elapsed_ms", time.Since(createStarted).Milliseconds(), "error", err)
		return "", true, err
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_create", "succeeded", "sandbox_id", sandboxID, "template", template, "stage_elapsed_ms", time.Since(createStarted).Milliseconds())
	readyStarted := time.Now()
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "waiting", "sandbox_id", sandboxID, "cold_start", true)
	if err := l.waitSandboxReady(ctx, sandboxID); err != nil {
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "failed", "sandbox_id", sandboxID, "cold_start", true, "stage_elapsed_ms", time.Since(readyStarted).Milliseconds(), "error", err)
		return "", true, err
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_ready", "succeeded", "sandbox_id", sandboxID, "cold_start", true, "stage_elapsed_ms", time.Since(readyStarted).Milliseconds())
	if scoped {
		_, err = l.Queries.UpsertFCE2BSandboxSession(ctx, db.UpsertFCE2BSandboxSessionParams{
			WorkspaceID: rt.WorkspaceID,
			RuntimeID:   rt.ID,
			ScopeType:   scope.typ,
			ScopeID:     scope.id,
			SandboxID:   sandboxID,
			Template:    template,
			ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Duration(l.Config.TimeoutSeconds) * time.Second), Valid: true},
		})
		if err != nil {
			return "", true, fmt.Errorf("record FC/E2B sandbox session: %w", err)
		}
	}
	return sandboxID, true, nil
}

func (l *FCE2BLauncher) createSandbox(ctx context.Context, template string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", errors.New("FC/E2B runtime has no template")
	}
	args := []string{
		"sandbox", "create",
		"--detach",
		"--timeout", strconv.Itoa(l.Config.TimeoutSeconds),
		"--lifecycle.ontimeout", "kill",
		template,
	}
	out, err := l.runE2BCommand(ctx, args)
	if err != nil {
		return "", fmt.Errorf("FC/E2B sandbox create failed: %w", err)
	}
	id, err := parseE2BSandboxID(out)
	if err != nil {
		return "", fmt.Errorf("FC/E2B sandbox create returned no sandbox id")
	}
	return id, nil
}

func (l *FCE2BLauncher) checkSandboxReady(ctx context.Context, sandboxID string) error {
	_, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", sandboxID, "true"})
	return err
}

func (l *FCE2BLauncher) waitSandboxReady(ctx context.Context, sandboxID string) error {
	deadline := time.Now().Add(l.Config.SandboxReadyTimeout)
	var lastErr error
	for {
		_, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", sandboxID, "true"})
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("FC/E2B sandbox was not ready within %s: %w", l.Config.SandboxReadyTimeout, lastErr)
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
	args = append(args,
		"-e", "MULTICA_SERVER_URL="+l.Config.ServerURL,
		"-e", "MULTICA_DAEMON_TOKEN="+token,
		"-e", "MULTICA_RUNTIME_ID="+runtimeID,
		"-e", "MULTICA_TASK_ID="+util.UUIDToString(taskID),
		"-e", "MULTICA_DAEMON_ID="+rt.DaemonID.String,
		"-e", "MULTICA_AGENT_RUNTIME_NAME="+rt.Name,
		"-e", "HOME="+launch.Home,
		"-e", "DWS_CONFIG_DIR=/home/user/.dws",
		"-e", "OPENAI_BASE_URL="+l.Config.LLMBaseURL,
		"-e", "OPENAI_API_KEY="+l.Config.LLMAPIKey,
	)
	if coldStart {
		args = append(args, "-e", "MULTICA_FC_E2B_COLD_START=true")
	}
	for _, key := range sortedEnvKeys(extraEnv) {
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
		return fmt.Errorf("FC/E2B runner exec failed: %w", err)
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
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return l.Runner.Run(cmdCtx, l.Config.CLIPath, args, l.e2bEnv())
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
		chattrace.TraceIDEnvKey,
		chattrace.TraceStartedAtUnixMSEnvKey,
		protocol.SandboxSourceHostnameEnvKey,
		protocol.DingTalkStreamHostnameEnvKey,
		protocol.DingTalkStreamNodeIDEnvKey,
		protocol.DingTalkStreamConnectionIDEnvKey,
		protocol.AgentIdentityContextTokenEnvKey,
		protocol.SandboxRelayTokenEnvKey,
		"MULTICA_AGENT_IDENTITY_BASE_URL",
		"MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS",
		"DWS_CLIENT_SECRET":
		return true
	default:
		return false
	}
}

func fcE2BTemplateForRuntime(rt db.AgentRuntime, configuredTemplate string) (string, error) {
	var metadata struct {
		Template string `json:"template"`
	}
	if len(rt.Metadata) > 0 {
		_ = json.Unmarshal(rt.Metadata, &metadata)
	}
	template := strings.TrimSpace(metadata.Template)
	if template == "" {
		template = strings.TrimSpace(configuredTemplate)
	}
	if template == "" {
		return "", errors.New("FC/E2B runtime has no template")
	}
	return template, nil
}

func (l *FCE2BLauncher) failLaunch(ctx context.Context, task db.AgentTaskQueue, msg string) error {
	if cause := context.Cause(ctx); errors.Is(cause, errRuntimeLaunchLeaseLost) {
		return cause
	}
	_, err := l.Tasks.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, msg)
	if err != nil {
		return err
	}
	return fmt.Errorf("FC/E2B launch failed: %s", redact.Text(msg))
}

var e2bSandboxIDPattern = regexp.MustCompile(`Sandbox created with ID ([A-Za-z0-9_-]+) using template`)

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

func (s *TaskService) FailTaskRuntimeStart(ctx context.Context, taskID, runtimeID pgtype.UUID, errMsg string) (*db.AgentTaskQueue, error) {
	errMsg = redact.Text(errMsg)
	var task db.AgentTaskQueue
	var assistantMsg *db.ChatMessage
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t, err := qtx.FailAgentTaskRuntimeStart(ctx, db.FailAgentTaskRuntimeStartParams{
			ID:        taskID,
			RuntimeID: runtimeID,
			Error:     pgtype.Text{String: errMsg, Valid: errMsg != ""},
		})
		if err != nil {
			return err
		}
		task = t

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
			Content: pgtype.Text{String: "Runtime start failed: " + errMsg, Valid: true},
		})
		if err != nil {
			return err
		}
		if task.ChatSessionID.Valid {
			row, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
				ChatSessionID: task.ChatSessionID,
				Role:          "assistant",
				Content:       "Runtime start failed: " + errMsg,
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
		"error", errMsg,
		"failure_reason", "runtime_start_failed",
	)
	s.captureTaskFailed(ctx, task)
	s.ReconcileAgentStatus(ctx, task.AgentID)
	if task.ChatSessionID.Valid {
		s.broadcastChatDone(ctx, task, assistantMsg)
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskFailed, task)
	return &task, nil
}
