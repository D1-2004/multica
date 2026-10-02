package connectorconfig

import "testing"

func inst(id string, enabled bool, bindings ...Binding) Instance {
	return Instance{ID: id, Enabled: enabled, Status: StatusActive, Bindings: bindings}
}

func TestSelectPriority(t *testing.T) {
	instances := []Instance{
		inst("ws", true, Binding{ScopeKind: ScopeWorkspace}),
		inst("env", true, Binding{ScopeKind: ScopeEnvironment, ScopeID: "staging"}),
		inst("proj", true, Binding{ScopeKind: ScopeProject, ScopeID: "project-1"}),
		inst("agent", true, Binding{ScopeKind: ScopeAgent, ScopeID: "agent-1"}),
	}
	got, rank, ok := Select(instances, CallContext{AgentID: "agent-1", ProjectID: "project-1", Environment: "Staging"})
	if !ok || got.ID != "agent" || rank != ScopeAgent {
		t.Fatalf("agent rank = %+v %s %v", got, rank, ok)
	}
	got, rank, ok = Select(instances, CallContext{AgentID: "agent-2", ProjectID: "project-1", Environment: "staging"})
	if !ok || got.ID != "proj" || rank != ScopeProject {
		t.Fatalf("project rank = %+v %s %v", got, rank, ok)
	}
	got, rank, ok = Select(instances, CallContext{AgentID: "agent-2", ProjectID: "other", Environment: "STAGING"})
	if !ok || got.ID != "env" || rank != ScopeEnvironment {
		t.Fatalf("environment rank = %+v %s %v", got, rank, ok)
	}
	got, rank, ok = Select(instances, CallContext{AgentID: "agent-2"})
	if !ok || got.ID != "ws" || rank != ScopeWorkspace {
		t.Fatalf("workspace rank = %+v %s %v", got, rank, ok)
	}
}

func TestSelectSkipsDisabledAndDoesNotInventAMatch(t *testing.T) {
	instances := []Instance{
		inst("agent-off", false, Binding{ScopeKind: ScopeAgent, ScopeID: "agent-1"}),
		inst("ws", true, Binding{ScopeKind: ScopeWorkspace}),
	}
	instances[0].Status = StatusDisabled
	got, rank, ok := Select(instances, CallContext{AgentID: "agent-1"})
	if !ok || got.ID != "ws" || rank != ScopeWorkspace {
		t.Fatalf("disabled agent should fall through to workspace, got %+v %s %v", got, rank, ok)
	}
	if _, _, ok := Select(nil, CallContext{AgentID: "agent-1"}); ok {
		t.Fatal("no instances is not a match")
	}
}

func TestSelectEnabledAgentWinsOverWorkspace(t *testing.T) {
	instances := []Instance{
		inst("ws", true, Binding{ScopeKind: ScopeWorkspace}),
		inst("agent", true, Binding{ScopeKind: ScopeAgent, ScopeID: "agent-1"}),
	}
	got, _, ok := Select(instances, CallContext{AgentID: "agent-1"})
	if !ok || got.ID != "agent" {
		t.Fatalf("got %+v %v", got, ok)
	}
}

func TestNormalizeProvider(t *testing.T) {
	if got, ok := NormalizeProvider(" GitHub "); !ok || got != "github" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := NormalizeProvider("Git Hub"); ok {
		t.Fatal("spaces are not a provider key")
	}
}
