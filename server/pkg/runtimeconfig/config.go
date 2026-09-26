package runtimeconfig

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const SchemaVersion = 1

// Duration is a JSON duration encoded with Go duration syntax, for example
// "30s" or "7m". Keeping units in the document avoids ambiguous integer
// values and lets validation reject zero or negative timeouts before publish.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

type Config struct {
	Version            int                      `json:"version"`
	Web                WebConfig                `json:"web"`
	Integrations       IntegrationsConfig       `json:"integrations"`
	Features           FeatureConfig            `json:"features"`
	Runtime            RuntimeConfig            `json:"runtime"`
	AgentIdentity      AgentIdentityConfig      `json:"agent_identity"`
	EnterpriseIdentity EnterpriseIdentityConfig `json:"enterprise_identity"`
}

type WebConfig struct {
	AttachmentDownloadMode string   `json:"attachment_download_mode"`
	SiteConnectSrc         []string `json:"site_connect_src"`
	CORSAllowedOrigins     []string `json:"cors_allowed_origins"`
	FrontendOrigin         string   `json:"frontend_origin"`
	LoginProviders         []string `json:"login_providers"`
	AppURL                 string   `json:"app_url"`
	PublicURL              string   `json:"public_url"`
	LocalUploadBaseURL     string   `json:"local_upload_base_url"`
}

type IntegrationsConfig struct {
	// Decode-only compatibility for old snapshots; employee settings now own response mode.
	DingTalkResponsePolicyEnabled   bool   `json:"dingtalk_response_policy_enabled,omitempty"`
	DingTalkResponsePolicyRevision  int64  `json:"dingtalk_response_policy_revision,omitempty"`
	AgentMessageRouterInternalURL   string `json:"agent_message_router_internal_url"`
	DingTalkDBaseBindingOrigin      string `json:"dingtalk_dbase_binding_origin"`
	DingTalkDBaseBindingPageURL     string `json:"dingtalk_dbase_binding_page_url"`
	GitHubAPIBaseURL                string `json:"github_api_base_url"`
	DingTalkRegistrationBaseURL     string `json:"dingtalk_registration_base_url"`
	DingTalkRegistrationOutgoingURL string `json:"dingtalk_registration_outgoing_url"`
}

type FeatureConfig struct {
	WorkspaceAccessTokens bool `json:"workspace_access_tokens"`
}

type RuntimeConfig struct {
	AgenticFS AgenticFSConfig `json:"agentic_fs"`
	LLM       LLMConfig       `json:"llm"`
	FCE2B     FCE2BConfig     `json:"fc_e2b"`
	ASB       ASBConfig       `json:"asb"`
}

// AgenticFSConfig contains live defaults for newly provisioned spaces, not
// credentials or changes to an existing space's cloud quota.
type AgenticFSConfig struct {
	SizeLimit          int64               `json:"size_limit"`
	FileCountLimit     int64               `json:"file_count_limit"`
	Placement          *AgenticFSPlacement `json:"placement,omitempty"`
	CredentialResource string              `json:"credential_resource,omitempty"`
}

// The resource reference selects a Normandy-managed access package; it is not
// an access key. Secret material is only resolved by the credential provider.
type AgenticFSPlacement struct {
	AccountID       string   `json:"account_id"`
	Region          string   `json:"region"`
	Zone            string   `json:"zone"`
	TeamID          string   `json:"team_id"`
	FileSystemID    string   `json:"file_system_id"`
	VPCID           string   `json:"vpc_id"`
	SecurityGroupID string   `json:"security_group_id"`
	VSwitchIDs      []string `json:"vswitch_ids"`
}

func (c AgenticFSConfig) Defaults() AgenticFSConfig {
	if c.SizeLimit == 0 && c.FileCountLimit == 0 {
		c.SizeLimit, c.FileCountLimit = 100<<30, 1000000000
	}
	return c
}

type LLMConfig struct {
	BaseURL          string   `json:"base_url"`
	Models           []string `json:"models"`
	DefaultModel     string   `json:"default_model"`
	CoordinatorModel string   `json:"coordinator_model,omitempty"`
}

