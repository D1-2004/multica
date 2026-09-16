package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The callback receives transaction-bound queries from the Profile store.
// Reusing Handler.Queries here would escape publication/configuration locks.
func readDSHProfileSource(ctx context.Context, q *db.Queries, key dshhost.Key, template string) (dshprofile.Source, error) {
	source := dshprofile.Source{TemplateID: template, Plugins: []dshprofile.SourcePlugin{}}
	if err := q.LockAgentDshPlugins(ctx, key.AgentID.String()); err != nil {
		return source, err
	}
	rows, err := q.ListDshPluginsForAgent(ctx, db.ListDshPluginsForAgentParams{AgentID: pgtype.UUID{Bytes: key.AgentID, Valid: true}, WorkspaceID: pgtype.UUID{Bytes: key.WorkspaceID, Valid: true}})
	if err != nil {
		return source, err
	}
	return dshProfileSource(template, rows)
}

func dshProfileSource(template string, rows []db.ListDshPluginsForAgentRow) (dshprofile.Source, error) {
	source := dshprofile.Source{TemplateID: template, Plugins: []dshprofile.SourcePlugin{}}
	for _, row := range rows {
		config, err := storedAgentDshPluginConfig(row)
		if row.Enabled {
			config, err = effectiveAgentDshPluginConfig(row)
		}
		if err != nil {
			return source, errors.New("employee plugin configuration requires repair")
		}
		source.Plugins = append(source.Plugins, dshprofile.SourcePlugin{ID: uuid.UUID(row.ID.Bytes), Enabled: row.Enabled, ConfigRevision: row.ConfigRevision, PackageName: row.PackageName, Version: row.ResolvedVersion, Integrity: row.Integrity, SourceKind: row.SourceKind, SourceSpec: row.SourceSpec, ArtifactKey: row.ArtifactKey, RowID: config.RowID, Config: config.Config})
	}
	return source, nil
}

func (h *Handler) GetDSHProfile(w http.ResponseWriter, r *http.Request) {
	h.manageDSHProfile(w, r, false)
}
func (h *Handler) PrepareDSHProfile(w http.ResponseWriter, r *http.Request) {
	h.manageDSHProfile(w, r, true)
}

func (h *Handler) RetryDSHProfileBuild(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Revision string `json:"revision"`
		BuildID  string `json:"build_id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		writeError(w, http.StatusBadRequest, "retry requires a revision and observed build ID")
		return
	}
	revision, err := strconv.ParseInt(input.Revision, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != input.Revision {
		writeError(w, http.StatusBadRequest, "invalid Profile revision")
		return
	}
	buildID, ok := parseUUIDOrBadRequest(w, input.BuildID, "build_id")
	if !ok {
		return
	}
	if uuid.UUID(buildID.Bytes) == uuid.Nil {
		writeError(w, http.StatusBadRequest, "invalid build_id")
		return
	}
	key, ok := h.dshHomeKey(w, r)
	if !ok {
		return
	}
	if h.FCE2BLauncher == nil {
		writeError(w, http.StatusServiceUnavailable, "employee Profile service is unavailable")
		return
	}
	err = h.FCE2BLauncher.RetryDSHEmployeeBuild(r.Context(), key, revision, uuid.UUID(buildID.Bytes))
	if errors.Is(err, dshprofile.ErrChanged) {
		writeError(w, http.StatusConflict, "build or Profile changed, or cleanup is not confirmed; refresh before retrying")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "build retry could not be confirmed; refresh its status")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (h *Handler) manageDSHProfile(w http.ResponseWriter, r *http.Request, prepare bool) {
	w.Header().Set("Cache-Control", "no-store")
	if prepare && r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		var empty struct{}
		err := decoder.Decode(&empty)
		if (err != nil && !errors.Is(err, io.EOF)) || (err == nil && !errors.Is(decoder.Decode(&empty), io.EOF)) {
			writeError(w, http.StatusBadRequest, "employee Profile preparation accepts no client configuration")
			return
		}
	}
	key, ok := h.dshHomeKey(w, r)
	if !ok {
		return
	}
	if h.FCE2BLauncher == nil {
		writeError(w, http.StatusServiceUnavailable, "employee Profile service is unavailable")
		return
	}
	if prepare && h.DB != nil {
		if _, err := h.DB.Exec(r.Context(), `UPDATE dsh_employee_profile SET apply_attempts=0,apply_error='',next_apply_at=now()
 WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "employee Profile retry could not be scheduled")
			return
		}
	}
	status, err := h.FCE2BLauncher.DSHEmployeeProfile(r.Context(), key, prepare)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "employee Profile could not be confirmed; check the runtime and plugin configuration")
		return
	}
	code := http.StatusOK
	if prepare && !status.Current {
		code = http.StatusAccepted
	}
	writeJSON(w, code, status)
}
