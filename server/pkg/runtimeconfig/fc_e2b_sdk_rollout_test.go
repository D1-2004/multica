package runtimeconfig

import (
	"strings"
	"testing"
)

// withSDKRollout places runtime.fc_e2b_sdk_rollout into the valid document.
func withSDKRollout(rollout string) string {
	return strings.Replace(validJSON(), `"runtime": {`, `"runtime": {"fc_e2b_sdk_rollout":`+rollout+`,`, 1)
}

func TestParseStrictReadsFCE2BSDKRollout(t *testing.T) {
	if cfg := mustParseConfig(t, validJSON()); cfg.Runtime.FCE2BSDKRollout != nil {
		t.Fatalf("absent rollout = %#v", cfg.Runtime.FCE2BSDKRollout)
	}
	cfg := mustParseConfig(t, withSDKRollout(`{"enabled":true,"workspace_ids":["11111111-1111-1111-1111-111111111111"],"agent_ids":["22222222-2222-2222-2222-222222222222"],"runtime_ids":["33333333-3333-3333-3333-333333333333"],"percent":5}`))
	got := cfg.Runtime.FCE2BSDKRollout
	if got == nil || !got.Enabled || got.WorkspaceIDs[0] != "11111111-1111-1111-1111-111111111111" || got.AgentIDs[0] != "22222222-2222-2222-2222-222222222222" ||
		got.RuntimeIDs[0] != "33333333-3333-3333-3333-333333333333" || got.Percent != 5 {
		t.Fatalf("rollout = %#v", got)
	}
	if off := mustParseConfig(t, withSDKRollout(`{"enabled":false}`)).Runtime.FCE2BSDKRollout; off == nil || off.Enabled {
		t.Fatalf("switched-off rollout = %#v", off)
	}
}

func TestParseStrictRejectsInvalidFCE2BSDKRollout(t *testing.T) {
	for _, rollout := range []string{
		`{"enabled":true,"percent":101}`,
		`{"percent":-1}`,
		`{"agent_ids":["agent-1"]}`,
		`{"agent_ids":["00000000-0000-0000-0000-000000000000"]}`,
		// Identifiers are compared as written, so only canonical ones count.
		`{"workspace_ids":[" 11111111-1111-1111-1111-111111111111 "]}`,
		`{"runtime_ids":["AAAAAAAA-1111-1111-1111-111111111111"]}`,
		`{"agent_ids":["22222222-2222-2222-2222-222222222222","22222222-2222-2222-2222-222222222222"]}`,
		`{"workspaces":["11111111-1111-1111-1111-111111111111"]}`,
		`{"enabled":"yes"}`,
		`[]`,
	} {
		if _, err := ParseStrict([]byte(withSDKRollout(rollout)), true); err == nil {
			t.Fatalf("invalid rollout accepted: %s", rollout)
		}
	}
}

// A rejected publication keeps the previous snapshot, rollout included.
func TestDiamondServiceRetainsFCE2BSDKRolloutOnInvalidUpdate(t *testing.T) {
	client := &fakeDiamondClient{content: withSDKRollout(`{"enabled":true,"agent_ids":["22222222-2222-2222-2222-222222222222"]}`)}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if first := service.Current().Config.Runtime.FCE2BSDKRollout; first == nil || !first.Enabled {
		t.Fatalf("loaded rollout = %#v", first)
	}
	client.onChange(withSDKRollout(`{"enabled":false}`))
	second := service.Current()
	if second.Generation != 2 || second.Config.Runtime.FCE2BSDKRollout.Enabled {
		t.Fatalf("switch-off = %#v", second.Config.Runtime.FCE2BSDKRollout)
	}
	client.onChange(withSDKRollout(`{"enabled":true,"percent":500}`))
	if kept := service.Current(); kept.Generation != 2 || kept.Config.Runtime.FCE2BSDKRollout.Enabled {
		t.Fatalf("invalid update replaced the snapshot: %#v", kept.Config.Runtime.FCE2BSDKRollout)
	}
	client.onChange(validJSON())
	if removed := service.Current(); removed.Generation != 3 || removed.Config.Runtime.FCE2BSDKRollout != nil {
		t.Fatalf("removed = %#v", removed.Config.Runtime.FCE2BSDKRollout)
	}
}

func TestSnapshotFCE2BSDKRolloutIsACopy(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, withSDKRollout(`{"enabled":true,"agent_ids":["22222222-2222-2222-2222-222222222222"]}`)))
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	first := service.Current()
	first.Config.Runtime.FCE2BSDKRollout.Enabled = false
	first.Config.Runtime.FCE2BSDKRollout.AgentIDs[0] = "mutated"
	current := service.Current().Config.Runtime.FCE2BSDKRollout
	if !current.Enabled || current.AgentIDs[0] != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("Current returned the service-owned rollout: %#v", current)
	}
}
