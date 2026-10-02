package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/modelpricing"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

const defaultFCE2BCLIPath = "/usr/local/bin/e2b"

type runtimeConfigSecrets struct {
	LLMAPIKey       string
	FCE2BAPIKey     string
	DWSClientSecret string
	ASBWireGuard    string
	BUCClientID     string
	BUCClientSecret string
	BUCAgentID      string
}

type appRuntimeConfig struct {
	models  *modelregistry.Registry
	remote  *runtimeconfig.Service
	secrets runtimeConfigSecrets
	base    handler.Config
}

func newAppRuntimeConfig(remote *runtimeconfig.Service) (*appRuntimeConfig, error) {
	if remote == nil {
		return nil, nil
	}
	config := &appRuntimeConfig{
		remote: remote,
		secrets: runtimeConfigSecrets{
			LLMAPIKey:       strings.TrimSpace(os.Getenv("MULTICA_RUNTIME_LLM_API_KEY")),
			FCE2BAPIKey:     strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_KEY")),
			DWSClientSecret: strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET")),
			ASBWireGuard:    strings.TrimSpace(os.Getenv("MULTICA_ASB_WG_CLIENT_CREDENTIALS")),
			BUCClientID:     strings.TrimSpace(os.Getenv("MULTICA_BUC_CLIENT_ID")),
			BUCClientSecret: strings.TrimSpace(os.Getenv("MULTICA_BUC_CLIENT_SECRET")),
			BUCAgentID:      strings.TrimSpace(os.Getenv("MULTICA_BUC_AGENT_ID")),
		},
	}
	if err := config.validateCurrent(); err != nil {
		return nil, err
	}
	if err := remote.SetValidator(config.validateSnapshot); err != nil {
		return nil, err
	}
	return config, nil
}

func (c *appRuntimeConfig) setBase(base handler.Config) {
	if c != nil {
		c.base = base
	}
}

func (c *appRuntimeConfig) validateCurrent() error {
	if c == nil || c.remote == nil {
		return nil
	}
	fc := c.fce2b()
	if fc.Enabled {
		if err := fc.Validate(); err != nil {
			return fmt.Errorf("Diamond FC/E2B config: %w", err)
		}
	}
	asb := c.asb()
	if asb.Enabled {
		if err := asb.Validate(); err != nil {
			return fmt.Errorf("Diamond ASB config: %w", err)
		}
	}
	enterprise := c.enterpriseIdentity()
	if enterprise.Enabled {
		if err := enterprise.Validate(asb); err != nil {
			return fmt.Errorf("Diamond enterprise identity config: %w", err)
		}
		if err := service.ValidateEnterpriseIdentityClients(enterprise); err != nil {
			return fmt.Errorf("Diamond enterprise identity clients: %w", err)
		}
	}
	return nil
}

func (c *appRuntimeConfig) validateSnapshot(raw runtimeconfig.Config) error {
	if c == nil || c.remote == nil {
		return nil
	}
	static, err := runtimeconfig.NewStatic(raw)
	if err != nil {
		return err
	}
	candidate := *c
	candidate.remote = static
	return candidate.validateCurrent()
}

func (c *appRuntimeConfig) current() runtimeconfig.Config {
	return c.snapshot().Config
}

func (c *appRuntimeConfig) snapshot() runtimeconfig.Snapshot {
	if c == nil || c.remote == nil {
		return runtimeconfig.Snapshot{}
	}
	return c.remote.Current()
}

// The deployment override is immutable for a process. It lets pre-release
// select canaries without adding a key to a Diamond document shared with
// an older production binary.
func newEventRouteConfigProvider(c *appRuntimeConfig, raw string) (func(string, string, string) (string, string), error) {
	if strings.TrimSpace(raw) == "" {
		return c.eventRouteConfig, nil
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("event router override too large")
	}
	var rollout runtimeconfig.EventSceneRouterConfig
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rollout); err != nil {
		return nil, fmt.Errorf("invalid event router override: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("event router override has trailing JSON")
	}
	if err := rollout.Validate(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(raw))
	return func(ws, agent, org string) (string, string) {
		_, version := c.eventRouteConfig(ws, agent, org)
		route := "legacy"
		if rollout.Allows(ws, agent, org) {
			route = "unified"
		}
		return route, fmt.Sprintf("%s:env:%x", version, digest)
	}, nil
}