type FCE2BConfig struct {
	DSHEventWakeup       bool `json:"dsh_event_wakeup,omitempty"`
	DingTalkReplyCommand bool `json:"dingtalk_reply_command,omitempty"`
	ASBEventWakeup       bool `json:"asb_event_wakeup,omitempty"`
	StartupObservability bool `json:"startup_observability,omitempty"`
	BoundedReadyExec     bool `json:"bounded_ready_exec,omitempty"`
	CoalescedHotExec     bool `json:"coalesced_hot_exec,omitempty"`
	BatchSkillResolve    bool `json:"batch_skill_resolve,omitempty"`

	RecoverAbandonedLaunches     bool     `json:"recover_abandoned_launches,omitempty"`
	BoundDSHHostWait             bool     `json:"bound_dsh_host_wait,omitempty"`
	DWSMessagePolicyFingerprints []string `json:"dws_message_policy_fingerprints,omitempty"`
	Enabled                      bool     `json:"enabled"`
	StablePublisherUserIDs       []string `json:"stable_publisher_user_ids"`
	Template                     string   `json:"template"`
	ServerURL                    string   `json:"server_url"`
	APIURL                       string   `json:"api_url"`
	Domain                       string   `json:"domain"`
	TimeoutSeconds               int      `json:"timeout_seconds"`
	SandboxReadyTimeout          Duration `json:"sandbox_ready_timeout"`
}

type ASBConfig struct {
	NetworkAllowlist      []string `json:"network_allowlist,omitempty"`
	Enabled               bool     `json:"enabled"`
	APIURL                string   `json:"api_url"`
	ServerURL             string   `json:"server_url"`
	TimeoutSeconds        int      `json:"timeout_seconds"`
	ReadyTimeout          Duration `json:"ready_timeout"`
	CommandReadyTimeout   Duration `json:"command_ready_timeout"`
	WireGuardReadyTimeout Duration `json:"wireguard_ready_timeout"`
	ResourceCPU           string   `json:"resource_cpu"`
	ResourceMemory        string   `json:"resource_memory"`
}

type AgentIdentityConfig struct {
	ControlBaseURL          string   `json:"control_base_url"`
	SandboxBaseURL          string   `json:"sandbox_base_url"`
	Timeout                 Duration `json:"timeout"`
	DebugLogContextToken    bool     `json:"debug_log_context_token"`
	DebugContextTokenAgents []string `json:"debug_context_token_agent_ids"`
}

type EnterpriseIdentityConfig struct {
	Enabled             bool     `json:"enabled"`
	OAuthAttemptTTL     Duration `json:"oauth_attempt_ttl"`
	BUCAuthorizeURL     string   `json:"buc_authorize_url"`
	BUCTokenURL         string   `json:"buc_token_url"`
	BUCIssuer           string   `json:"buc_issuer"`
	BUCJWKSURL          string   `json:"buc_jwks_url"`
	BUCRedirectURL      string   `json:"buc_redirect_url"`
	BUCAuthorizeApps    []string `json:"buc_authorize_apps"`
	AuthXServiceID      string   `json:"authx_service_id"`
	AuthXAudience       string   `json:"authx_audience"`
	AuthXEnvironment    string   `json:"authx_environment"`
	AuthXTTLSeconds     int64    `json:"authx_ttl_seconds"`
	IdemBaseURL         string   `json:"idem_base_url"`
	IdemTimeout         Duration `json:"idem_timeout"`
	OperatorTrustDomain string   `json:"operator_trust_domain"`
	AgentTrustDomain    string   `json:"agent_trust_domain"`
	AgentNamespace      string   `json:"agent_namespace"`
	AITTTLSeconds       int64    `json:"ait_ttl_seconds"`
}

// ParseStrict decodes one complete runtime document. Unknown fields and
// trailing JSON are rejected so a misspelled key cannot be silently ignored.
func ParseStrict(data []byte, production bool) (Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse runtime config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("runtime config contains multiple JSON values")
		}
		return Config{}, fmt.Errorf("parse trailing runtime config: %w", err)
	}
	if err := cfg.Validate(production); err != nil {
		return Config{}, err
	}
	return cfg.normalized(), nil
}

