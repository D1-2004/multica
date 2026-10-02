package main

import "testing"

func TestEventRouteDeploymentOverride(t *testing.T) {
	ws := "11111111-1111-4111-8111-111111111111"
	agent := "22222222-2222-4222-8222-222222222222"
	provider, err := newEventRouteConfigProvider(nil, `{"enabled":true,"targets":[{"workspace_id":"`+ws+`","agent_id":"`+agent+`","tenant_org_id":"org1"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	route, version := provider(ws, agent, "org1")
	if route != "unified" || version == "" {
		t.Fatal("deployment canary was not selected")
	}
	if route, _ := provider(ws, agent, "org2"); route != "legacy" {
		t.Fatal("tenant crossed")
	}
	for _, raw := range []string{`{"enabled":true,"unknown":1}`, `{"enabled":true} {}`, `{"enabled":true,"targets":[{"workspace_id":"*"}]}`} {
		if _, err := newEventRouteConfigProvider(nil, raw); err == nil {
			t.Fatal("invalid override accepted")
		}
	}
	provider, err = newEventRouteConfigProvider(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if route, _ := provider(ws, agent, "org1"); route != "legacy" {
		t.Fatal("default did not retain legacy")
	}
}
