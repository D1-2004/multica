package service

import (
	"context"
	"encoding/json"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestASBNetworkTargetsRejectIsolationBypasses(t *testing.T) {
	for _, target := range []string{"*", "*.alibaba-inc.com", "0.0.0.0/0", "::/0", "https://host.example/a", "host.example:443", "a..example", "example.com/path", "example.com\nother.com", "-a.example", "example.com?x=1"} {
		t.Run(target, func(t *testing.T) {
			if _, err := NormalizeASBNetworkTargets([]string{target}); err == nil {
				t.Fatalf("accepted %q", target)
			}
		})
	}
	got, err := NormalizeASBNetworkTargets([]string{" Example.COM. ", "example.com", "1.2.3.4", "2001:db8::1", ""})
	if err != nil || !reflect.DeepEqual(got, []string{"1.2.3.4", "2001:db8::1", "example.com"}) {
		t.Fatalf("normalized=%v err=%v", got, err)
	}
	if _, err := NormalizeASBNetworkTargets(make([]string, 257)); err == nil {
		t.Fatal("accepted oversized allowlist")
	}
}

func TestASBNetworkPolicyMergesDependenciesAndKeepsSecretsOut(t *testing.T) {
	targets := asbConfiguredURLTargets([]byte(`{"mcpServers":{"remote":{"url":"https://mcp.example/path?token=secret","headers":{"Authorization":"https://credential.example/secret"}},"local":{"url":"http://127.0.0.1:9000"}},"BASE_URL":"https://llm.example/v1","api_key":"https://key.example/secret"}`))
	settings, err := asbNetworkSettings(ASBConfig{ServerURL: "https://multica.example/api", LLMBaseURL: "https://llm.example/v1", NetworkServiceURLs: []string{"https://identity.example/"}, NetworkAllowlist: []string{" PLATFORM.example "}}, db.AgentRuntime{Metadata: []byte(`{"asb_network_allowlist":["custom.example"]}`)}, targets)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"multica.example", "llm.example", "identity.example", "platform.example", "custom.example", "mcp.example", "mcp-gw.dingtalk.com"} {
		if !slices.Contains(settings.EffectiveTargets, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, target := range settings.EffectiveTargets {
		if strings.Contains(target, "secret") || strings.Contains(target, "credential") || strings.Contains(target, "key.example") || strings.Contains(target, "127.0.0.1") {
			t.Errorf("unsafe target %q", target)
		}
	}
	p := settings.Policy()
	if p.DefaultAction != "deny" {
		t.Fatal("policy is not deny")
	}
	other := p
	other.Egress = append(append([]ASBNetworkRule{}, p.Egress...), ASBNetworkRule{Action: "allow", Target: "another.example"})
	if p.Fingerprint() == other.Fingerprint() {
		t.Fatal("policy change did not change fingerprint")
	}
}

func TestASBCreateAlwaysSendsDenyAndPolicyFingerprint(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body asbCreateSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.NetworkPolicy.DefaultAction != "deny" || body.NetworkPolicy.Egress == nil {
			t.Errorf("policy=%+v", body.NetworkPolicy)
		}
		if body.Metadata[asbNetworkPolicyFingerprintKey] != body.NetworkPolicy.Fingerprint() {
			t.Error("fingerprint not persisted")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sandbox-test","status":{"state":"Running"},"createdAt":"2026-09-08T00:00:00Z"}`))
	}))
	defer srv.Close()
	client, err := NewASBClient(ASBClientConfig{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	input := ASBCreateSandboxInput{ImageURI: "image", TimeoutSeconds: 60, ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep infinity"}}
	if _, err = client.CreateSandbox(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.NetworkPolicy = ASBNetworkPolicy{DefaultAction: "allow"}
	if _, err = client.CreateSandbox(context.Background(), input); err == nil {
		t.Fatal("accepted allow")
	}
	input.NetworkPolicy = ASBNetworkPolicy{DefaultAction: "deny", Egress: []ASBNetworkRule{{Action: "allow", Target: "*.alibaba-inc.com"}}}
	if _, err = client.CreateSandbox(context.Background(), input); err == nil {
		t.Fatal("accepted wildcard")
	}
	if calls != 1 {
		t.Fatalf("unsafe requests reached control plane: %d", calls)
	}
}

func TestASBWarmSandboxRequiresMatchingNetworkPolicy(t *testing.T) {
	for _, fingerprint := range []string{"", "old-policy", "expected"} {
		t.Run(fingerprint, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(ASBSandbox{ID: "sandbox-test", Status: ASBSandboxStatus{State: "Running"}, Metadata: map[string]string{asbNetworkPolicyFingerprintKey: fingerprint}})
			}))
			defer srv.Close()
			client, _ := NewASBClient(ASBClientConfig{BaseURL: srv.URL, APIKey: "test-key"})
			reusable, _, err := inspectReusableASBSandbox(context.Background(), client, "sandbox-test", "expected")
			if err != nil || reusable != (fingerprint == "expected") {
				t.Fatalf("reusable=%v err=%v", reusable, err)
			}
		})
	}
}
