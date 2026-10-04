package runtimeconfig

import (
	"strings"
	"testing"
)

func TestEventRouterConfigValidationAndSnapshot(t *testing.T) {
	ws := "11111111-1111-4111-8111-111111111111"
	agent := "22222222-2222-4222-8222-222222222222"
	target := `{"workspace_id":"` + ws + `","agent_id":"` + agent + `","tenant_org_id":"org1"}`
	for _, value := range []string{
		`{"enabled":true,"targets":[` + target + `,` + target + `]}`,
		`{"enabled":true,"targets":[{"workspace_id":"*","agent_id":"` + agent + `","tenant_org_id":"org1"}]}`,
		`{"enabled":true,"targets":[{"workspace_id":"` + ws + `","agent_id":"` + agent + `","tenant_org_id":""}]}`,
	} {
		raw := strings.Replace(validJSON(), `"runtime": {`, `"runtime":{"event_scene_router":`+value+`,`, 1)
		if _, err := ParseStrict([]byte(raw), false); err == nil {
			t.Fatal("invalid canary accepted")
		}
	}
	raw := strings.Replace(validJSON(), `"runtime": {`, `"runtime":{"event_scene_router":{"enabled":true,"targets":[`+target+`]},`, 1)
	cfg, err := ParseStrict([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	original := Snapshot{Config: cfg}
	copy := cloneSnapshot(original)
	copy.Config.Runtime.EventSceneRouter.Targets[0].TenantOrgID = "other"
	if !original.Config.Runtime.EventSceneRouter.Allows(ws, agent, "org1") {
		t.Fatal("snapshot target was mutated")
	}
}

func TestEventRouterExactCanaryTargets(t *testing.T) {
	target := EventSceneRouterTarget{WorkspaceID: "workspace", AgentID: "agent", TenantOrgID: "org"}
	c := EventSceneRouterConfig{Enabled: true, Targets: []EventSceneRouterTarget{target}}
	if !c.Allows("workspace", "agent", "org") {
		t.Fatal("target excluded")
	}
	for _, v := range []EventSceneRouterTarget{{"other", "agent", "org"}, {"workspace", "other", "org"}, {"workspace", "agent", "other"}, {"workspace", "agent", ""}} {
		if c.Allows(v.WorkspaceID, v.AgentID, v.TenantOrgID) {
			t.Fatalf("canary leaked: %+v", v)
		}
	}
	c.Enabled = false
	if c.Allows("workspace", "agent", "org") {
		t.Fatal("off accepted")
	}
	c.Enabled = true
	c.Targets = nil
	if c.Allows("workspace", "agent", "org") {
		t.Fatal("empty targeted all")
	}
}
