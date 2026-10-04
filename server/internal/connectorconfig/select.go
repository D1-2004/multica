package connectorconfig

import "strings"

// Select picks the authorization instance that takes effect for ctx.
// ok is false when nothing matches, which means the caller keeps the
// legacy credential. A match is final: the caller must not substitute a
// wider account when the chosen instance has no usable token.
func Select(instances []Instance, ctx CallContext) (Instance, string, bool) {
	targets := []struct {
		kind string
		id   string
	}{
		{ScopeAgent, strings.TrimSpace(ctx.AgentID)},
		{ScopeProject, strings.TrimSpace(ctx.ProjectID)},
		{ScopeEnvironment, NormalizeEnvironment(ctx.Environment)},
		{ScopeWorkspace, ""},
	}
	for _, target := range targets {
		if target.kind != ScopeWorkspace && target.id == "" {
			continue
		}
		if picked, ok := matchRank(instances, target.kind, target.id); ok {
			return picked, target.kind, true
		}
	}
	return Instance{}, "", false
}

func matchRank(instances []Instance, kind, id string) (Instance, bool) {
	var found Instance
	matched := false
	for _, instance := range instances {
		if !instance.Enabled || instance.Status == StatusDisabled {
			continue
		}
		for _, binding := range instance.Bindings {
			if !bindingMatches(binding, kind, id) {
				continue
			}
			if !matched || instance.ID < found.ID {
				found = instance
				matched = true
			}
		}
	}
	return found, matched
}

func bindingMatches(binding Binding, kind, id string) bool {
	if binding.ScopeKind != kind {
		return false
	}
	if kind == ScopeWorkspace {
		return binding.ScopeID == ""
	}
	if kind == ScopeEnvironment {
		return NormalizeEnvironment(binding.ScopeID) == id && id != ""
	}
	return binding.ScopeID == id && id != ""
}
