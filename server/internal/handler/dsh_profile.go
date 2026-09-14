package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

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
