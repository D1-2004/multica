package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dshplugin"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DSH plugins as a workspace asset.
//
// DeepSeek Harness has no plugin registry: `dsh plugin --profile <p> add <pkg>`
// forwards to pnpm in the profile directory, and a plugin is an npm package
// declaring `dsh.bundle.patch`. These endpoints therefore store a pinned
// package reference and nothing more, and every field beyond that reference is
// read out of the package itself at import time rather than typed by hand.

// dshPluginRegistryEnv overrides the registry the resolver reads. It defaults
// to the same mirror the runtime adapter uses, so what imports here is what
// installs in a sandbox.
const dshPluginRegistryEnv = "MULTICA_DSH_PLUGIN_REGISTRY"

// maxDshPluginsPerWorkspace bounds one workspace's plugin set. Every enabled
// plugin is fetched and unpacked at task start, so an unbounded set would
// silently become a startup-latency problem.
const maxDshPluginsPerWorkspace = 100

// DshPluginResponse is the API shape of one imported plugin.
type DshPluginResponse struct {
	ID                  string         `json:"id"`
	WorkspaceID         string         `json:"workspace_id"`
	PackageName         string         `json:"package_name"`
	DisplayName         string         `json:"display_name"`
	Description         string         `json:"description"`
	Homepage            string         `json:"homepage"`
	SourceKind          string         `json:"source_kind"`
	SourceSpec          string         `json:"source_spec"`
	ResolvedVersion     string         `json:"resolved_version"`
	Integrity           string         `json:"integrity"`
	BundleRows          []string       `json:"bundle_rows"`
	ConfigRow           string         `json:"config_row"`
	Config              map[string]any `json:"config"`
	Catalog             string         `json:"catalog"`
	ValidatedDshVersion string         `json:"validated_dsh_version"`
	CreatedBy           *string        `json:"created_by"`
	CreatedAt           string         `json:"created_at"`
	UpdatedAt           string         `json:"updated_at"`
}

// ImportDshPluginRequest is the import body. `source` follows npm's own
// vocabulary so an operator can paste a spec straight out of a plugin's README.
type ImportDshPluginRequest struct {
	Source      string         `json:"source"`
	DisplayName string         `json:"display_name"`
	ConfigRow   string         `json:"config_row"`
	Config      map[string]any `json:"config"`
	Catalog     string         `json:"catalog"`
	OnConflict  string         `json:"on_conflict"`
}

// UpdateDshPluginRequest changes only what an operator owns. Identity and
// provenance come from the package and are never client-supplied.
type UpdateDshPluginRequest struct {
	DisplayName string         `json:"display_name"`
	ConfigRow   string         `json:"config_row"`
	Config      map[string]any `json:"config"`
	// Source, when set, re-resolves the plugin — this is how an operator
	// moves to a new version.
	Source string `json:"source"`
}

func dshPluginResolver() *dshplugin.Resolver {
	return dshplugin.NewResolver(strings.TrimSpace(os.Getenv(dshPluginRegistryEnv)))
}

func dshPluginToResponse(row db.DshPlugin) DshPluginResponse {
	resp := DshPluginResponse{
		ID:                  uuidToString(row.ID),
		WorkspaceID:         uuidToString(row.WorkspaceID),
		PackageName:         row.PackageName,
		DisplayName:         row.DisplayName,
		Description:         row.Description,
		Homepage:            row.Homepage,
		SourceKind:          row.SourceKind,
		SourceSpec:          row.SourceSpec,
		ResolvedVersion:     row.ResolvedVersion,
		Integrity:           row.Integrity,
		BundleRows:          []string{},
		ConfigRow:           row.ConfigRow,
		Config:              map[string]any{},
		Catalog:             row.Catalog,
		ValidatedDshVersion: row.ValidatedDshVersion,
		CreatedAt:           timestampToString(row.CreatedAt),
		UpdatedAt:           timestampToString(row.UpdatedAt),
	}
	if len(row.BundleRows) > 0 {
		_ = json.Unmarshal(row.BundleRows, &resp.BundleRows)
	}
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &resp.Config)
	}
	resp.CreatedBy = uuidToPtr(row.CreatedBy)
	return resp
}

