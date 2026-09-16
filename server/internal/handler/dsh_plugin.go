package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sort"
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
//
// Every field is a pointer so an omitted field can be told apart from one the
// caller deliberately emptied. Treating them the same would let a rename or a
// version bump silently discard a plugin's configuration, which is where its
// credentials and connection settings live.
type UpdateDshPluginRequest struct {
	DisplayName *string         `json:"display_name"`
	ConfigRow   *string         `json:"config_row"`
	Config      *map[string]any `json:"config"`
	// Source, when set, re-resolves the plugin — this is how an operator
	// moves to a new version.
	Source *string `json:"source"`
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

	// Keep the exact bytes that were validated. A later task then loads them
	// from storage instead of asking a registry again, which is both faster and
	// the only way to guarantee the sandbox runs what was checked here.
	artifactKey, storeErr := h.storeDshPluginArtifact(
		r.Context(), workspaceUUID, resolved.PackageName, resolved.Archive)
	if storeErr != nil || artifactKey == "" {
		writeError(w, http.StatusServiceUnavailable, "the plugin package could not be stored; retry the import")
		return
	}

	h.persistDshPlugin(w, r, dshPluginWrite{
		WorkspaceID:  workspaceUUID,
		CreatorID:    parseUUID(creatorID),
		ArtifactKey:  artifactKey,
		ArtifactSize: int64(len(resolved.Archive)),
		Source:       pinnedSource(source, resolved),
		Resolved:     resolved,
		DisplayName:  strings.TrimSpace(req.DisplayName),
		ConfigRow:    configRow,
		Config:       req.Config,
		Catalog:      strings.TrimSpace(req.Catalog),
		OnConflict:   strings.TrimSpace(req.OnConflict),
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
	// ArtifactKey is where the validated bytes were stored. Empty means the
	// sandbox falls back to fetching the upstream source itself.
	ArtifactKey  string
	ArtifactSize int64
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
		// Replacing an imported plugin changes what already-bound agents run on
		// their next task, and the digest recorded for the new bytes proves
		// integrity but not authorship. A first import stays open to any
		// member; swapping one out does not.
		if in.OnConflict == "overwrite" && !h.dshPluginActorMayReplace(w, r, in.WorkspaceID) {
			return
		}
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
				SourceKind:          string(in.Source.Kind),
				ArtifactKey:         in.ArtifactKey,
				ArtifactSize:        in.ArtifactSize,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to update the DSH plugin")
				return
			}
			// Only now. The row still pointed at prior.ArtifactKey until that
			// update succeeded, and dropping first would have left it pointing
			// at bytes that no longer exist — every task for every agent bound
			// to this plugin failing to fetch it, permanently, until someone
			// re-imported. DeleteDshPlugin already spells out the rule; these
			// two update paths were the ones breaking it.
			h.dropDshPluginArtifact(r.Context(), prior.ArtifactKey, in.ArtifactKey)
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
		ArtifactKey:         in.ArtifactKey,
		ArtifactSize:        in.ArtifactSize,
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
	sourceKind := row.SourceKind
	artifactKey := row.ArtifactKey
	// Set when a new source replaces the stored bytes; dropped only after the
	// row has been updated to stop referring to them.
	supersededKey := ""
	artifactSize := row.ArtifactSize
	version := row.ResolvedVersion
	integrity := row.Integrity
	description := row.Description
	bundleRows := row.BundleRows
	var warnings []string

	availableRows := []string{}
	if len(row.BundleRows) > 0 {
		_ = json.Unmarshal(row.BundleRows, &availableRows)
	}

	// Re-resolve whenever a source is supplied, even an identical one. A
	// github: ref or an https URL can point at different bytes than it did at
	// import, and the recorded digest would then make the sandbox refuse to
	// run — re-resolving in place is the only way to adopt the new content.
	if req.Source != nil && strings.TrimSpace(*req.Source) != "" {
		// Same gate as an overwriting import, because this is the same act:
		// re-resolving replaces the stored bytes, and every agent bound to this
		// plugin runs the new ones on its next claim. Import refuses that to a
		// non-admin (see the OnConflict branch in persistDshPlugin); leaving
		// this route open made that refusal decorative.
		//
		// Only when a source is supplied. Editing a display name or a config is
		// ordinary member work and stays open.
		if !h.dshPluginActorMayReplace(w, r, row.WorkspaceID) {
			return
		}
		source, err := dshplugin.ParseSource(*req.Source)
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
		pinned := pinnedSource(source, resolved)
		if key, err := h.storeDshPluginArtifact(
			r.Context(), row.WorkspaceID, resolved.PackageName, resolved.Archive); err == nil && key != "" {
			// Remembered, not dropped: the row keeps pointing at the old key
			// until the update below commits, and this function can still fail
			// several ways before it gets there.
			supersededKey = artifactKey
			artifactKey = key
			artifactSize = int64(len(resolved.Archive))
		} else {
			writeError(w, http.StatusServiceUnavailable, "the plugin package could not be stored; the existing version was preserved")
			return
		}
		sourceKind = string(source.Kind)
		sourceSpec = pinned.Spec
		version = resolved.Version
		integrity = resolved.Integrity
		description = resolved.Description
		availableRows = resolved.BundleRows
		warnings = resolved.Warnings
		if encoded, err := json.Marshal(resolved.BundleRows); err == nil {
			bundleRows = encoded
		}
	}

	// Config and its row travel together: changing one without the other would
	// address settings at a row they do not belong to.
	configJSON := row.Config
	configRow := row.ConfigRow
	if req.Config != nil || req.ConfigRow != nil {
		nextConfig := map[string]any{}
		if req.Config != nil {
			nextConfig = orEmptyObject(*req.Config)
		} else if len(row.Config) > 0 {
			_ = json.Unmarshal(row.Config, &nextConfig)
		}
		requestedRow := configRow
		if req.ConfigRow != nil {
			requestedRow = *req.ConfigRow
		}
		chosen, err := chooseConfigRow(requestedRow, nextConfig, &dshplugin.Resolved{
			PackageName: row.PackageName,
			BundleRows:  availableRows,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, err := json.Marshal(nextConfig)
		if err != nil {
			writeError(w, http.StatusBadRequest, "configuration could not be encoded")
			return
		}
		configJSON = encoded
		configRow = chosen
	}

	displayName := row.DisplayName
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) != "" {
		displayName = strings.TrimSpace(*req.DisplayName)
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
		SourceKind:          sourceKind,
		ArtifactKey:         artifactKey,
		ArtifactSize:        artifactSize,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the DSH plugin")
		return
	}
	h.dropDshPluginArtifact(r.Context(), supersededKey, artifactKey)
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
	// Lock the parent before deleting children so concurrent imports cannot
	// commit a new binding between child cleanup and parent deletion.
	if _, err := qtx.GetDshPluginInWorkspaceForUpdate(r.Context(), db.GetDshPluginInWorkspaceForUpdateParams{ID: row.ID, WorkspaceID: row.WorkspaceID}); err != nil {
		writeError(w, http.StatusConflict, "plugin changed before deletion")
		return
	}
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
	// After the commit: a failed delete here leaves an orphan object, which is
	// recoverable, whereas deleting before the commit could lose the bytes a
	// still-referenced row points at.
	h.dropDshPluginArtifact(r.Context(), row.ArtifactKey, "")
	w.WriteHeader(http.StatusNoContent)
}

// dropDshPluginArtifact removes a stored package, unless it is the one still in
// use. Keys are content-addressed, so re-importing identical bytes yields the
// same key and must not delete what the new row just claimed.
func (h *Handler) dropDshPluginArtifact(ctx context.Context, key, keep string) {
	if h.Storage == nil || key == "" || key == keep {
		return
	}
	if err := h.Storage.DeleteObject(ctx, key); err != nil {
		slog.Warn("failed to delete a DSH plugin artifact", "key", key, "error", err)
	}
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
type AgentDshPluginConfigChange struct {
	ExpectedRevision *int64          `json:"expected_revision"`
	Override         json.RawMessage `json:"config_override"`
}

type SetAgentDshPluginsRequest struct {
	Plugins []struct {
		ID           string                      `json:"id"`
		Enabled      *bool                       `json:"enabled"`
		ConfigChange *AgentDshPluginConfigChange `json:"config_change,omitempty"`
	} `json:"plugins"`
}

// SetAgentDshPlugins replaces the agent's bindings. Every id is checked against
// the agent's own workspace before anything is written.
func (h *Handler) SetAgentDshPlugins(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	// Which plugins an agent boots with decides what code runs inside it, so
	// this is managing the agent, not reading it — the same gate UpdateAgent
	// uses. loadAgentForUser only proves the caller can SEE the agent, which
	// every workspace member can for a public one.
	if !h.canManageAgent(w, r, agent) {
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

	var configActor pgtype.UUID
	for _, entry := range req.Plugins {
		if entry.ConfigChange != nil {
			_, member, allowed := h.authorizeAgentEnv(w, r)
			if !allowed {
				return
			}
			configActor = member.UserID
			break
		}
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
		id           pgtype.UUID
		enabled      bool
		configChange *AgentDshPluginConfigChange
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
		bindings = append(bindings, binding{id: row.ID, enabled: enabled, configChange: entry.ConfigChange})
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update the agent's DSH plugins")
		return
	}
	defer tx.Rollback(r.Context())
	// Delete-then-insert is only a replacement if nothing interleaves. Without
	// this lock two concurrent replaces can both delete, then both insert, and
	// the agent ends up with the union of two requests — a set neither caller
	// asked for.
	if _, err := tx.Exec(r.Context(),
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"agent_dsh_plugin:"+uuidToString(agent.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock the agent's DSH plugins")
		return
	}
	qtx := h.Queries.WithTx(tx)
	// Lock all destination packages before changing bindings, in a stable
	// order shared with recipe imports. A concurrent delete must finish first.
	sort.Slice(bindings, func(i, j int) bool { return uuidToString(bindings[i].id) < uuidToString(bindings[j].id) })
	for _, entry := range bindings {
		if _, err := qtx.GetDshPluginInWorkspaceForShare(r.Context(), db.GetDshPluginInWorkspaceForShareParams{ID: entry.id, WorkspaceID: agent.WorkspaceID}); err != nil {
			writeError(w, http.StatusConflict, "destination plugin is no longer available")
			return
		}
	}
	// Preserve private overrides when old clients replace only IDs/enabled.
	previous, err := qtx.ListDshPluginsForAgent(r.Context(), db.ListDshPluginsForAgentParams{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if err != nil {
		writeError(w, 500, "failed to read plugin configuration")
		return
	}
	previousByID := map[pgtype.UUID]db.ListDshPluginsForAgentRow{}
	for _, row := range previous {
		previousByID[row.ID] = row
	}

	if err := qtx.DeleteAgentDshPluginsByAgent(r.Context(), agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear the agent's DSH plugins")
		return
	}
	for _, entry := range bindings {
		if err := qtx.AddAgentDshPlugin(r.Context(), db.AddAgentDshPluginParams{
			AgentID:        agent.ID,
			DshPluginID:    entry.id,
			Enabled:        entry.enabled,
			ConfigOverride: previousByID[entry.id].ConfigOverride,
			ConfigRevision: previousByID[entry.id].ConfigRevision,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to attach a DSH plugin")
			return
		}
	}
	// Apply private config edits inside the same transaction as the binding set.
	// The Profile worker only ever observes the final submitted configuration.
	for _, entry := range bindings {
		change := entry.configChange
		if change == nil {
			continue
		}
		previous := previousByID[entry.id]
		if change.ExpectedRevision == nil || *change.ExpectedRevision < 0 || len(change.Override) == 0 {
			writeError(w, 400, "config_change requires expected_revision and config_override")
			return
		}
		if previous.ConfigRevision != *change.ExpectedRevision {
			writeError(w, 409, "plugin configuration changed; reload before submitting")
			return
		}
		plugin := byID[uuidToString(entry.id)]
		previous.PackageName = plugin.PackageName
		previous.BundleRows = plugin.BundleRows
		value, err := decodeAgentDshPluginOverride(change.Override, previous)
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		var encoded []byte
		if value != nil {
			encoded, err = json.Marshal(value)
		}
		if err != nil {
			writeError(w, 400, "invalid plugin configuration")
			return
		}
		expected := previous.ConfigRevision
		if expected == 0 {
			// A newly attached row receives its first revision during insertion.
			if err := tx.QueryRow(r.Context(), `SELECT config_revision FROM agent_dsh_plugin WHERE agent_id=$1 AND dsh_plugin_id=$2`, agent.ID, entry.id).Scan(&expected); err != nil {
				writeError(w, 500, "failed to read new plugin configuration")
				return
			}
		}
		if _, err := qtx.UpdateAgentDshPluginConfig(r.Context(), db.UpdateAgentDshPluginConfigParams{AgentID: agent.ID, PluginID: entry.id, WorkspaceID: agent.WorkspaceID, ConfigOverride: encoded, ExpectedRevision: expected}); err != nil {
			writeError(w, 409, "plugin configuration changed; reload before submitting")
			return
		}
	}
	if configActor.Valid {
		details, _ := json.Marshal(map[string]any{"agent_id": uuidToString(agent.ID), "plugin_count": len(bindings)})
		if _, err := qtx.CreateActivity(r.Context(), db.CreateActivityParams{WorkspaceID: agent.WorkspaceID, ActorType: pgtype.Text{String: "member", Valid: true}, ActorID: configActor, Action: "agent_dsh_plugin_configuration_submitted", Details: details}); err != nil {
			writeError(w, 500, "plugin configuration audit failed")
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
	// Which plugins an agent boots with decides what code runs inside it, so
	// this is managing the agent, not reading it — the same gate UpdateAgent
	// uses. loadAgentForUser only proves the caller can SEE the agent, which
	// every workspace member can for a public one.
	if !h.canManageAgent(w, r, agent) {
		return
	}
	pluginUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "pluginId"), "pluginId")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to detach plugin")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "agent_dsh_plugin:"+uuidToString(agent.ID)); err != nil {
		writeError(w, 500, "failed to lock plugin bindings")
		return
	}
	affected, err := h.Queries.WithTx(tx).RemoveAgentDshPlugin(r.Context(), db.RemoveAgentDshPluginParams{
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
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to detach plugin")
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

// dshPluginActorMayReplace reports whether the caller may overwrite an existing
// plugin, writing the refusal itself when they may not.
func (h *Handler) dshPluginActorMayReplace(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID) bool {
	member, ok := h.requireWorkspaceRole(
		w, r, uuidToString(workspaceID), "workspace not found",
		"owner", "admin", "member",
	)
	if !ok {
		return false
	}
	if !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden,
			"only a workspace owner or admin can replace an imported plugin")
		return false
	}
	return true
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