func (c *appRuntimeConfig) eventRouteConfig(workspaceID, agentID, orgID string) (string, string) {
	snapshot := c.snapshot()
	route := "legacy"
	if rollout := snapshot.Config.Runtime.EventSceneRouter; rollout != nil && rollout.Allows(workspaceID, agentID, orgID) {
		route = "unified"
	}
	return route, fmt.Sprintf("%s:%d", snapshot.SHA256, snapshot.Generation)
}

// coordinatorDecisionConfig reads, from one runtime configuration snapshot,
// the Coordinator model, whether the performance switch selects agentID,
// and the finish schema experiment, which only applies under that switch.
func (c *appRuntimeConfig) coordinatorDecisionConfig(agentID string) inboundcoord.DecisionConfig {
	snapshot := c.snapshot()
	cfg := inboundcoord.DecisionConfig{
		Model:            snapshot.Config.Runtime.LLM.CoordinatorModel,
		ConfigSHA256:     snapshot.SHA256,
		ConfigGeneration: snapshot.Generation,
	}
	rollout := snapshot.Config.Runtime.PerformanceOptimization
	if rollout == nil || !rollout.AllowsAgent(agentID) {
		return cfg
	}
	cfg.Performance = true
	if experiment := rollout.FinishSchemaExperiment; experiment != nil && experiment.Enabled {
		cfg.FinishSchema = inboundcoord.FinishSchemaExperiment{Enabled: true, Mode: experiment.Mode, Salt: experiment.Salt}
	}
	return cfg
}

// coordinatorCollectQuiet is agentID's collect-window silence under the
// performance switch; zero keeps the handler default.
func (c *appRuntimeConfig) coordinatorCollectQuiet(agentID string) time.Duration {
	rollout := c.current().Runtime.PerformanceOptimization
	if rollout == nil || !rollout.AllowsAgent(agentID) || rollout.CollectQuietMS <= 0 {
		return 0
	}
	return time.Duration(rollout.CollectQuietMS) * time.Millisecond
}

// useDWSForTag is the live runtime.use_dws_for_tag.
func (c *appRuntimeConfig) useDWSForTag() bool {
	if c == nil || c.remote == nil {
		return false
	}
	return c.remote.UseDWSForTag()
}

// fcE2BSDKRollout is the live runtime.fc_e2b_sdk_rollout.
func (c *appRuntimeConfig) fcE2BSDKRollout() runtimeconfig.FCE2BSDKRollout {
	if c == nil || c.remote == nil {
		return runtimeconfig.FCE2BSDKRollout{}
	}
	return c.remote.FCE2BSDKRollout()
}

// fcE2BSDKRolloutOf is the rollout of one runtime document; absent selects
// nothing.
func fcE2BSDKRolloutOf(raw runtimeconfig.RuntimeConfig) runtimeconfig.FCE2BSDKRollout {
	if raw.FCE2BSDKRollout == nil {
		return runtimeconfig.FCE2BSDKRollout{}
	}
	return *raw.FCE2BSDKRollout
}

