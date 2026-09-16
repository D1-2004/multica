package handler

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshplugin"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) syncAgentNativePlugins(ctx context.Context, agent db.Agent) error {
	if h.FCE2BLauncher == nil || h.FCE2BLauncher.SyncDSHProfileSource == nil || agent.RuntimeMode != "cloud" || !agent.RuntimeID.Valid {
		return nil
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return err
	}
	if runtime.Provider != "dsh" || !service.IsFCE2BRuntime(runtime) {
		return nil
	}
	return h.FCE2BLauncher.SyncDSHNativePlugins(ctx, dshhost.Key{WorkspaceID: uuid.UUID(agent.WorkspaceID.Bytes), AgentID: uuid.UUID(agent.ID.Bytes)})
}

func nativeSnapshotChanged(snapshot dshprofile.NativeSnapshot, baseline dshprofile.Descriptor) bool {
	if len(snapshot.Plugins) != len(baseline.Plugins) {
		return true
	}
	byName := map[string]dshprofile.Plugin{}
	for _, plugin := range baseline.Plugins {
		byName[plugin.PackageName] = plugin
	}
	for _, plugin := range snapshot.Plugins {
		before, ok := byName[plugin.PackageName]
		if !ok || before.Version != plugin.Version || len(plugin.Patches) > 0 {
			return true
		}
	}
	return false
}

type nativeResolvedPackage struct {
	resolved *dshplugin.Resolved
	source   dshplugin.Source
	artifact string
}

