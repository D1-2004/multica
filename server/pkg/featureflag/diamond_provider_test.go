package featureflag

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestDiamondProviderAppliesCompleteSnapshotsAtomically(t *testing.T) {
	t.Parallel()
	provider := NewDiamondProvider()

	count, digest, err := provider.ApplyJSON([]byte(`{
	  "auto": {"prompt": "policy-v1"}
}`))
	if err != nil {
		t.Fatalf("ApplyJSON valid snapshot: %v", err)
	}
	if count != 1 || len(digest) != 64 {
		t.Fatalf("ApplyJSON metadata = count %d digest %q", count, digest)
	}
	decision, found := provider.Lookup(context.Background(), DispatchAutoRuntimePromptFlagKey)
	if !found || decision.Variant != "policy-v1" || decision.Source != "diamond" {
		t.Fatalf("initial decision = %+v, found=%v", decision, found)
	}

	if _, _, err := provider.ApplyJSON([]byte(`{"auto":`)); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
	decision, found = provider.Lookup(context.Background(), DispatchAutoRuntimePromptFlagKey)
	if !found || decision.Variant != "policy-v1" {
		t.Fatalf("invalid update replaced last valid snapshot: %+v, found=%v", decision, found)
	}

	count, _, err = provider.ApplyJSON([]byte(`{}`))
	if err != nil {
		t.Fatalf("ApplyJSON empty object: %v", err)
	}
	if count != 0 {
		t.Fatalf("empty object count = %d, want 0", count)
	}
	if _, found := provider.Lookup(context.Background(), DispatchAutoRuntimePromptFlagKey); found {
		t.Fatal("empty object must atomically clear the Diamond snapshot")
	}
}

func TestDiamondProviderLoadsSurfacePrompts(t *testing.T) {
	t.Parallel()
	provider := NewDiamondProvider()

	count, _, err := provider.ApplyJSON([]byte(`{
  "issue": {"prompt": "ISSUE POLICY"},
  "chat": {"prompt": "CHAT POLICY"},
  "auto": {"prompt": "AUTO POLICY"}
}`))
	if err != nil {
		t.Fatalf("ApplyJSON surface prompts: %v", err)
	}
	if count != 3 {
		t.Fatalf("surface prompt count = %d, want 3", count)
	}
	for key, want := range map[string]string{
		DispatchIssueRuntimePromptFlagKey: "ISSUE POLICY",
		DispatchChatRuntimePromptFlagKey:  "CHAT POLICY",
		DispatchAutoRuntimePromptFlagKey:  "AUTO POLICY",
	} {
		decision, found := provider.Lookup(context.Background(), key)
		if !found || decision.Variant != want || decision.Source != "diamond" {
			t.Errorf("Lookup(%q) = %+v, found=%v; want %q from Diamond", key, decision, found, want)
		}
	}
}