func (c *appRuntimeConfig) fce2b() service.FCE2BConfig {
	raw := c.current()
	runtimeProviders := runtimeconfig.RuntimeProvidersSnapshot{}
	if c != nil && c.remote != nil {
		runtimeProviders = c.remote.RuntimeProviders()
	}
	return service.FCE2BConfig{
		QuickWins:                         quickWinsForRuntime(raw.Runtime),
		TaskModelResolver:                 c.taskModelResolver(),
		ModelResolver:                     c.modelResolver(),
		Enabled:                           raw.Runtime.FCE2B.Enabled,
		Template:                          raw.Runtime.FCE2B.Template,
		ServerURL:                         raw.Runtime.FCE2B.ServerURL,
		AppOrigin:                         raw.Web.AppURL,
		APIKey:                            c.secrets.FCE2BAPIKey,
		APIURL:                            raw.Runtime.FCE2B.APIURL,
		Domain:                            raw.Runtime.FCE2B.Domain,
		LLMBaseURL:                        raw.Runtime.LLM.BaseURL,
		LLMAPIKey:                         c.secrets.LLMAPIKey,
		LLMModels:                         defaultModelFirst(raw.Runtime.LLM.Models, raw.Runtime.LLM.DefaultModel),
		RuntimeProviderFingerprints:       runtimeProviders.Fingerprints,
		AgentIdentityControlBaseURL:       raw.AgentIdentity.ControlBaseURL,
		AgentIdentitySandboxBaseURL:       raw.AgentIdentity.SandboxBaseURL,
		AgentIdentityBaseURL:              raw.AgentIdentity.SandboxBaseURL,
		AgentIdentityTimeout:              raw.AgentIdentity.Timeout.Duration,
		AgentIdentityDebugLogContextToken: raw.AgentIdentity.DebugLogContextToken,
		AgentIdentityDebugContextAgentIDs: append([]string(nil), raw.AgentIdentity.DebugContextTokenAgents...),
		DWSClientSecret:                   c.secrets.DWSClientSecret,
		CLIPath:                           defaultFCE2BCLIPath,
		TimeoutSeconds:                    raw.Runtime.FCE2B.TimeoutSeconds,
		SandboxReadyTimeout:               raw.Runtime.FCE2B.SandboxReadyTimeout.Duration,
		SDKRollout:                        fcE2BSDKRolloutOf(raw.Runtime),
		ConnectionReuse:                   connectionReuseOf(raw.Runtime.FCE2B),
	}
}

// connectionReuseOf copies runtime.fc_e2b.connection_reuse into the launcher
// config. Nil uses the default cap. Target slices are copied so the launcher
// does not share the snapshot's lists.
func connectionReuseOf(cfg runtimeconfig.FCE2BConfig) runtimeconfig.FCE2BConnectionReuse {
	if cfg.ConnectionReuse == nil {
		return runtimeconfig.FCE2BConnectionReuse{}
	}
	out := *cfg.ConnectionReuse
	out.WorkspaceIDs = append([]string(nil), cfg.ConnectionReuse.WorkspaceIDs...)
	out.AgentIDs = append([]string(nil), cfg.ConnectionReuse.AgentIDs...)
	return out
}

func (c *appRuntimeConfig) asb() service.ASBConfig {
	raw := c.current()
	return service.ASBConfig{
		TaskModelResolver: c.taskModelResolver(),
		ModelResolver:     c.modelResolver(),
		NetworkAllowlist:  append([]string{}, raw.Runtime.ASB.NetworkAllowlist...),
		NetworkServiceURLs: []string{
			raw.AgentIdentity.ControlBaseURL, raw.AgentIdentity.SandboxBaseURL,
			raw.EnterpriseIdentity.BUCAuthorizeURL, raw.EnterpriseIdentity.BUCTokenURL, raw.EnterpriseIdentity.BUCIssuer, raw.EnterpriseIdentity.BUCJWKSURL, raw.EnterpriseIdentity.IdemBaseURL,
			raw.Web.PublicURL, raw.Web.AppURL, raw.Web.LocalUploadBaseURL,
			raw.Integrations.AgentMessageRouterInternalURL, raw.Integrations.GitHubAPIBaseURL,
			raw.Integrations.DingTalkRegistrationBaseURL, raw.Integrations.DingTalkRegistrationOutgoingURL,
			raw.Integrations.DingTalkDBaseBindingOrigin, raw.Integrations.DingTalkDBaseBindingPageURL,
		},
		Enabled:               raw.Runtime.ASB.Enabled,
		APIURL:                raw.Runtime.ASB.APIURL,
		ServerURL:             raw.Runtime.ASB.ServerURL,
		LLMBaseURL:            raw.Runtime.LLM.BaseURL,
		LLMAPIKey:             c.secrets.LLMAPIKey,
		LLMModels:             defaultModelFirst(raw.Runtime.LLM.Models, raw.Runtime.LLM.DefaultModel),
		TimeoutSeconds:        raw.Runtime.ASB.TimeoutSeconds,
		ReadyTimeout:          raw.Runtime.ASB.ReadyTimeout.Duration,
		CommandReadyTimeout:   raw.Runtime.ASB.CommandReadyTimeout.Duration,
		WireGuardReadyTimeout: raw.Runtime.ASB.WireGuardReadyTimeout.Duration,
		ResourceCPU:           raw.Runtime.ASB.ResourceCPU,
		ResourceMemory:        raw.Runtime.ASB.ResourceMemory,
		WireGuardCredentials:  c.secrets.ASBWireGuard,
	}
}

