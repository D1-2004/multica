package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshplugin"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentDshPluginConfig overrides one loader row for one employee. Empty config
// uses the package's bundle defaults; nil override inherits workspace settings.
type AgentDshPluginConfig struct {
	RowID   string                   `json:"row_id"`
	Config  map[string]any           `json:"config"`
	Rows    []dshprofile.RowOverride `json:"rows,omitempty"`
	Package *agentDshPackageOverride `json:"package,omitempty"`
}

// A native upgrade is pinned to this employee, never to every employee using
// the workspace's imported package. Only the synchronizer writes provenance.
type agentDshPackageOverride struct {
	Version     string   `json:"version"`
	Integrity   string   `json:"integrity"`
	SourceKind  string   `json:"source_kind"`
	SourceSpec  string   `json:"source_spec"`
	ArtifactKey string   `json:"artifact_key"`
	BundleRows  []string `json:"bundle_rows"`
}

type AgentDshPluginConfigResponse struct {
	AgentID   string `json:"agent_id"`
	PluginID  string `json:"plugin_id"`
	Revision  int64  `json:"revision"`
	Inherited bool   `json:"inherited"`
	AgentDshPluginConfig
}

func decodeAgentDshPluginOverride(raw []byte, row db.ListDshPluginsForAgentRow) (*AgentDshPluginConfig, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		previous, err := storedAgentDshPluginConfig(row)
		if err != nil {
			return nil, err
		}
		if previous.Package == nil {
			return nil, nil
		}
		// Reset settings without silently downgrading a native-installed version.
		return &AgentDshPluginConfig{Config: map[string]any{}, Package: previous.Package}, nil
	}
	if len(raw) > 60000 {
		return nil, errors.New("plugin configuration is too large")
	}
	var input struct {
		RowID  string         `json:"row_id"`
		Config map[string]any `json:"config"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil || input.Config == nil {
		return nil, errors.New("plugin override requires row_id and an object config")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid plugin override")
	}
	value := AgentDshPluginConfig{RowID: input.RowID, Config: input.Config}
	// Preserve native settings for other rows and server-owned package identity.
	previous, err := storedAgentDshPluginConfig(row)
	if err != nil {
		return nil, err
	}
	value.Package = previous.Package
	var rows []string
	if err := json.Unmarshal(row.BundleRows, &rows); err != nil {
		return nil, errors.New("invalid stored plugin loader rows")
	}
	if value.Package != nil {
		rows = value.Package.BundleRows
	}
	// Even an empty override must address a declared row if it names one.
	if value.RowID != "" {
		found := false
		for _, id := range rows {
			if id == value.RowID {
				found = true
			}
		}
		if !found {
			return nil, errors.New("plugin configuration names an undeclared loader row")
		}
	}
	id, err := chooseConfigRow(value.RowID, value.Config, &dshplugin.Resolved{PackageName: row.PackageName, BundleRows: rows})
	if err != nil {
		return nil, errors.New("plugin configuration requires one declared loader row")
	}
	value.RowID = id
	for _, patch := range previous.Rows {
		if patch.ID == value.RowID {
			patch.Config = nil
			if patch.Disabled == nil {
				continue
			}
		}
		value.Rows = append(value.Rows, patch)
	}
	return &value, nil
}

// Reading saved settings must remain possible after a plugin update removes a
// loader row, so an owner can repair them using the current revision.
func storedAgentDshPluginConfig(row db.ListDshPluginsForAgentRow) (AgentDshPluginConfig, error) {
	value := AgentDshPluginConfig{RowID: row.ConfigRow, Config: map[string]any{}}
	if len(row.ConfigOverride) > 0 {
		if err := json.Unmarshal(row.ConfigOverride, &value); err != nil || value.Config == nil {
			return AgentDshPluginConfig{}, errors.New("invalid stored employee plugin configuration")
		}
	} else if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &value.Config); err != nil || value.Config == nil {
			return AgentDshPluginConfig{}, errors.New("invalid stored workspace plugin configuration")
		}
	}
	return value, nil
}

func effectiveAgentDshPluginConfig(row db.ListDshPluginsForAgentRow) (AgentDshPluginConfig, error) {
	value, err := storedAgentDshPluginConfig(row)
	if err != nil {
		return AgentDshPluginConfig{}, err
	}
	raw, err := json.Marshal(struct {
		RowID  string         `json:"row_id"`
		Config map[string]any `json:"config"`
	}{value.RowID, value.Config})
	if err != nil {
		return AgentDshPluginConfig{}, err
	}
	checked, err := decodeAgentDshPluginOverride(raw, row)
	if err != nil {
		return AgentDshPluginConfig{}, err
	}
	checked.Rows = value.Rows
	rows := []string{}
	if err := json.Unmarshal(row.BundleRows, &rows); err != nil {
		return AgentDshPluginConfig{}, err
	}
	if value.Package != nil {
		rows = value.Package.BundleRows
	}
	if err := dshprofile.ValidateRowOverrides(value.Rows, rows); err != nil {
		return AgentDshPluginConfig{}, err
	}
	return *checked, nil
}

// manageAgentDshPluginConfig uses the same human owner/admin gate as env access.
// Only this audited endpoint returns employee values; generic plugin lists do not.
func (h *Handler) manageAgentDshPluginConfig(w http.ResponseWriter, r *http.Request, update bool) {
	w.Header().Set("Cache-Control", "no-store")
	agent, member, ok := h.authorizeAgentEnv(w, r)
	if !ok {
		return
	}
	pluginID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "pluginId"), "pluginId")
	if !ok {
		return
	}
	var req struct {
		Override         json.RawMessage `json:"config_override"`
		ExpectedRevision *int64          `json:"expected_revision"`
	}
	if update {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || len(req.Override) == 0 || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 {
			writeError(w, 400, "config_override and expected_revision are required")
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			writeError(w, 400, "invalid request body")
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "failed to access plugin configuration")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "agent_dsh_plugin:"+uuidToString(agent.ID)); err != nil {
		writeError(w, 500, "failed to lock plugin configuration")
		return
	}
	q := h.Queries.WithTx(tx)
	rows, err := q.ListDshPluginsForAgent(r.Context(), db.ListDshPluginsForAgentParams{AgentID: agent.ID, WorkspaceID: agent.WorkspaceID})
	if err != nil {
		writeError(w, 500, "failed to read plugin configuration")
		return
	}
	var selected *db.ListDshPluginsForAgentRow
	for i := range rows {
		if rows[i].ID == pluginID {
			selected = &rows[i]
			break
		}
	}
	if selected == nil {
		writeError(w, 404, "plugin is not attached to this agent")
		return
	}
	row := *selected
	action := "agent_dsh_plugin_config_revealed"
	if update {
		if row.ConfigRevision != *req.ExpectedRevision {
			writeError(w, 409, "plugin configuration changed; reload before saving")
			return
		}
		value, err := decodeAgentDshPluginOverride(req.Override, row)
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		var encoded []byte
		if value != nil {
			encoded, err = json.Marshal(value)
			if err != nil {
				writeError(w, 400, "invalid plugin configuration")
				return
			}
		}
		revision, err := q.UpdateAgentDshPluginConfig(r.Context(), db.UpdateAgentDshPluginConfigParams{AgentID: agent.ID, PluginID: pluginID, WorkspaceID: agent.WorkspaceID, ConfigOverride: encoded, ExpectedRevision: *req.ExpectedRevision})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 409, "plugin configuration changed; reload before saving")
			return
		}
		if err != nil {
			writeError(w, 500, "failed to save plugin configuration")
			return
		}
		row.ConfigOverride = encoded
		row.ConfigRevision = revision
		action = "agent_dsh_plugin_config_updated"
	}
	value, err := storedAgentDshPluginConfig(row)
	if update && err == nil {
		value, err = effectiveAgentDshPluginConfig(row)
	}
	if err != nil {
		writeError(w, 422, "stored plugin configuration no longer matches the plugin; save a valid override")
		return
	}
	details, _ := json.Marshal(map[string]any{"agent_id": uuidToString(agent.ID), "plugin_id": uuidToString(pluginID), "revision": row.ConfigRevision, "inherited": len(row.ConfigOverride) == 0})
	if _, err := q.CreateActivity(r.Context(), db.CreateActivityParams{WorkspaceID: agent.WorkspaceID, ActorType: pgtype.Text{String: "member", Valid: true}, ActorID: member.UserID, Action: action, Details: details}); err != nil {
		writeError(w, 500, "plugin configuration audit failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, 500, "failed to commit plugin configuration access")
		return
	}
	writeJSON(w, 200, AgentDshPluginConfigResponse{AgentID: uuidToString(agent.ID), PluginID: uuidToString(pluginID), Revision: row.ConfigRevision, Inherited: len(row.ConfigOverride) == 0, AgentDshPluginConfig: value})
}

func (h *Handler) GetAgentDshPluginConfig(w http.ResponseWriter, r *http.Request) {
	h.manageAgentDshPluginConfig(w, r, false)
}
func (h *Handler) UpdateAgentDshPluginConfig(w http.ResponseWriter, r *http.Request) {
	h.manageAgentDshPluginConfig(w, r, true)
}

// An explicit workbench toggle controls the whole package, including rows
// previously disabled through the native editor.
func resetNativePluginEnablement(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var value AgentDshPluginConfig
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	rows := value.Rows[:0]
	for _, row := range value.Rows {
		row.Disabled = nil
		if len(row.Config) > 0 {
			rows = append(rows, row)
		}
	}
	value.Rows = rows
	return json.Marshal(value)
}