func TestDiamondProviderRejectsInvalidRulesWithoutReplacingSnapshot(t *testing.T) {
	t.Parallel()
	provider := NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{"chat":{"prompt":"stable"}}`)); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	invalid := []string{
		`null`,
		`{"chat":null}`,
		`{"chat":{"prompt":"next","unknown":true}}`,
		`{"chat":{"prompt":42}}`,
		`{"stable":{"prompt":"unknown section"}}`,
		`{"":{"prompt":"empty section"}}`,
		`{} {}`,
	}
	for _, body := range invalid {
		if _, _, err := provider.ApplyJSON([]byte(body)); err == nil {
			t.Errorf("ApplyJSON(%q) unexpectedly succeeded", body)
		}
		decision, found := provider.Lookup(context.Background(), DispatchChatRuntimePromptFlagKey)
		if !found || decision.Variant != "stable" {
			t.Fatalf("invalid update %q replaced the valid snapshot: %+v, found=%v", body, decision, found)
		}
	}
}

func TestDiamondAlwaysStartsWithoutEnableSwitch(t *testing.T) {
	path := writeTempFile(t, "flags.yaml", "yaml_flag:\n  default: true\nenv_flag:\n  default: false\n")
	t.Setenv(EnvFlagFile, path)
	t.Setenv("FF_ENV_FLAG", "true")

	factoryCalled := false
	service, err := newServiceFromEnvWithDiamondFactory(func(diamondClientSettings) (diamondConfigClient, error) {
		factoryCalled = true
		return &fakeDiamondClient{content: `{"chat":{"prompt":"diamond-chat"}}`}, nil
	})
	if err != nil {
		t.Fatalf("always-on Diamond changed startup behavior: %v", err)
	}
	if !factoryCalled {
		t.Fatal("Diamond client was not created without an enable switch")
	}
	if !service.IsEnabled(context.Background(), "env_flag", false) {
		t.Fatal("existing FF_ override did not retain precedence")
	}
	if !service.IsEnabled(context.Background(), "yaml_flag", false) {
		t.Fatal("existing YAML provider behavior changed")
	}
	if service.IsEnabled(context.Background(), "missing", false) {
		t.Fatal("caller default behavior changed")
	}
	if got := service.Variant(context.Background(), DispatchChatRuntimePromptFlagKey, "fallback"); got != "diamond-chat" {
		t.Fatalf("Diamond prompt = %q, want diamond-chat", got)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDiamondProviderConcurrentLookupAndReplacement(t *testing.T) {
	t.Parallel()
	provider := NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{"auto":{"prompt":"a"}}`)); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				decision, found := provider.Lookup(context.Background(), DispatchAutoRuntimePromptFlagKey)
				if !found || (decision.Variant != "a" && decision.Variant != "b") {
					t.Errorf("observed partial snapshot: %+v, found=%v", decision, found)
					return
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		variant := "a"
		if i%2 == 1 {
			variant = "b"
		}
		body := `{"auto":{"prompt":"` + variant + `"}}`
		if _, _, err := provider.ApplyJSON([]byte(body)); err != nil {
			t.Fatalf("replace snapshot: %v", err)
		}
	}
	wg.Wait()
}

type fakeDiamondClient struct {
	content    string
	getErr     error
	listenErr  error
	cancelErr  error
	onChange   func(string)
	dataID     string
	group      string
	cancelled  bool
	closed     bool
	lifecycle  []string
}

func (c *fakeDiamondClient) GetConfig(dataID, group string) (string, error) {
	c.dataID = dataID
	c.group = group
	return c.content, c.getErr
}

func (c *fakeDiamondClient) ListenConfig(dataID, group string, onChange func(string)) error {
	c.dataID = dataID
	c.group = group
	c.onChange = onChange
	return c.listenErr
}

func (c *fakeDiamondClient) CancelListenConfig(dataID, group string) error {
	c.dataID = dataID
	c.group = group
	c.cancelled = true
	c.lifecycle = append(c.lifecycle, "cancel")
	return c.cancelErr
}

func (c *fakeDiamondClient) CloseClient() {
	c.closed = true
	c.lifecycle = append(c.lifecycle, "close")
}

