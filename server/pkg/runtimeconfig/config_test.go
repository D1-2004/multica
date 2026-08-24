package runtimeconfig

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDocumentedExampleMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
	if err != nil {
		t.Fatalf("read documented example: %v", err)
	}
	if _, err := ParseStrict(raw, true); err != nil {
		t.Fatalf("documented example: %v", err)
	}
}

func validJSON() string {
	return `{
  "version": 1,
  "web": {
    "attachment_download_mode": "auto",
    "cors_allowed_origins": ["https://pre.example.com"],
    "frontend_origin": "https://pre.example.com",
    "login_providers": ["dingtalk"],
    "app_url": "https://pre.example.com",
    "public_url": "https://pre-api.example.com",
    "local_upload_base_url": ""
  },
  "integrations": {
    "agent_message_router_internal_url": "https://router.example.com",
    "dingtalk_dbase_binding_origin": "https://dbase.example.com",
    "dingtalk_dbase_binding_page_url": "https://dbase.example.com/bind",
    "github_api_base_url": "https://api.github.com",
    "dingtalk_registration_base_url": "https://registration.example.com",
    "dingtalk_registration_outgoing_url": "https://outgoing.example.com"
  },
  "features": {"workspace_access_tokens": true},
  "runtime": {
    "llm": {
      "base_url": "https://llm.example.com/v1",
      "models": ["qwen3.8-max", "qwen3-max"],
      "default_model": "qwen3.8-max"
    },
    "fc_e2b": {
      "enabled": true,
      "stable_publisher_user_ids": ["019fcfda-70bb-7830-acee-61d95e68668b"],
      "template": "stable-template",
      "server_url": "https://pre-api.example.com",
      "api_url": "https://fc.example.com",
      "domain": "fc.example.com",
      "timeout_seconds": 900,
      "sandbox_ready_timeout": "5m"
    },
    "asb": {
      "enabled": true,
      "api_url": "https://asb.example.com",
      "server_url": "https://pre-api.example.com",
      "timeout_seconds": 900,
      "ready_timeout": "5m",
      "command_ready_timeout": "7m",
      "wireguard_ready_timeout": "2m",
      "resource_cpu": "4",
      "resource_memory": "8Gi"
    }
  },
  "agent_identity": {
    "control_base_url": "https://pre-identity.example.com",
    "sandbox_base_url": "https://identity.example.com",
    "timeout": "30s",
    "debug_log_context_token": false,
    "debug_context_token_agent_ids": []
  },
  "enterprise_identity": {
    "enabled": true,
    "oauth_attempt_ttl": "10m",
    "buc_authorize_url": "https://login.example.com/authorize",
    "buc_token_url": "https://login.example.com/token",
    "buc_issuer": "https://login.example.com",
    "buc_jwks_url": "https://login.example.com/jwks",
    "buc_redirect_url": "https://pre-api.example.com/callback",
    "buc_authorize_apps": ["app-a"],
    "authx_service_id": "service-a",
    "authx_audience": "audience-a",
    "authx_environment": "staging",
    "authx_ttl_seconds": 3600,
    "idem_base_url": "https://idem.example.com",
    "idem_timeout": "30s",
    "operator_trust_domain": "operator.example",
    "agent_trust_domain": "agent.example",
    "agent_namespace": "multica",
    "ait_ttl_seconds": 900
  }
}`
}

func TestParseStrictAcceptsCompleteConfig(t *testing.T) {
	cfg, err := ParseStrict([]byte(validJSON()), true)
	if err != nil {
		t.Fatalf("ParseStrict: %v", err)
	}
	if cfg.Runtime.LLM.DefaultModel != "qwen3.8-max" {
		t.Fatalf("default model = %q", cfg.Runtime.LLM.DefaultModel)
	}
	if got := cfg.Runtime.ASB.CommandReadyTimeout.String(); got != "7m0s" {
		t.Fatalf("ASB command timeout = %q", got)
	}
}

