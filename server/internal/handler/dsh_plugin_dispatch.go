package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DshPluginSetEnvKey is the variable the runtime adapter reads to decide which
// plugins a task boots with.
//
// The adapter already consumed this before there was any UI: an operator set it
// by hand in an agent's custom_env. Composing the same value from the agent's
// bound plugins therefore needs no change in the daemon, the sandbox, or the
// image — the managed path produces exactly what the manual one did.
const DshPluginSetEnvKey = "DSH_PLUGIN_SET"

// dshPluginSetEntry is one element of the JSON array the adapter parses. The
// field names are the adapter's, not this server's.
type dshPluginSetEntry struct {
	Name      string         `json:"name"`
	Source    string         `json:"source"`
	Integrity string         `json:"integrity,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	RowID     string         `json:"row_id,omitempty"`
}

// composeAgentDshPluginSet builds the DSH_PLUGIN_SET value for one agent.
//
// Returns "" when the agent has no enabled plugins, which is the signal to
// leave whatever the operator put in custom_env alone.
func (h *Handler) composeAgentDshPluginSet(ctx context.Context, agentID, workspaceID pgtype.UUID) (string, error) {
	rows, err := h.Queries.ListDshPluginsForAgent(ctx, db.ListDshPluginsForAgentParams{
		AgentID:     agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return "", err
	}
	entries := make([]dshPluginSetEntry, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		source, err := h.dshPluginDeliverySource(ctx, db.DshPlugin{
			PackageName: row.PackageName,
			SourceKind:  row.SourceKind,
			SourceSpec:  row.SourceSpec,
			ArtifactKey: row.ArtifactKey,
		})
		if err != nil {
			return "", err
		}
		entry := dshPluginSetEntry{
			Name:      row.PackageName,
			Source:    source,
			Integrity: row.Integrity,
		}
		// A patch replaces a loader row's config wholesale, so an empty config
		// must not be emitted: it would wipe the defaults the plugin's own
		// bundle layer supplies.
		var config map[string]any
		if len(row.Config) > 0 {
			if err := json.Unmarshal(row.Config, &config); err != nil {
				slog.Warn("failed to decode a DSH plugin config",
					"plugin", row.PackageName, "error", err)
				continue
			}
		}
		if len(config) > 0 {
			entry.Config = config
			entry.RowID = row.ConfigRow
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// applyAgentDshPluginSet layers the composed plugin set over an agent's
// custom_env for a task claim.
//
// Only for a DSH runtime: every other provider ignores the variable, and
// reading the binding table on every claim would be wasted work. When the agent
// has no bound plugins the operator's own value survives untouched, so the
// manual path this replaces keeps working.
func (h *Handler) applyAgentDshPluginSet(
	ctx context.Context,
	provider string,
	isExternalA2A bool,
	agentID, workspaceID pgtype.UUID,
	customEnv map[string]string,
) (map[string]string, error) {
	if provider != "dsh" || isExternalA2A {
		// An external A2A turn has already had agent-owned environment
		// stripped, precisely so an owner's credentials do not reach a caller
		// from outside the workspace. A plugin's configuration is agent-owned
		// too, so re-adding it here would undo that.
		return customEnv, nil
	}
	composed, err := h.composeAgentDshPluginSet(ctx, agentID, workspaceID)
	if err != nil {
		// Returning the plain environment would silently run the task without
		// the plugins it is configured with, which is a different task. Fail
		// the claim instead and let it be retried.
		return customEnv, err
	}
	if composed == "" {
		return customEnv, nil
	}
	if customEnv == nil {
		customEnv = map[string]string{}
	}
	customEnv[DshPluginSetEnvKey] = composed
	return customEnv, nil
}
