package handler

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func packageSecretValues(state packageBindingState, bundle agentsource.Bundle, agent db.Agent) map[string]string {
	var declaration any
	_ = json.Unmarshal(bundle.Definition["configuration"], &declaration)
	stored := map[string]any{}
	for key, raw := range map[string][]byte{"custom_env": agent.CustomEnv, "custom_args": agent.CustomArgs, "runtime_config": agent.RuntimeConfig, "mcp_config": agent.McpConfig} {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			stored[key] = value
		}
	}
	root := map[string]any{"configuration": stored}
	result := map[string]string{}
	invalid := map[string]bool{}
	walkPackageSecrets(declaration, "/configuration", func(path, ref string) {
		value, exists := packageValueAtPointer(root, path).(string)
		if !exists || state.SecretRefs[path] != ref {
			invalid[ref] = true
			return
		}
		if previous, present := result[ref]; present && previous != value {
			invalid[ref] = true
			return
		}
		result[ref] = value
	})
	for ref := range invalid {
		delete(result, ref)
	}
	return result
}
func packageValueAtPointer(root any, path string) any {
	value := root
	for _, escaped := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(escaped, "~1", "/"), "~0", "~")
		switch item := value.(type) {
		case map[string]any:
			value = item[key]
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(item) {
				return nil
			}
			value = item[index]
		default:
			return nil
		}
	}
	return value
}
func (h *Handler) reusablePackageInputs(ctx context.Context, q *db.Queries, agent db.Agent, actor string, state packageBindingState, bundle agentsource.Bundle) (map[string]string, []string, error) {
	secrets := packageSecretValues(state, bundle, agent)
	// Plugin credentials always require an explicit new value, even when a
	// manually authored recipe reuses an environment secret alias.
	var plugins any
	_ = json.Unmarshal(bundle.Definition["dsh_plugins"], &plugins)
	walkPackageSecrets(plugins, "/dsh_plugins", func(_ string, ref string) { delete(secrets, ref) })
	report, err := h.packageBindingReport(ctx, q, agent, actor, state)
	if err != nil {
		return nil, nil, err
	}
	var root any
	encoded, _ := json.Marshal(bundle.Definition)
	_ = json.Unmarshal(encoded, &root)
	ready := []string{}
	for _, item := range report.Bindings {
		declaration := packageValueAtPointer(root, item.Path)
		// A missing path must never be confused with an explicit null.
		if item.Status == "ready" && packageValueHash(declaration) == packageValueHash(item.Declaration) {
			ready = append(ready, item.Path)
		}
	}
	return secrets, ready, nil
}
func (h *Handler) packageRequirementsForAgent(ctx context.Context, q *db.Queries, agent db.Agent, actor string, bundle agentsource.Bundle) (PackageRequirements, error) {
	requirements := packageRequirements(bundle)
	state, err := readPackageBindingState(ctx, q, agent)
	if err != nil {
		return requirements, err
	}
	secrets, ready, err := h.reusablePackageInputs(ctx, q, agent, actor, state, bundle)
	if err != nil {
		return requirements, err
	}
	pendingSecrets := []string{}
	for _, ref := range requirements.Secrets {
		if _, ok := secrets[ref]; !ok {
			pendingSecrets = append(pendingSecrets, ref)
		}
	}
	requirements.Secrets = pendingSecrets
	pendingBindings := []string{}
	for _, path := range requirements.DeferredBindings {
		reusable := false
		for _, current := range ready {
			if current == path {
				reusable = true
				break
			}
		}
		if !reusable {
			pendingBindings = append(pendingBindings, path)
		}
	}
	requirements.DeferredBindings = pendingBindings
	return requirements, nil
}
