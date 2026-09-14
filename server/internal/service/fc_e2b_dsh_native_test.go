package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"strings"
	"testing"
	"time"
)

func TestDSHNativeAuthorityFromEnvironment(t *testing.T) {
	t.Setenv("MULTICA_FC_E2B_SERVER_URL", "https://production-relay.test")
	t.Setenv("MULTICA_APP_URL", "https://pre.multica.test/")
	config := FCE2BConfigFromEnv()
	if config.DSHNativeAuthority != "https://pre.multica.test" || config.ServerURL != "https://production-relay.test" {
		t.Fatal("DSH authority and task relay were conflated")
	}
	t.Setenv("MULTICA_APP_URL", "")
	if FCE2BConfigFromEnv().DSHNativeAuthority != "" {
		t.Fatal("missing DSH authority fell back to the task relay")
	}
}

func TestDSHNativeGatewayRequiresExactLiveReceipt(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, State: "running", Generation: 3, SandboxID: "sbx-fixture"}
	config := FCE2BConfig{ServerURL: "https://production-relay.test", DSHNativeAuthority: "https://pre.multica.test", Domain: "fc.example.test", APIKey: "fixture", APIURL: "https://api.fc.example.test"}
	origin, authority, err := dshNativeGatewayAddress(config, host)
	if err != nil {
		t.Fatal(err)
	}
	if authority != "https://pre.multica.test" {
		t.Fatal("native capability was routed to the task relay")
	}
	bridge := newDSHNativeAuthorityBridge("test-secret")
	receipt := dshNativeGatewayReceipt{PublicKey: bridge.publicKey(), Version: 2, Ready: true, WorkspaceID: host.WorkspaceID.String(), AgentID: host.AgentID.String(), Generation: host.Generation, SandboxID: host.SandboxID, Port: DSHNativeGatewayPort, Authority: authority, Origin: origin}
	encode := func(r dshNativeGatewayReceipt) string { b, _ := json.Marshal(r); return string(b) }
	for _, mutate := range []func(*dshNativeGatewayReceipt){
		func(r *dshNativeGatewayReceipt) { r.Version = 1 }, func(r *dshNativeGatewayReceipt) { r.Ready = false },
		func(r *dshNativeGatewayReceipt) { r.WorkspaceID = uuid.NewString() }, func(r *dshNativeGatewayReceipt) { r.AgentID = uuid.NewString() },
		func(r *dshNativeGatewayReceipt) { r.Generation++ }, func(r *dshNativeGatewayReceipt) { r.SandboxID = "sbx-other" },
		func(r *dshNativeGatewayReceipt) { r.Port++ }, func(r *dshNativeGatewayReceipt) { r.Authority = "https://other.test" },
		func(r *dshNativeGatewayReceipt) { r.Origin = "https://other.test" },
	} {
		changed := receipt
		mutate(&changed)
		if validateDSHNativeGatewayReceipt(encode(changed), host, origin, authority, bridge.publicKey()) == nil {
			t.Fatal("mismatched receipt accepted")
		}
	}
	runner := &fakeCommandRunner{out: []string{encode(receipt)}}
	launcher := &FCE2BLauncher{nativeAuthority: bridge, ConfigProvider: func() FCE2BConfig { return config }, Runner: runner}
	got, err := launcher.DSHNativeGatewayURL(context.Background(), host)
	if err != nil || got != origin {
		t.Fatal("valid receipt failed", err)
	}
	if len(runner.calls) != 1 || !strings.Contains(strings.Join(runner.calls[0].args, " "), "sbx-fixture -- /usr/local/libexec/multica-dsh-host --gateway-health") || !runner.deadlines[0] || runner.timeouts[0] > 10*time.Second {
		t.Fatal("readiness did not use the bounded fixed command")
	}
	runner.errs = []error{errors.New("old image: unknown argument")}
	if got, err := launcher.DSHNativeGatewayURL(context.Background(), host); err == nil || got != "" {
		t.Fatal("old image exposed native entry")
	}
}

func TestDSHNativeGatewayRejectsUntrustedAddresses(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, State: "running", Generation: 1, SandboxID: "sbx-fixture"}
	for _, config := range []FCE2BConfig{
		{ServerURL: "https://production-relay.test", Domain: "fc.test"},
		{DSHNativeAuthority: "https://api.test:443", Domain: "fc.test"},
		{DSHNativeAuthority: "https://API.test", Domain: "fc.test"},
		{DSHNativeAuthority: "https://api.test?", Domain: "fc.test"},
		{DSHNativeAuthority: "http://api.test", Domain: "fc.test"}, {DSHNativeAuthority: "https://user@api.test", Domain: "fc.test"},
		{DSHNativeAuthority: "https://api.test/path", Domain: "fc.test"}, {DSHNativeAuthority: "https://api.test?x=1", Domain: "fc.test"},
		{DSHNativeAuthority: "https://api.test#fragment", Domain: "fc.test"}, {DSHNativeAuthority: "https://api.test", Domain: "fc.test/redirect"},
		{DSHNativeAuthority: "https://api.test", Domain: "fc.test:443"}, {DSHNativeAuthority: "https://api.test", Domain: "fc..test"},
	} {
		if _, _, err := dshNativeGatewayAddress(config, host); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	config := FCE2BConfig{DSHNativeAuthority: "https://api.test", Domain: "fc.test"}
	host.SandboxID = "../other"
	if _, _, err := dshNativeGatewayAddress(config, host); err == nil {
		t.Fatal("unsafe sandbox identity accepted")
	}
	host.SandboxID = "sbx-fixture"
	host.State = "retiring"
	if _, _, err := dshNativeGatewayAddress(config, host); err == nil {
		t.Fatal("retiring Host accepted")
	}
}