// ListDshPlugins returns every plugin imported into the workspace.
func (h *Handler) ListDshPlugins(w http.ResponseWriter, r *http.Request) {
	workspaceUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListDshPluginsByWorkspace(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list DSH plugins")
		return
	}
	resp := make([]DshPluginResponse, len(rows))
	for i, row := range rows {
		resp[i] = dshPluginToResponse(row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetDshPlugin returns one imported plugin.
func (h *Handler) GetDshPlugin(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadDshPlugin(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, dshPluginToResponse(row))
}

func (h *Handler) loadDshPlugin(w http.ResponseWriter, r *http.Request) (db.DshPlugin, bool) {
	workspaceUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return db.DshPlugin{}, false
	}
	pluginUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return db.DshPlugin{}, false
	}
	row, err := h.Queries.GetDshPluginInWorkspace(r.Context(), db.GetDshPluginInWorkspaceParams{
		ID:          pluginUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "DSH plugin not found")
		return db.DshPlugin{}, false
	}
	return row, true
}

// ImportDshPlugin resolves a package reference, validates that the package is
// something DeepSeek Harness can actually load, and records it.
//
// The validation is deliberately the same set of gates the runtime adapter
// applies before booting a profile, so an import that succeeds here will run
// in a task rather than failing where no operator can see it.
func (h *Handler) ImportDshPlugin(w http.ResponseWriter, r *http.Request) {
	workspaceUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req ImportDshPluginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	source, err := dshplugin.ParseSource(req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A file: source would let a caller name any path the server can read.
	// Uploads arrive as a body, never as a server-side path.
	if source.Kind == dshplugin.SourceFile {
		writeError(w, http.StatusBadRequest, "a file: source is only valid inside a sandbox; upload the package instead")
		return
	}

	existing, err := h.Queries.ListDshPluginsByWorkspace(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the workspace plugin set")
		return
	}
	if len(existing) >= maxDshPluginsPerWorkspace {
		writeError(w, http.StatusUnprocessableEntity, "this workspace already has the maximum number of DSH plugins")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	resolved, err := dshPluginResolver().Resolve(ctx, source)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	configRow, err := chooseConfigRow(req.ConfigRow, req.Config, resolved)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.persistDshPlugin(w, r, dshPluginWrite{
		WorkspaceID: workspaceUUID,
		CreatorID:   parseUUID(creatorID),
		Source:      pinnedSource(source, resolved),
		Resolved:    resolved,
		DisplayName: strings.TrimSpace(req.DisplayName),
		ConfigRow:   configRow,
		Config:      req.Config,
		Catalog:     strings.TrimSpace(req.Catalog),
		OnConflict:  strings.TrimSpace(req.OnConflict),
	})
}

// chooseConfigRow decides which loader row an operator's config overrides.
//
// A patch replaces a row's config wholesale, so guessing the wrong row would
// silently wipe a working plugin's settings. When no config is supplied there
// is nothing to address and the answer is "none".
func chooseConfigRow(requested string, config map[string]any, resolved *dshplugin.Resolved) (string, error) {
	requested = strings.TrimSpace(requested)
	if len(config) == 0 {
		return requested, nil
	}
	if len(resolved.BundleRows) == 0 {
		return "", errors.New("this plugin inserts no loader row, so there is nothing to configure")
	}
	if requested != "" {
		for _, row := range resolved.BundleRows {
			if row == requested {
				return requested, nil
			}
		}
		return "", errors.New("this plugin has no row " + requested + "; it declares: " + strings.Join(resolved.BundleRows, ", "))
	}
	for _, row := range resolved.BundleRows {
		if row == resolved.PackageName {
			return row, nil
		}
	}
	if len(resolved.BundleRows) == 1 {
		return resolved.BundleRows[0], nil
	}
	return "", errors.New("this plugin declares several rows (" + strings.Join(resolved.BundleRows, ", ") +
		"); say which one the configuration belongs to")
}

type dshPluginWrite struct {
	WorkspaceID pgtype.UUID
	CreatorID   pgtype.UUID
	Source      dshplugin.Source
	Resolved    *dshplugin.Resolved
	DisplayName string
	ConfigRow   string
	Config      map[string]any
	Catalog     string
	OnConflict  string
}

func (h *Handler) persistDshPlugin(w http.ResponseWriter, r *http.Request, in dshPluginWrite) {
	configJSON, err := json.Marshal(orEmptyObject(in.Config))
	if err != nil {
		writeError(w, http.StatusBadRequest, "configuration could not be encoded")
		return
	}
	rowsJSON, err := json.Marshal(in.Resolved.BundleRows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record the plugin's loader rows")
		return
	}
	displayName := in.DisplayName
	if displayName == "" {
		displayName = in.Resolved.PackageName
	}

	prior, err := h.Queries.GetDshPluginByWorkspaceAndPackage(r.Context(),
		db.GetDshPluginByWorkspaceAndPackageParams{
			WorkspaceID: in.WorkspaceID,
			PackageName: in.Resolved.PackageName,
		})
	if err == nil {
		switch in.OnConflict {
		case "skip":
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "skipped",
				"plugin": dshPluginToResponse(prior),
			})
			return
		case "overwrite":
			updated, err := h.Queries.UpdateDshPlugin(r.Context(), db.UpdateDshPluginParams{
				ID:                  prior.ID,
				WorkspaceID:         in.WorkspaceID,
				DisplayName:         displayName,
				Description:         in.Resolved.Description,
				SourceSpec:          in.Source.Spec,
				ResolvedVersion:     in.Resolved.Version,
				Integrity:           in.Resolved.Integrity,
				BundleRows:          rowsJSON,
				ConfigRow:           in.ConfigRow,
				Config:              configJSON,
				ValidatedDshVersion: prior.ValidatedDshVersion,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update the DSH plugin")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"status":   "updated",
				"plugin":   dshPluginToResponse(updated),
				"warnings": orEmptyStrings(in.Resolved.Warnings),
			})
			return
		default:
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":          "conflict",
				"error":           in.Resolved.PackageName + " is already imported into this workspace",
				"existing_plugin": dshPluginToResponse(prior),
			})
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check for an existing plugin")
		return
	}

	created, err := h.Queries.CreateDshPlugin(r.Context(), db.CreateDshPluginParams{
		WorkspaceID:         in.WorkspaceID,
		PackageName:         in.Resolved.PackageName,
		DisplayName:         displayName,
		Description:         in.Resolved.Description,
		Homepage:            in.Resolved.Homepage,
		SourceKind:          string(in.Source.Kind),
		SourceSpec:          in.Source.Spec,
		ResolvedVersion:     in.Resolved.Version,
		Integrity:           in.Resolved.Integrity,
		BundleRows:          rowsJSON,
		ConfigRow:           in.ConfigRow,
		Config:              configJSON,
		Catalog:             in.Catalog,
		ValidatedDshVersion: "",
		CreatedBy:           in.CreatorID,
	})
	if err != nil {
		slog.Error("create DSH plugin failed", "package", in.Resolved.PackageName, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to import the DSH plugin")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"status":   "created",
		"plugin":   dshPluginToResponse(created),
		"warnings": orEmptyStrings(in.Resolved.Warnings),
	})
}