// Native filesystem changes enter the same database source and immutable build
// pipeline as workbench edits. Never replace the shared workspace package when
// a single employee upgrades it in dshmarket.
func (h *Handler) syncNativeDSHPlugins(ctx context.Context, conn *pgxpool.Conn, key dshhost.Key, template string, snapshot dshprofile.NativeSnapshot) error {
	if err := snapshot.Validate(key.WorkspaceID.String(), key.AgentID.String()); err != nil {
		return err
	}
	revision, _ := strconv.ParseInt(snapshot.BaseRevision, 10, 64)
	var rawDescriptor string
	if err := conn.QueryRow(ctx, `SELECT descriptor_json FROM dsh_profile_revision WHERE workspace_id=$1 AND agent_id=$2 AND revision=$3`, key.WorkspaceID, key.AgentID, revision).Scan(&rawDescriptor); err != nil {
		return err
	}
	var baseline dshprofile.Descriptor
	if json.Unmarshal([]byte(rawDescriptor), &baseline) != nil {
		return errors.New("invalid native plugin baseline")
	}
	if !nativeSnapshotChanged(snapshot, baseline) {
		return nil
	}
	var applied, synced int64
	var fingerprint string
	if err := conn.QueryRow(ctx, `SELECT applied_revision,native_sync_revision,native_sync_fingerprint FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID).Scan(&applied, &synced, &fingerprint); err != nil {
		return err
	}
	if applied != revision || (synced == revision && fingerprint == snapshot.Fingerprint) {
		return nil
	}
	workspace := pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}
	agent := pgtype.UUID{Bytes: key.AgentID, Valid: true}
	q := db.New(conn)
	currentRows, err := q.ListDshPluginsForAgent(ctx, db.ListDshPluginsForAgentParams{AgentID: agent, WorkspaceID: workspace})
	if err != nil {
		return err
	}
	current, err := dshProfileSource(template, currentRows)
	if err != nil {
		return err
	}
	packages := map[string]dshprofile.SourcePlugin{}
	for _, p := range current.Plugins {
		packages[p.PackageName] = p
	}
	resolved := map[string]nativeResolvedPackage{}
	for _, plugin := range snapshot.Plugins {
		if old, ok := packages[plugin.PackageName]; ok && old.Version == plugin.Version {
			continue
		}
		source, err := dshplugin.ParseSource(plugin.PackageName + "@" + plugin.Version)
		if err != nil {
			return err
		}
		pkg, err := dshPluginResolver().Resolve(ctx, source)
		if err != nil {
			return errors.New("native plugin package could not be resolved")
		}
		if pkg.PackageName != plugin.PackageName || pkg.Version != plugin.Version {
			return errors.New("native plugin package identity changed")
		}
		artifact, err := h.storeDshPluginArtifact(ctx, workspace, pkg.PackageName, pkg.Archive)
		if err != nil || artifact == "" {
			return errors.New("native plugin package could not be stored")
		}
		resolved[plugin.PackageName] = nativeResolvedPackage{pkg, pinnedSource(source, pkg), artifact}
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT applied_revision,native_sync_revision,native_sync_fingerprint FROM dsh_employee_profile WHERE workspace_id=$1 AND agent_id=$2 FOR UPDATE`, key.WorkspaceID, key.AgentID).Scan(&applied, &synced, &fingerprint); err != nil {
		return err
	}
	if applied != revision || (synced == revision && fingerprint == snapshot.Fingerprint) {
		return nil
	}
	q = db.New(tx)
	base, err := (dshprofile.Store{DB: tx}).PrivateSource(ctx, key, revision)
	if err != nil {
		return err
	}
	current, err = readDSHProfileSource(ctx, q, key, base.TemplateID)
	if err != nil {
		return err
	}
	// Synchronize only native deltas. Unchanged native packages must not
	// overwrite workbench edits already submitted against this revision.
	baselineByName := map[string]dshprofile.Plugin{}
	for _, plugin := range baseline.Plugins {
		baselineByName[plugin.PackageName] = plugin
	}
	currentRows, err = q.ListDshPluginsForAgent(ctx, db.ListDshPluginsForAgentParams{AgentID: agent, WorkspaceID: workspace})
	if err != nil {
		return err
	}
	rowsByName := map[string]db.ListDshPluginsForAgentRow{}
	for _, row := range currentRows {
		rowsByName[row.PackageName] = row
	}
	installed := map[string]bool{}
	for _, plugin := range snapshot.Plugins {
		installed[plugin.PackageName] = true
		if before, ok := baselineByName[plugin.PackageName]; ok && before.Version == plugin.Version && len(plugin.Patches) == 0 {
			continue
		}
		prior, exists := rowsByName[plugin.PackageName]
		value := AgentDshPluginConfig{Config: map[string]any{}}
		if exists {
			value, err = storedAgentDshPluginConfig(prior)
			if err != nil {
				return err
			}
		}
		pkg, hasResolved := resolved[plugin.PackageName]
		if !exists {
			catalog, lookupErr := q.GetDshPluginByWorkspaceAndPackage(ctx, db.GetDshPluginByWorkspaceAndPackageParams{WorkspaceID: workspace, PackageName: plugin.PackageName})
			if errors.Is(lookupErr, pgx.ErrNoRows) {
				if !hasResolved {
					return errors.New("native package provenance unavailable")
				}
				rows, _ := json.Marshal(pkg.resolved.BundleRows)
				catalog, lookupErr = q.CreateDshPlugin(ctx, db.CreateDshPluginParams{WorkspaceID: workspace, PackageName: plugin.PackageName, DisplayName: plugin.PackageName, Description: pkg.resolved.Description, Homepage: pkg.resolved.Homepage, SourceKind: string(pkg.source.Kind), SourceSpec: pkg.source.Spec, ResolvedVersion: pkg.resolved.Version, Integrity: pkg.resolved.Integrity, BundleRows: rows, Config: []byte(`{}`), ArtifactKey: pkg.artifact, ArtifactSize: int64(len(pkg.resolved.Archive))})
			}
			if lookupErr != nil {
				return lookupErr
			}
			prior.ID = catalog.ID
			prior.BundleRows = catalog.BundleRows
		}
		if hasResolved {
			value.Package = &agentDshPackageOverride{Version: pkg.resolved.Version, Integrity: pkg.resolved.Integrity, SourceKind: string(pkg.source.Kind), SourceSpec: pkg.source.Spec, ArtifactKey: pkg.artifact, BundleRows: pkg.resolved.BundleRows}
		}
		owned := []string{}
		if value.Package != nil {
			owned = value.Package.BundleRows
		} else if json.Unmarshal(prior.BundleRows, &owned) != nil {
			return errors.New("invalid native plugin rows")
		}
		if err = dshprofile.ValidateRowOverrides(plugin.Patches, owned); err != nil {
			return err
		}
		value.Rows = mergeNativePluginRows(value.Rows, plugin.Patches)
		// The ordinary settings editor displays the most recently edited config
		// row; overrides for other rows survive subsequent workbench saves.
		for _, patch := range plugin.Patches {
			var config map[string]any
			if len(patch.Config) > 0 && json.Unmarshal(patch.Config, &config) == nil && config != nil {
				value.RowID, value.Config = patch.ID, config
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		enabled := true
		if exists {
			enabled = prior.Enabled
		}
		for _, patch := range plugin.Patches {
			if patch.Disabled != nil {
				enabled = !*patch.Disabled
			}
		}
		if exists && prior.Enabled == enabled {
			var before AgentDshPluginConfig
			_ = json.Unmarshal(prior.ConfigOverride, &before)
			if reflect.DeepEqual(before, value) {
				continue
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO agent_dsh_plugin(agent_id,dsh_plugin_id,enabled,config_override,config_revision) VALUES($1,$2,$3,$4,nextval('agent_dsh_plugin_config_revision_seq')) ON CONFLICT(agent_id,dsh_plugin_id) DO UPDATE SET enabled=EXCLUDED.enabled,config_override=EXCLUDED.config_override,config_revision=EXCLUDED.config_revision`, agent, prior.ID, enabled, encoded); err != nil {
			return err
		}
	}
	// Only packages present in the native baseline can have been uninstalled
	// there. Preserve disabled legacy bindings that were never in that profile.
	for _, plugin := range baseline.Plugins {
		if !installed[plugin.PackageName] {
			if row, ok := rowsByName[plugin.PackageName]; ok {
				if _, err = q.RemoveAgentDshPlugin(ctx, db.RemoveAgentDshPluginParams{AgentID: agent, DshPluginID: row.ID}); err != nil {
					return err
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE dsh_employee_profile SET native_sync_revision=$3,native_sync_fingerprint=$4,next_apply_at=now(),apply_attempts=0,apply_error='' WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID, revision, snapshot.Fingerprint); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func mergeNativePluginRows(previous, updates []dshprofile.RowOverride) []dshprofile.RowOverride {
	result := append([]dshprofile.RowOverride{}, previous...)
	for _, update := range updates {
		found := false
		for i := range result {
			if result[i].ID == update.ID {
				if len(update.Config) > 0 {
					result[i].Config = update.Config
				}
				if update.Disabled != nil {
					result[i].Disabled = update.Disabled
				}
				found = true
				break
			}
		}
		if !found {
			result = append(result, update)
		}
	}
	return result
}