func TestNewServiceFromEnvUsesDiamondBetweenEnvAndYAML(t *testing.T) {
	path := writeTempFile(t, "flags.yaml", "dispatch_issue_runtime_prompt:\n  default: true\n  variant: yaml-issue\ndispatch_chat_runtime_prompt:\n  default: true\n  variant: yaml-chat\n")
	t.Setenv(EnvFlagFile, path)
	t.Setenv(EnvDiamondDataID, "")
	t.Setenv(EnvDiamondGroup, "")
	t.Setenv("FF_DISPATCH_ISSUE_RUNTIME_PROMPT", "env-issue")

	client := &fakeDiamondClient{content: `{"issue":{"prompt":"diamond-issue"},"chat":{"prompt":"diamond-chat"}}`}
	var settings diamondClientSettings
	service, err := newServiceFromEnvWithDiamondFactory(func(got diamondClientSettings) (diamondConfigClient, error) {
		settings = got
		return client, nil
	})
	if err != nil {
		t.Fatalf("newServiceFromEnvWithDiamondFactory: %v", err)
	}
	if got := service.Variant(context.Background(), DispatchIssueRuntimePromptFlagKey, "fallback"); got != "env-issue" {
		t.Fatal("FF_ override must beat Diamond and YAML")
	}
	if got := service.Variant(context.Background(), DispatchChatRuntimePromptFlagKey, "fallback"); got != "diamond-chat" {
		t.Fatal("Diamond must beat YAML when no FF_ override is present")
	}
	if settings.Endpoint != "jmenv.tbsite.net:8080" || settings.EndpointContextPath != "diamond-server" ||
		settings.ClusterName != "diamond" || settings.NamespaceID != "" || settings.AppName != "dt-fde-multica" {
		t.Fatalf("Diamond settings = %+v", settings)
	}
	if client.dataID != DefaultDiamondDataID || client.group != DefaultDiamondGroup {
		t.Fatalf("Diamond coordinates = %q/%q", client.dataID, client.group)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.cancelled || !client.closed {
		t.Fatalf("Diamond lifecycle not closed: cancelled=%v closed=%v", client.cancelled, client.closed)
	}
	if got := strings.Join(client.lifecycle, ","); got != "cancel,close" {
		t.Fatalf("Diamond shutdown order = %q, want cancel,close", got)
	}
}

func TestDiamondListenerUpdatesWithoutRestartAndRejectsInvalidJSON(t *testing.T) {
	t.Setenv(EnvFlagFile, "")
	t.Setenv(EnvDiamondDataID, "custom.json")
	t.Setenv(EnvDiamondGroup, "CUSTOM_GROUP")

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	client := &fakeDiamondClient{content: `{"auto":{"prompt":"v1"}}`}
	service, err := newServiceFromEnvWithDiamondFactory(func(diamondClientSettings) (diamondConfigClient, error) {
		return client, nil
	}, WithLogger(logger))
	if err != nil {
		t.Fatalf("newServiceFromEnvWithDiamondFactory: %v", err)
	}
	if got := service.Variant(context.Background(), DispatchAutoRuntimePromptFlagKey, "fallback"); got != "v1" {
		t.Fatalf("initial variant = %q", got)
	}
	if client.onChange == nil {
		t.Fatal("ListenConfig callback was not registered")
	}

	client.onChange(`{"auto":{"prompt":"v2"}}`)
	if got := service.Variant(context.Background(), DispatchAutoRuntimePromptFlagKey, "fallback"); got != "v2" {
		t.Fatalf("updated variant = %q", got)
	}
	client.onChange(`{"live":"secret-payload"`)
	if got := service.Variant(context.Background(), DispatchAutoRuntimePromptFlagKey, "fallback"); got != "v2" {
		t.Fatalf("invalid update replaced last valid variant: %q", got)
	}
	if strings.Contains(logs.String(), "secret-payload") {
		t.Fatalf("Diamond log leaked configuration content: %s", logs.String())
	}

	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.dataID != "custom.json" || client.group != "CUSTOM_GROUP" {
		t.Fatalf("custom coordinates = %q/%q", client.dataID, client.group)
	}
}

func TestDiamondUnavailableFailsOpenWithoutLoggingContent(t *testing.T) {
	path := writeTempFile(t, "flags.yaml", "yaml_flag:\n  default: true\n")
	t.Setenv(EnvFlagFile, path)

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	service, err := newServiceFromEnvWithDiamondFactory(func(diamondClientSettings) (diamondConfigClient, error) {
		return nil, errors.New("secret-content-must-not-leak")
	}, WithLogger(logger))
	if err != nil {
		t.Fatalf("Diamond failure must fail open, got %v", err)
	}
	if !service.IsEnabled(context.Background(), "yaml_flag", false) {
		t.Fatal("YAML fallback was not retained")
	}
	if strings.Contains(logs.String(), "secret-content-must-not-leak") {
		t.Fatalf("Diamond log leaked error/config content: %s", logs.String())
	}
	for _, want := range []string{DefaultDiamondDataID, DefaultDiamondGroup, "rules=0", "sha256="} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("Diamond fail-open log missing %q: %s", want, logs.String())
		}
	}
}

func TestDiamondGetFailureStillRegistersListener(t *testing.T) {
	t.Setenv(EnvFlagFile, "")
	client := &fakeDiamondClient{getErr: errors.New("unavailable")}
	service, err := newServiceFromEnvWithDiamondFactory(func(diamondClientSettings) (diamondConfigClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("GetConfig failure must fail open, got %v", err)
	}
	if client.onChange == nil {
		t.Fatal("GetConfig failure must not prevent ListenConfig registration")
	}
	client.onChange(`{"issue":{"prompt":"recovered"}}`)
	if got := service.Variant(context.Background(), DispatchIssueRuntimePromptFlagKey, "fallback"); got != "recovered" {
		t.Fatal("listener did not recover after initial GetConfig failure")
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