// UpdateDshPlugin changes an imported plugin's label, configuration, or pinned
// source. Re-resolving on a source change is what makes a version bump safe:
// the new package is validated before it replaces the old pin.
func (h *Handler) UpdateDshPlugin(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadDshPlugin(w, r)
	if !ok {
		return
	}
	var req UpdateDshPluginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	sourceSpec := row.SourceSpec
	version := row.ResolvedVersion
	integrity := row.Integrity
	description := row.Description
	bundleRows := row.BundleRows
	var warnings []string

	availableRows := []string{}
	if len(row.BundleRows) > 0 {
		_ = json.Unmarshal(row.BundleRows, &availableRows)
	}

	if strings.TrimSpace(req.Source) != "" && strings.TrimSpace(req.Source) != row.SourceSpec {
		source, err := dshplugin.ParseSource(req.Source)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if source.Kind == dshplugin.SourceFile {
			writeError(w, http.StatusBadRequest, "a file: source is only valid inside a sandbox; upload the package instead")
			return
		}
		if source.Kind == dshplugin.SourceNPM && source.Name != row.PackageName {
			writeError(w, http.StatusBadRequest, "a new source must point at the same package; import the other package separately")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		resolved, err := dshPluginResolver().Resolve(ctx, source)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if resolved.PackageName != row.PackageName {
			writeError(w, http.StatusBadRequest, "that source resolves to "+resolved.PackageName+", not "+row.PackageName)
			return
		}
		sourceSpec = pinnedSource(source, resolved).Spec
		version = resolved.Version
		integrity = resolved.Integrity
		description = resolved.Description
		availableRows = resolved.BundleRows
		warnings = resolved.Warnings
		if encoded, err := json.Marshal(resolved.BundleRows); err == nil {
			bundleRows = encoded
		}
	}

	configRow, err := chooseConfigRow(req.ConfigRow, req.Config, &dshplugin.Resolved{
		PackageName: row.PackageName,
		BundleRows:  availableRows,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	configJSON, err := json.Marshal(orEmptyObject(req.Config))
	if err != nil {
		writeError(w, http.StatusBadRequest, "configuration could not be encoded")
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = row.DisplayName
	}

	updated, err := h.Queries.UpdateDshPlugin(r.Context(), db.UpdateDshPluginParams{
		ID:                  row.ID,
		WorkspaceID:         row.WorkspaceID,
		DisplayName:         displayName,
		Description:         description,
		SourceSpec:          sourceSpec,
		ResolvedVersion:     version,
		Integrity:           integrity,
		BundleRows:          bundleRows,
		ConfigRow:           configRow,
		Config:              configJSON,
		ValidatedDshVersion: row.ValidatedDshVersion,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the DSH plugin")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugin":   dshPluginToResponse(updated),
		"warnings": orEmptyStrings(warnings),
	})
}

// DeleteDshPlugin removes a plugin and every agent binding to it. This fork
// has no cascading deletes, so both happen in one transaction.
func (h *Handler) DeleteDshPlugin(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadDshPlugin(w, r)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove the DSH plugin")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeleteAgentDshPluginsByPlugin(r.Context(), row.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove the plugin's agent bindings")
		return
	}
	affected, err := qtx.DeleteDshPluginInWorkspace(r.Context(), db.DeleteDshPluginInWorkspaceParams{
		ID:          row.ID,
		WorkspaceID: row.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove the DSH plugin")
		return
	}
	if affected == 0 {
		writeError(w, http.StatusNotFound, "DSH plugin not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove the DSH plugin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DshPluginBindingResponse is one (agent, plugin) pair.
type DshPluginBindingResponse struct {
	AgentID  string `json:"agent_id"`
	PluginID string `json:"dsh_plugin_id"`
	Enabled  bool   `json:"enabled"`
}

// ListDshPluginBindings returns every agent/plugin pair in the workspace so the
// list page can show "used by" without a request per row.
func (h *Handler) ListDshPluginBindings(w http.ResponseWriter, r *http.Request) {
	workspaceUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListDshPluginBindingsByWorkspace(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list DSH plugin bindings")
		return
	}
	resp := make([]DshPluginBindingResponse, len(rows))
	for i, row := range rows {
		resp[i] = DshPluginBindingResponse{
			AgentID:  uuidToString(row.AgentID),
			PluginID: uuidToString(row.DshPluginID),
			Enabled:  row.Enabled,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListAgentDshPlugins returns the plugins bound to one agent.
func (h *Handler) ListAgentDshPlugins(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListDshPluginsForAgent(r.Context(), db.ListDshPluginsForAgentParams{
		AgentID:     agent.ID,
		WorkspaceID: agent.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list the agent's DSH plugins")
		return
	}
	type agentPluginResponse struct {
		DshPluginResponse
		Enabled bool `json:"enabled"`
	}
	resp := make([]agentPluginResponse, len(rows))
	for i, row := range rows {
		resp[i] = agentPluginResponse{
			DshPluginResponse: dshPluginToResponse(db.DshPlugin{
				ID: row.ID, WorkspaceID: row.WorkspaceID, PackageName: row.PackageName,
				DisplayName: row.DisplayName, Description: row.Description, Homepage: row.Homepage,
				SourceKind: row.SourceKind, SourceSpec: row.SourceSpec,
				ResolvedVersion: row.ResolvedVersion, Integrity: row.Integrity,
				BundleRows: row.BundleRows, ConfigRow: row.ConfigRow, Config: row.Config,
				Catalog: row.Catalog, ValidatedDshVersion: row.ValidatedDshVersion,
				CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			}),
			Enabled: row.Enabled,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetAgentDshPluginsRequest replaces an agent's plugin set wholesale.
type SetAgentDshPluginsRequest struct {
	Plugins []struct {
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	} `json:"plugins"`
}

// SetAgentDshPlugins replaces the agent's bindings. Every id is checked against
// the agent's own workspace before anything is written.
func (h *Handler) SetAgentDshPlugins(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req SetAgentDshPluginsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Plugins) > maxDshPluginsPerWorkspace {
		writeError(w, http.StatusUnprocessableEntity, "too many DSH plugins for one agent")
		return
	}

	available, err := h.Queries.ListDshPluginsByWorkspace(r.Context(), agent.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the workspace plugin set")
		return
	}
	byID := map[string]db.DshPlugin{}
	for _, row := range available {
		byID[uuidToString(row.ID)] = row
	}

	type binding struct {
		id      pgtype.UUID
		enabled bool
	}
	bindings := make([]binding, 0, len(req.Plugins))
	seen := map[string]bool{}
	for _, entry := range req.Plugins {
		row, found := byID[entry.ID]
		if !found {
			writeError(w, http.StatusBadRequest, "unknown DSH plugin: "+entry.ID)
			return
		}
		if seen[entry.ID] {
			writeError(w, http.StatusBadRequest, "duplicate DSH plugin: "+entry.ID)
			return
		}
		seen[entry.ID] = true
		enabled := true
		if entry.Enabled != nil {
			enabled = *entry.Enabled
		}
		bindings = append(bindings, binding{id: row.ID, enabled: enabled})
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the agent's DSH plugins")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DeleteAgentDshPluginsByAgent(r.Context(), agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear the agent's DSH plugins")
		return
	}
	for _, entry := range bindings {
		if err := qtx.AddAgentDshPlugin(r.Context(), db.AddAgentDshPluginParams{
			AgentID:     agent.ID,
			DshPluginID: entry.id,
			Enabled:     entry.enabled,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to attach a DSH plugin")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the agent's DSH plugins")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveAgentDshPlugin detaches one plugin from one agent.
func (h *Handler) RemoveAgentDshPlugin(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	pluginUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "pluginId"), "pluginId")
	if !ok {
		return
	}
	affected, err := h.Queries.RemoveAgentDshPlugin(r.Context(), db.RemoveAgentDshPluginParams{
		AgentID:     agent.ID,
		DshPluginID: pluginUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach the DSH plugin")
		return
	}
	if affected == 0 {
		writeError(w, http.StatusNotFound, "that DSH plugin is not attached to this agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pinnedSource rewrites an npm source to name the exact version resolved.
//
// The stored spec is handed to the sandbox verbatim, and the runtime adapter
// refuses a source with no version — so importing `dsh-mcp-lens` or
// `name@latest` would succeed here and then fail at task start. Pinning also
// makes the recorded integrity digest meaningful: it describes the bytes of one
// specific version rather than whatever `latest` happens to be later.
func pinnedSource(source dshplugin.Source, resolved *dshplugin.Resolved) dshplugin.Source {
	if source.Kind != dshplugin.SourceNPM || resolved == nil || resolved.Version == "" {
		return source
	}
	source.Version = resolved.Version
	source.Spec = "npm:" + resolved.PackageName + "@" + resolved.Version
	return source
}

func orEmptyObject(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	return in
}

func orEmptyStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// parseBoundedInt reads a positive query integer with a default and a ceiling.
func parseBoundedInt(raw string, fallback, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}