func TestParseStrictRejectsUnknownField(t *testing.T) {
	raw := strings.Replace(validJSON(), `"version": 1`, `"version": 1, "versoin": 1`, 1)
	_, err := ParseStrict([]byte(raw), true)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown field", err)
	}
}

func TestParseStrictRejectsDefaultOutsideCatalog(t *testing.T) {
	raw := strings.Replace(validJSON(), `"default_model": "qwen3.8-max"`, `"default_model": "missing"`, 1)
	_, err := ParseStrict([]byte(raw), true)
	if err == nil || !strings.Contains(err.Error(), "default_model") {
		t.Fatalf("error = %v, want default model validation", err)
	}
}

func TestParseStrictRejectsProductionContextTokenDebug(t *testing.T) {
	raw := strings.Replace(validJSON(), `"debug_log_context_token": false`, `"debug_log_context_token": true`, 1)
	_, err := ParseStrict([]byte(raw), true)
	if err == nil || !strings.Contains(err.Error(), "disabled in production") {
		t.Fatalf("error = %v, want production debug guard", err)
	}
	if _, err := ParseStrict([]byte(raw), false); err != nil {
		t.Fatalf("staging debug config should be accepted: %v", err)
	}
}

func TestProductionEnvironmentPrefersAoneEnvironmentType(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("AONE_ENV_TYPE", "pre")
	if productionEnvironmentFromEnv() {
		t.Fatal("Aone pre-release unit must not be classified as production")
	}
	t.Setenv("AONE_ENV_TYPE", "production")
	if !productionEnvironmentFromEnv() {
		t.Fatal("Aone production unit must be classified as production")
	}
}

type fakeDiamondClient struct {
	content                       string
	manifestFingerprintsContent   string
	getErr                        error
	manifestFingerprintsGetErr    error
	listenErr                     error
	manifestFingerprintsListenErr error
	onChange                      func(string)
	manifestFingerprintsOnChange  func(string)
	cancelled                     bool
	manifestFingerprintsCancelled bool
	closed                        bool
}

func (c *fakeDiamondClient) GetConfig(dataID, group string) (string, error) {
	if group != DiamondGroup {
		return "", errors.New("unexpected coordinates")
	}
	switch dataID {
	case DiamondDataID:
		return c.content, c.getErr
	case ManifestFingerprintsDiamondDataID:
		content := c.manifestFingerprintsContent
		if content == "" {
			content = validManifestFingerprintsJSON()
		}
		return content, c.manifestFingerprintsGetErr
	default:
		return "", errors.New("unexpected coordinates")
	}
}

func (c *fakeDiamondClient) ListenConfig(dataID, group string, onChange func(string)) error {
	if group != DiamondGroup {
		return errors.New("unexpected coordinates")
	}
	switch dataID {
	case DiamondDataID:
		c.onChange = onChange
		return c.listenErr
	case ManifestFingerprintsDiamondDataID:
		c.manifestFingerprintsOnChange = onChange
		return c.manifestFingerprintsListenErr
	default:
		return errors.New("unexpected coordinates")
	}
}

func (c *fakeDiamondClient) CancelListenConfig(dataID, group string) error {
	if group != DiamondGroup {
		return errors.New("unexpected coordinates")
	}
	switch dataID {
	case DiamondDataID:
		c.cancelled = true
		return nil
	case ManifestFingerprintsDiamondDataID:
		c.manifestFingerprintsCancelled = true
		return nil
	default:
		return errors.New("unexpected coordinates")
	}
}

func (c *fakeDiamondClient) CloseClient() { c.closed = true }

