package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type PackageDshPluginRequirement struct {
	Ref         string `json:"ref"`
	PackageName string `json:"package_name,omitempty"`
	Version     string `json:"version,omitempty"`
	Integrity   string `json:"integrity,omitempty"`
}

type packageDshPlugin struct {
	PackageDshPluginRequirement
	Enabled bool           `json:"enabled"`
	RowID   string         `json:"row_id"`
	Config  map[string]any `json:"config"`
}

var packagePluginIntegrity = regexp.MustCompile(`^sha256-[0-9a-f]{64}$`)

func exportPackageDshPlugin(notes *agentExportNotes, plugin db.ListDshPluginsForAgentRow) (map[string]any, error) {
	if plugin.PackageName == "" || plugin.ResolvedVersion == "" || !packagePluginIntegrity.MatchString(plugin.Integrity) {
		return nil, errors.New("pin the plugin package version and integrity before exporting")
	}
	config, err := effectiveAgentDshPluginConfig(plugin)
	if err != nil {
		return nil, errors.New("repair the employee plugin configuration before exporting")
	}
	// Artifact identity, rather than a workspace UUID, makes aliases stable
	// when a recipe is copied to another workspace and exported again.
	ref := notes.resource("dsh-plugin", plugin.PackageName+"@"+plugin.ResolvedVersion+":"+plugin.Integrity, plugin.PackageName)
	ref["enabled"] = plugin.Enabled
	ref["package_name"] = plugin.PackageName
	ref["version"] = plugin.ResolvedVersion
	ref["integrity"] = plugin.Integrity
	ref["row_id"] = config.RowID
	ref["config"] = notes.configValue(config.Config, "/dsh_plugins/"+ref["ref"].(string)+"/config", "config")
	return ref, nil
}

func validatePackagePluginBindings(plugins []packageDshPlugin, bindings map[string]string) error {
	refs, ids := map[string]bool{}, map[string]bool{}
	for _, p := range plugins {
		if p.Ref == "" || refs[p.Ref] {
			return sourceRequestError(422, "plugin recipe references must be unique")
		}
		refs[p.Ref] = true
		id, err := uuid.Parse(bindings[p.Ref])
		if err != nil || id == uuid.Nil || id.String() != bindings[p.Ref] {
			return sourceRequestError(422, "select a destination plugin for "+p.Ref)
		}
		if ids[id.String()] {
			return sourceRequestError(422, "each recipe plugin needs a distinct destination binding")
		}
		ids[id.String()] = true
		if p.PackageName != "" && (p.Version == "" || !packagePluginIntegrity.MatchString(p.Integrity) || p.Config == nil) {
			return sourceRequestError(422, "plugin recipe must include pinned identity and configuration")
		}
	}
	for ref := range bindings {
		if !refs[ref] {
			return sourceRequestError(422, "unknown plugin recipe binding")
		}
	}
	return nil
}

func resolvePackagePluginConfig(recipe packageDshPlugin, row db.DshPlugin) ([]byte, error) {
	if recipe.PackageName != "" && (recipe.PackageName != row.PackageName || recipe.Version != row.ResolvedVersion || recipe.Integrity != row.Integrity) {
		return nil, sourceRequestError(http.StatusConflict, "destination plugin package, version or integrity does not match the recipe")
	}
	config := recipe.Config
	if config == nil {
		config = map[string]any{}
	}
	encoded, err := json.Marshal(AgentDshPluginConfig{RowID: recipe.RowID, Config: config})
	if err != nil {
		return nil, sourceRequestError(422, "invalid plugin recipe configuration")
	}
	checked, err := decodeAgentDshPluginOverride(encoded, db.ListDshPluginsForAgentRow{PackageName: row.PackageName, BundleRows: row.BundleRows})
	if err != nil {
		return nil, sourceRequestError(422, "plugin recipe configuration does not match its loader rows")
	}
	return json.Marshal(checked)
}

// The source confirmation already holds a transaction. Validate every target
// before replacing the set, and never install code from an untrusted manifest.
func (definition packageConfiguration) applyDshPlugins(ctx context.Context, q *db.Queries, agent db.Agent, actorID pgtype.UUID) error {
	if definition.DshPlugins == nil {
		return nil
	}
	if err := q.LockAgentDshPlugins(ctx, uuidToString(agent.ID)); err != nil {
		return err
	}
	type binding struct {
		id      pgtype.UUID
		enabled bool
		config  []byte
	}
	bindings := make([]binding, 0, len(definition.DshPlugins))
	plugins := append([]packageDshPlugin(nil), definition.DshPlugins...)
	sort.Slice(plugins, func(i, j int) bool {
		return strings.Compare(definition.pluginBindings[plugins[i].Ref], definition.pluginBindings[plugins[j].Ref]) < 0
	})
	for _, plugin := range plugins {
		id := parseUUID(definition.pluginBindings[plugin.Ref]) // validated request UUID
		row, err := q.GetDshPluginInWorkspaceForShare(ctx, db.GetDshPluginInWorkspaceForShareParams{ID: id, WorkspaceID: agent.WorkspaceID})
		if err != nil {
			return sourceRequestError(422, "destination plugin is unavailable in this workspace")
		}
		config, err := resolvePackagePluginConfig(plugin, row)
		if err != nil {
			return err
		}
		bindings = append(bindings, binding{id, plugin.Enabled, config})
	}
	if err := q.DeleteAgentDshPluginsByAgent(ctx, agent.ID); err != nil {
		return err
	}
	for _, b := range bindings {
		if err := q.AddAgentDshPlugin(ctx, db.AddAgentDshPluginParams{AgentID: agent.ID, DshPluginID: b.id, Enabled: b.enabled, ConfigOverride: b.config}); err != nil {
			return err
		}
	}
	details, _ := json.Marshal(map[string]any{"agent_id": uuidToString(agent.ID), "plugin_count": len(bindings)})
	_, err := q.CreateActivity(ctx, db.CreateActivityParams{WorkspaceID: agent.WorkspaceID, ActorID: actorID, ActorType: pgtype.Text{String: "member", Valid: true}, Action: "agent_dsh_plugins_imported", Details: details})
	return err
}

// Preserve declared references in previews while hiding literal private values
// from manually authored packages. Exports never use this special-case path.
func previewPackagePluginConfig(value any, path, field string) any {
	switch item := value.(type) {
	case map[string]any:
		if ref, ok := item["secret_ref"].(string); ok && len(item) == 1 {
			return map[string]any{"secret_ref": ref}
		}
		result := map[string]any{}
		for key, child := range item {
			result[key] = previewPackagePluginConfig(child, path+"/"+strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1"), key)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for i, child := range item {
			result[i] = previewPackagePluginConfig(child, fmt.Sprintf("%s/%d", path, i), field)
		}
		return result
	default:
		return (&agentExportNotes{}).configValue(value, path, field)
	}
}

func validatePackageActor(definition map[string]json.RawMessage, actorSource string) error {
	if actorSource != "" && (definition["a2a"] != nil || definition["dsh_plugins"] != nil) {
		return sourceRequestError(http.StatusForbidden, "identity and plugin configuration import requires a human actor")
	}
	return nil
}