func (c *appRuntimeConfig) enterpriseIdentity() service.EnterpriseIdentityConfig {
	raw := c.current().EnterpriseIdentity
	return service.EnterpriseIdentityConfig{
		Enabled:             raw.Enabled,
		BUCAuthorizeURL:     raw.BUCAuthorizeURL,
		BUCTokenURL:         raw.BUCTokenURL,
		BUCIssuer:           raw.BUCIssuer,
		BUCJWKSURL:          raw.BUCJWKSURL,
		BUCClientID:         c.secrets.BUCClientID,
		BUCClientSecret:     c.secrets.BUCClientSecret,
		BUCAgentID:          c.secrets.BUCAgentID,
		BUCRedirectURL:      raw.BUCRedirectURL,
		BUCAuthorizeApps:    append([]string(nil), raw.BUCAuthorizeApps...),
		AuthXServiceID:      raw.AuthXServiceID,
		AuthXAudience:       raw.AuthXAudience,
		AuthXTTL:            raw.AuthXTTLSeconds,
		AuthXEnvironment:    raw.AuthXEnvironment,
		IdemBaseURL:         raw.IdemBaseURL,
		IdemTimeout:         raw.IdemTimeout.Duration,
		OperatorTrustDomain: raw.OperatorTrustDomain,
		AgentTrustDomain:    raw.AgentTrustDomain,
		AgentNamespace:      raw.AgentNamespace,
		AITTTL:              raw.AITTTLSeconds,
		OAuthAttemptTTL:     raw.OAuthAttemptTTL.Duration,
	}
}

func (c *appRuntimeConfig) modelPricing() modelpricing.Catalog {
	if c == nil || c.remote == nil {
		return nil
	}
	return c.remote.ModelPricing().Models
}

func (c *appRuntimeConfig) handlerConfig() handler.Config {
	if c == nil {
		return handler.Config{}
	}
	raw := c.current()
	cfg := c.base
	cfg.StableRuntimePublisherUserIDs = stringSet(raw.Runtime.FCE2B.StablePublisherUserIDs)
	cfg.PublicURL = raw.Web.PublicURL
	cfg.FrontendOrigin = raw.Web.FrontendOrigin
	cfg.AppURL = raw.Web.AppURL
	cfg.LoginProviders = append([]string(nil), raw.Web.LoginProviders...)
	cfg.GitHubAPIBaseURLProvider = c.githubAPIBaseURL
	cfg.AgentIdentityControlBaseURL = raw.AgentIdentity.ControlBaseURL
	cfg.AgentIdentityControlBaseURLProvider = c.agentIdentityControlBaseURL
	cfg.AgentIdentityTimeoutProvider = c.agentIdentityTimeout
	cfg.FCE2B = c.fce2b()
	cfg.ASB = c.asb()
	cfg.EnterpriseIdentity = c.enterpriseIdentity()
	cfg.ModelPricing = c.modelPricing()
	cfg.AttachmentDownloadMode = raw.Web.AttachmentDownloadMode
	cfg.AttachmentFrameAncestors = append([]string(nil), raw.Web.CORSAllowedOrigins...)
	if u := strings.TrimSpace(raw.Runtime.LLM.BaseURL); u != "" {
		cfg.LLMBaseURL = u
	}
	if k := strings.TrimSpace(c.secrets.LLMAPIKey); k != "" {
		cfg.LLMAPIKey = k
	}
	if m := strings.TrimSpace(raw.Runtime.LLM.DefaultModel); m != "" {
		cfg.LLMDefaultModel = m
	}
	return cfg
}

func (c *appRuntimeConfig) stablePublisherUserIDs() map[string]struct{} {
	return stringSet(c.current().Runtime.FCE2B.StablePublisherUserIDs)
}

func (c *appRuntimeConfig) appURL() string {
	if c == nil {
		return ""
	}
	return c.current().Web.AppURL
}

func (c *appRuntimeConfig) publicURL() string {
	if c == nil {
		return ""
	}
	return c.current().Web.PublicURL
}

func (c *appRuntimeConfig) siteConnectSrc() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.current().Web.SiteConnectSrc...)
}

func (c *appRuntimeConfig) corsAllowedOrigins() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.current().Web.CORSAllowedOrigins...)
}