func TestDiamondServiceAppliesValidUpdateAndRejectsInvalidUpdate(t *testing.T) {
	client := &fakeDiamondClient{content: validJSON()}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	first := service.Current()
	if first.Generation != 1 || client.onChange == nil || client.manifestFingerprintsOnChange == nil {
		t.Fatalf("initial snapshot = %#v, runtime listener=%v, fingerprints listener=%v", first, client.onChange != nil, client.manifestFingerprintsOnChange != nil)
	}

	updated := strings.Replace(validJSON(), `"default_model": "qwen3.8-max"`, `"default_model": "qwen3-max"`, 1)
	client.onChange(updated)
	second := service.Current()
	if second.Generation != 2 || second.Config.Runtime.LLM.DefaultModel != "qwen3-max" {
		t.Fatalf("updated snapshot = %#v", second)
	}

	client.onChange(`{"version":1}`)
	retained := service.Current()
	if retained.Generation != 2 || retained.SHA256 != second.SHA256 {
		t.Fatalf("invalid update replaced snapshot: %#v", retained)
	}
}

func TestDiamondServiceValidatorRejectsUpdateAndRetainsSnapshot(t *testing.T) {
	client := &fakeDiamondClient{content: validJSON()}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if err := service.SetValidator(func(cfg Config) error {
		if cfg.Runtime.LLM.DefaultModel == "qwen3-max" {
			return errors.New("deployment dependency rejected")
		}
		return nil
	}); err != nil {
		t.Fatalf("SetValidator: %v", err)
	}

	first := service.Current()
	client.onChange(strings.Replace(validJSON(), `"default_model": "qwen3.8-max"`, `"default_model": "qwen3-max"`, 1))
	retained := service.Current()
	if retained.Generation != first.Generation || retained.SHA256 != first.SHA256 {
		t.Fatalf("validator-rejected update replaced snapshot: %#v", retained)
	}
}

func TestSnapshotCollectionsAreImmutableCopies(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, validJSON()))
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	first := service.Current()
	first.Config.Runtime.FCE2B.StablePublisherUserIDs[0] = "mutated"
	first.Config.Runtime.LLM.Models[0] = "mutated"
	current := service.Current()
	if current.Config.Runtime.FCE2B.StablePublisherUserIDs[0] == "mutated" || current.Config.Runtime.LLM.Models[0] == "mutated" {
		t.Fatalf("Current returned mutable service-owned collections: %#v", current)
	}
}

func mustParseConfig(t *testing.T, raw string) Config {
	t.Helper()
	cfg, err := ParseStrict([]byte(raw), true)
	if err != nil {
		t.Fatalf("ParseStrict: %v", err)
	}
	return cfg
}

func TestDiamondServiceRequiresInitialFetchAndListener(t *testing.T) {
	for _, test := range []struct {
		name   string
		client *fakeDiamondClient
	}{
		{name: "fetch", client: &fakeDiamondClient{getErr: errors.New("unavailable")}},
		{name: "fingerprints fetch", client: &fakeDiamondClient{content: validJSON(), manifestFingerprintsGetErr: errors.New("unavailable")}},
		{name: "fingerprints validation", client: &fakeDiamondClient{content: validJSON(), manifestFingerprintsContent: `{"version":1,"fingerprints":{}}`}},
		{name: "listener", client: &fakeDiamondClient{content: validJSON(), listenErr: errors.New("unavailable")}},
		{name: "fingerprints listener", client: &fakeDiamondClient{content: validJSON(), manifestFingerprintsListenErr: errors.New("unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, err := newDiamondService(nil, true, func() (diamondClient, error) { return test.client, nil })
			if err == nil || service != nil {
				t.Fatalf("service=%v error=%v, want startup failure", service, err)
			}
			if !test.client.closed {
				t.Fatal("failed startup must close client")
			}
			if test.name == "fingerprints listener" && !test.client.cancelled {
				t.Fatal("failed fingerprints listener startup must cancel runtime config listener")
			}
		})
	}
}

func TestDiamondServiceCloseCancelsListener(t *testing.T) {
	client := &fakeDiamondClient{content: validJSON()}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.cancelled || !client.manifestFingerprintsCancelled || !client.closed {
		t.Fatalf("runtime cancelled=%v fingerprints cancelled=%v closed=%v", client.cancelled, client.manifestFingerprintsCancelled, client.closed)
	}
}