func (c Config) Validate(production bool) error {
	if c.Version != SchemaVersion {
		return fmt.Errorf("runtime config version must be %d", SchemaVersion)
	}
	if err := c.Web.validate(); err != nil {
		return fmt.Errorf("web: %w", err)
	}
	if err := c.Integrations.validate(); err != nil {
		return fmt.Errorf("integrations: %w", err)
	}
	if err := c.Runtime.validate(); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := c.AgentIdentity.validate(production); err != nil {
		return fmt.Errorf("agent_identity: %w", err)
	}
	if err := c.EnterpriseIdentity.validate(); err != nil {
		return fmt.Errorf("enterprise_identity: %w", err)
	}
	return nil
}

func (c Config) normalized() Config {
	c.Runtime.AgenticFS = c.Runtime.AgenticFS.Defaults()
	c.Web.AttachmentDownloadMode = strings.ToLower(strings.TrimSpace(c.Web.AttachmentDownloadMode))
	c.Web.SiteConnectSrc = normalizedUnique(c.Web.SiteConnectSrc)
	c.Web.CORSAllowedOrigins = normalizedUnique(c.Web.CORSAllowedOrigins)
	c.Web.LoginProviders = normalizedLowerUnique(c.Web.LoginProviders)
	c.Web.FrontendOrigin = trimURL(c.Web.FrontendOrigin)
	c.Web.AppURL = trimURL(c.Web.AppURL)
	c.Web.PublicURL = trimURL(c.Web.PublicURL)
	c.Web.LocalUploadBaseURL = trimURL(c.Web.LocalUploadBaseURL)
	c.Integrations.AgentMessageRouterInternalURL = trimURL(c.Integrations.AgentMessageRouterInternalURL)
	c.Integrations.DingTalkDBaseBindingOrigin = trimURL(c.Integrations.DingTalkDBaseBindingOrigin)
	c.Integrations.DingTalkDBaseBindingPageURL = trimURL(c.Integrations.DingTalkDBaseBindingPageURL)
	c.Integrations.GitHubAPIBaseURL = trimURL(c.Integrations.GitHubAPIBaseURL)
	c.Integrations.DingTalkRegistrationBaseURL = trimURL(c.Integrations.DingTalkRegistrationBaseURL)
	c.Integrations.DingTalkRegistrationOutgoingURL = trimURL(c.Integrations.DingTalkRegistrationOutgoingURL)
	c.Runtime.LLM.BaseURL = trimURL(c.Runtime.LLM.BaseURL)
	c.Runtime.LLM.Models = normalizedUnique(c.Runtime.LLM.Models)
	c.Runtime.LLM.DefaultModel = strings.TrimSpace(c.Runtime.LLM.DefaultModel)
	c.Runtime.LLM.CoordinatorModel = strings.TrimSpace(c.Runtime.LLM.CoordinatorModel)
	c.Runtime.FCE2B.Template = strings.TrimSpace(c.Runtime.FCE2B.Template)
	c.Runtime.FCE2B.StablePublisherUserIDs = normalizedUnique(c.Runtime.FCE2B.StablePublisherUserIDs)
	c.Runtime.FCE2B.DWSMessagePolicyFingerprints = normalizedUnique(c.Runtime.FCE2B.DWSMessagePolicyFingerprints)
	c.Runtime.FCE2B.ServerURL = trimURL(c.Runtime.FCE2B.ServerURL)
	c.Runtime.FCE2B.APIURL = trimURL(c.Runtime.FCE2B.APIURL)
	c.Runtime.FCE2B.Domain = strings.TrimSpace(c.Runtime.FCE2B.Domain)
	c.Runtime.ASB.APIURL = trimURL(c.Runtime.ASB.APIURL)
	c.Runtime.ASB.ServerURL = trimURL(c.Runtime.ASB.ServerURL)
	c.Runtime.ASB.ResourceCPU = strings.TrimSpace(c.Runtime.ASB.ResourceCPU)
	c.Runtime.ASB.ResourceMemory = strings.TrimSpace(c.Runtime.ASB.ResourceMemory)
	c.AgentIdentity.ControlBaseURL = trimURL(c.AgentIdentity.ControlBaseURL)
	c.AgentIdentity.SandboxBaseURL = trimURL(c.AgentIdentity.SandboxBaseURL)
	c.AgentIdentity.DebugContextTokenAgents = normalizedUnique(c.AgentIdentity.DebugContextTokenAgents)
	c.EnterpriseIdentity.BUCAuthorizeURL = trimURL(c.EnterpriseIdentity.BUCAuthorizeURL)
	c.EnterpriseIdentity.BUCTokenURL = trimURL(c.EnterpriseIdentity.BUCTokenURL)
	c.EnterpriseIdentity.BUCIssuer = trimURL(c.EnterpriseIdentity.BUCIssuer)
	c.EnterpriseIdentity.BUCJWKSURL = trimURL(c.EnterpriseIdentity.BUCJWKSURL)
	c.EnterpriseIdentity.BUCRedirectURL = trimURL(c.EnterpriseIdentity.BUCRedirectURL)
	c.EnterpriseIdentity.BUCAuthorizeApps = normalizedUnique(c.EnterpriseIdentity.BUCAuthorizeApps)
	c.EnterpriseIdentity.AuthXServiceID = strings.TrimSpace(c.EnterpriseIdentity.AuthXServiceID)
	c.EnterpriseIdentity.AuthXAudience = strings.TrimSpace(c.EnterpriseIdentity.AuthXAudience)
	c.EnterpriseIdentity.AuthXEnvironment = strings.TrimSpace(c.EnterpriseIdentity.AuthXEnvironment)
	c.EnterpriseIdentity.IdemBaseURL = trimURL(c.EnterpriseIdentity.IdemBaseURL)
	c.EnterpriseIdentity.OperatorTrustDomain = strings.TrimSpace(c.EnterpriseIdentity.OperatorTrustDomain)
	c.EnterpriseIdentity.AgentTrustDomain = strings.TrimSpace(c.EnterpriseIdentity.AgentTrustDomain)
	c.EnterpriseIdentity.AgentNamespace = strings.TrimSpace(c.EnterpriseIdentity.AgentNamespace)
	return c
}