func (c *appRuntimeConfig) dbaseBindingOrigin() string {
	if c == nil {
		return ""
	}
	return c.current().Integrations.DingTalkDBaseBindingOrigin
}

func (c *appRuntimeConfig) dbaseBindingPageURL() string {
	if c == nil {
		return ""
	}
	return c.current().Integrations.DingTalkDBaseBindingPageURL
}

func (c *appRuntimeConfig) localUploadBaseURL() string {
	if c == nil {
		return ""
	}
	return c.current().Web.LocalUploadBaseURL
}

func (c *appRuntimeConfig) agentMessageRouterInternalURL() string {
	if c == nil {
		return ""
	}
	return c.current().Integrations.AgentMessageRouterInternalURL
}

func (c *appRuntimeConfig) agentIdentityControlBaseURL() string {
	if c == nil {
		return ""
	}
	return c.current().AgentIdentity.ControlBaseURL
}

func (c *appRuntimeConfig) agentIdentityTimeout() time.Duration {
	if c == nil {
		return 0
	}
	return c.current().AgentIdentity.Timeout.Duration
}

func (c *appRuntimeConfig) githubAPIBaseURL() string {
	if c == nil {
		return ""
	}
	return c.current().Integrations.GitHubAPIBaseURL
}

func (c *appRuntimeConfig) dingTalkRegistrationBaseURL() string {
	if c == nil {
		return ""
	}
	return c.current().Integrations.DingTalkRegistrationBaseURL
}

func (c *appRuntimeConfig) dingTalkRegistrationOutgoingURL() string {
	if c == nil {
		return ""
	}
	return c.current().Integrations.DingTalkRegistrationOutgoingURL
}

func (c *appRuntimeConfig) frontendOrigin() string {
	if c == nil {
		return ""
	}
	return c.current().Web.FrontendOrigin
}

func (c *appRuntimeConfig) subscribe(subscriber func(runtimeconfig.Snapshot)) {
	if c != nil && c.remote != nil {
		c.remote.Subscribe(subscriber)
	}
}

func (c *appRuntimeConfig) loginProviderAllowed(name string) bool {
	if c == nil {
		return handler.LoginProviderAllowed(name)
	}
	providers := c.current().Web.LoginProviders
	if len(providers) == 0 {
		return true
	}
	return slices.Contains(providers, strings.ToLower(strings.TrimSpace(name)))
}

func defaultModelFirst(models []string, defaultModel string) []string {
	defaultModel = strings.TrimSpace(defaultModel)
	out := make([]string, 0, len(models))
	if defaultModel != "" {
		out = append(out, defaultModel)
	}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" && model != defaultModel {
			out = append(out, model)
		}
	}
	return out
}

func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

type runtimeFeatureFlagProvider struct {
	config *appRuntimeConfig
}

func (p runtimeFeatureFlagProvider) Lookup(_ context.Context, key string) (featureflag.Decision, bool) {
	if p.config == nil {
		return featureflag.Decision{}, false
	}
	var enabled bool
	switch key {
	case "workspace_access_tokens":
		enabled = p.config.current().Features.WorkspaceAccessTokens
	case "semantica_mcp_relay":
		value := p.config.current().Features.SemanticaMCPRelay
		if value == nil {
			return featureflag.Decision{}, false
		}
		enabled = *value
	default:
		return featureflag.Decision{}, false
	}
	variant := "off"
	if enabled {
		variant = "on"
	}
	return featureflag.Decision{
		Enabled: enabled,
		Variant: variant,
		Reason:  featureflag.ReasonStatic,
		Source:  p.Name(),
	}, true
}

func (runtimeFeatureFlagProvider) Name() string { return "runtime-diamond" }

func (c *appRuntimeConfig) modelResolver() func(string) (string, error) {
	if c.models == nil || os.Getenv("MULTICA_MODEL_GATEWAY_ENABLED") != "true" {
		return nil
	}
	return c.models.ModelForAgent
}

func (c *appRuntimeConfig) taskModelResolver() func(context.Context, string, string) (string, error) {
	if c.models == nil || os.Getenv("MULTICA_MODEL_GATEWAY_ENABLED") != "true" {
		return nil
	}
	return c.models.PrepareTaskModel
}