func (c WebConfig) validate() error {
	switch strings.ToLower(strings.TrimSpace(c.AttachmentDownloadMode)) {
	case "auto", "proxy", "cloudfront":
	default:
		return fmt.Errorf("attachment_download_mode must be auto, proxy, or cloudfront")
	}
	if len(c.CORSAllowedOrigins) == 0 {
		return fmt.Errorf("cors_allowed_origins must not be empty")
	}
	for i, origin := range c.CORSAllowedOrigins {
		if err := validateHTTPOrigin(origin, true); err != nil {
			return fmt.Errorf("cors_allowed_origins[%d]: %w", i, err)
		}
	}
	for name, raw := range map[string]string{
		"frontend_origin": c.FrontendOrigin,
		"app_url":         c.AppURL,
		"public_url":      c.PublicURL,
	} {
		if err := validateHTTPOrigin(raw, true); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := validateHTTPURL(c.LocalUploadBaseURL, false); err != nil {
		return fmt.Errorf("local_upload_base_url: %w", err)
	}
	known := map[string]bool{"email": true, "google": true, "dingtalk": true, "lark": true}
	for _, provider := range c.LoginProviders {
		if !known[strings.ToLower(strings.TrimSpace(provider))] {
			return fmt.Errorf("unknown login provider %q", provider)
		}
	}
	return validateUnique("login_providers", c.LoginProviders, true)
}

func (c IntegrationsConfig) validate() error {
	for name, raw := range map[string]string{
		"agent_message_router_internal_url":  c.AgentMessageRouterInternalURL,
		"dingtalk_dbase_binding_origin":      c.DingTalkDBaseBindingOrigin,
		"dingtalk_dbase_binding_page_url":    c.DingTalkDBaseBindingPageURL,
		"github_api_base_url":                c.GitHubAPIBaseURL,
		"dingtalk_registration_base_url":     c.DingTalkRegistrationBaseURL,
		"dingtalk_registration_outgoing_url": c.DingTalkRegistrationOutgoingURL,
	} {
		if err := validateHTTPURL(raw, false); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	dbaseOrigin := strings.TrimSpace(c.DingTalkDBaseBindingOrigin)
	dbasePage := strings.TrimSpace(c.DingTalkDBaseBindingPageURL)
	if (dbaseOrigin == "") != (dbasePage == "") {
		return fmt.Errorf("dingtalk_dbase_binding_origin and dingtalk_dbase_binding_page_url must be configured together")
	}
	if dbaseOrigin != "" {
		if err := validateHTTPOrigin(dbaseOrigin, true); err != nil {
			return fmt.Errorf("dingtalk_dbase_binding_origin: %w", err)
		}
		origin, _ := url.Parse(dbaseOrigin)
		page, _ := url.Parse(dbasePage)
		if !strings.EqualFold(origin.Scheme, page.Scheme) || !strings.EqualFold(origin.Host, page.Host) ||
			page.RawQuery != "" || page.Fragment != "" {
			return fmt.Errorf("dingtalk_dbase_binding_page_url must share the configured binding origin and contain no query or fragment")
		}
	}
	if outgoing := strings.TrimSpace(c.DingTalkRegistrationOutgoingURL); outgoing != "" {
		parsed, _ := url.Parse(outgoing)
		if !strings.EqualFold(parsed.Scheme, "https") {
			return fmt.Errorf("dingtalk_registration_outgoing_url must use HTTPS")
		}
	}
	return nil
}

func (c RuntimeConfig) validate() error {
	quota := c.AgenticFS.Defaults()
	if quota.SizeLimit < 10<<30 || quota.SizeLimit%(1<<30) != 0 || quota.FileCountLimit < 10000 || quota.FileCountLimit > 1000000000 {
		return fmt.Errorf("agentic_fs requires size_limit >= 10 GiB in whole GiB and file_count_limit between 10000 and 1000000000")
	}
	if p := c.AgenticFS.Placement; p != nil {
		if p.AccountID == "" || p.Region == "" || !strings.HasPrefix(p.Zone, p.Region+"-") || p.TeamID == "" || p.FileSystemID == "" || p.VPCID == "" || p.SecurityGroupID == "" || len(p.VSwitchIDs) == 0 || slices.Contains(p.VSwitchIDs, "") || !strings.HasPrefix(c.AgenticFS.CredentialResource, "internal:acs:ram:"+p.AccountID+":user/") || !strings.HasSuffix(c.AgenticFS.CredentialResource, "/accesspack") {
			return fmt.Errorf("agentic_fs requires complete placement and an account-matching managed credential resource")
		}
	} else if c.AgenticFS.CredentialResource != "" {
		return fmt.Errorf("agentic_fs.credential_resource requires placement")
	}
	for _, fingerprint := range c.FCE2B.DWSMessagePolicyFingerprints {
		if _, err := hex.DecodeString(fingerprint); err != nil || len(fingerprint) != 16 || fingerprint != strings.ToLower(fingerprint) {
			return fmt.Errorf("fc_e2b.dws_message_policy_fingerprints must contain exact lowercase 16-character fingerprints")
		}
	}
	if err := validateHTTPURL(c.LLM.BaseURL, true); err != nil {
		return fmt.Errorf("llm.base_url: %w", err)
	}
	if len(c.LLM.Models) == 0 {
		return fmt.Errorf("llm.models must not be empty")
	}
	if err := validateUnique("llm.models", c.LLM.Models, false); err != nil {
		return err
	}
	defaultModel := strings.TrimSpace(c.LLM.DefaultModel)
	if defaultModel == "" || !slices.Contains(normalizedUnique(c.LLM.Models), defaultModel) {
		return fmt.Errorf("llm.default_model must name an entry in llm.models")
	}
	if c.FCE2B.Enabled {
		if err := validateUnique("fc_e2b.stable_publisher_user_ids", c.FCE2B.StablePublisherUserIDs, true); err != nil {
			return err
		}
		for _, userID := range c.FCE2B.StablePublisherUserIDs {
			if _, err := uuid.Parse(strings.TrimSpace(userID)); err != nil {
				return fmt.Errorf("fc_e2b.stable_publisher_user_ids contains invalid UUID %q", userID)
			}
		}
		for name, raw := range map[string]string{"server_url": c.FCE2B.ServerURL, "api_url": c.FCE2B.APIURL} {
			if err := validateHTTPURL(raw, true); err != nil {
				return fmt.Errorf("fc_e2b.%s: %w", name, err)
			}
		}
		if strings.TrimSpace(c.FCE2B.Template) == "" || strings.TrimSpace(c.FCE2B.Domain) == "" {
			return fmt.Errorf("fc_e2b.template and fc_e2b.domain are required when enabled")
		}
		if c.FCE2B.TimeoutSeconds <= 0 || c.FCE2B.SandboxReadyTimeout.Duration <= 0 {
			return fmt.Errorf("fc_e2b timeouts must be positive")
		}
	}
	if c.ASB.Enabled {
		for name, raw := range map[string]string{"api_url": c.ASB.APIURL, "server_url": c.ASB.ServerURL} {
			if err := validateHTTPURL(raw, true); err != nil {
				return fmt.Errorf("asb.%s: %w", name, err)
			}
		}
		if c.ASB.TimeoutSeconds <= 0 || c.ASB.ReadyTimeout.Duration <= 0 || c.ASB.CommandReadyTimeout.Duration <= 0 || c.ASB.WireGuardReadyTimeout.Duration <= 0 {
			return fmt.Errorf("asb timeouts must be positive")
		}
		if strings.TrimSpace(c.ASB.ResourceCPU) == "" || strings.TrimSpace(c.ASB.ResourceMemory) == "" {
			return fmt.Errorf("asb resource_cpu and resource_memory are required when enabled")
		}
	}
	return nil
}

func (c AgentIdentityConfig) validate(production bool) error {
	if err := validateHTTPURL(c.ControlBaseURL, true); err != nil {
		return fmt.Errorf("control_base_url: %w", err)
	}
	if err := validateHTTPURL(c.SandboxBaseURL, true); err != nil {
		return fmt.Errorf("sandbox_base_url: %w", err)
	}
	if c.Timeout.Duration <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if production && (c.DebugLogContextToken || len(c.DebugContextTokenAgents) > 0) {
		return fmt.Errorf("context-token debug settings must be disabled in production")
	}
	return validateUnique("debug_context_token_agent_ids", c.DebugContextTokenAgents, false)
}

func (c EnterpriseIdentityConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	for name, raw := range map[string]string{
		"buc_authorize_url": c.BUCAuthorizeURL,
		"buc_token_url":     c.BUCTokenURL,
		"buc_issuer":        c.BUCIssuer,
		"buc_jwks_url":      c.BUCJWKSURL,
		"buc_redirect_url":  c.BUCRedirectURL,
		"idem_base_url":     c.IdemBaseURL,
	} {
		if err := validateHTTPSURL(raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, raw := range map[string]string{
		"authx_service_id":      c.AuthXServiceID,
		"authx_audience":        c.AuthXAudience,
		"authx_environment":     c.AuthXEnvironment,
		"operator_trust_domain": c.OperatorTrustDomain,
		"agent_trust_domain":    c.AgentTrustDomain,
		"agent_namespace":       c.AgentNamespace,
	} {
		if strings.TrimSpace(raw) == "" || strings.ContainsAny(raw, "\r\n") {
			return fmt.Errorf("%s must not be empty", name)
		}
	}
	if c.AuthXTTLSeconds <= 0 || c.AITTTLSeconds <= 0 || c.IdemTimeout.Duration <= 0 || c.OAuthAttemptTTL.Duration <= 0 {
		return fmt.Errorf("durations and TTL values must be positive")
	}
	if len(c.BUCAuthorizeApps) > 40 {
		return fmt.Errorf("buc_authorize_apps must contain at most 40 entries")
	}
	return validateUnique("buc_authorize_apps", c.BUCAuthorizeApps, false)
}

func validateHTTPURL(raw string, required bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return fmt.Errorf("must not be empty")
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("must be an absolute HTTP(S) URL without user info")
	}
	return nil
}

func validateHTTPOrigin(raw string, required bool) error {
	if err := validateHTTPURL(raw, required); err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, _ := url.Parse(strings.TrimSpace(raw))
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("must be an HTTP(S) origin without path, query, or fragment")
	}
	return nil
}

func validateHTTPSURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("must be an absolute HTTPS URL without user info")
	}
	return nil
}

func validateUnique(name string, values []string, lower bool) error {
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" {
			return fmt.Errorf("%s[%d] must not be empty", name, i)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func normalizedUnique(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizedLowerUnique(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strings.ToLower(strings.TrimSpace(value))
	}
	return normalizedUnique(out)
}

func trimURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
