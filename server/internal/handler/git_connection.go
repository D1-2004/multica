package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/gitrepo"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func gitConnectionResponse(row db.GitConnection) map[string]any {
	return map[string]any{"id":uuidToString(row.ID),"provider":row.Provider,"account_login":row.AccountLogin,"created_at":timestampToString(row.CreatedAt)}
}

func (h *Handler) ListGitConnections(w http.ResponseWriter, r *http.Request) {
	workspace, ok := parseUUIDOrBadRequest(w,workspaceIDFromURL(r,"id"),"workspace id")
	if !ok { return }
	rows, err := h.Queries.ListGitConnections(r.Context(),workspace)
	if err != nil { writeError(w,http.StatusInternalServerError,"failed to list Git connections"); return }
	items := make([]map[string]any,0,len(rows))
	for _, row := range rows { items = append(items,gitConnectionResponse(row)) }
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusOK,map[string]any{"connections":items})
}

func (h *Handler) DeleteGitConnection(w http.ResponseWriter, r *http.Request) {
	ws := workspaceIDFromURL(r,"id")
	if _, ok := h.requireWorkspaceRole(w,r,ws,"workspace not found","owner","admin"); !ok { return }
	workspace, ok := parseUUIDOrBadRequest(w,ws,"workspace id")
	if !ok { return }
	id, ok := parseUUIDOrBadRequest(w,chi.URLParam(r,"connectionId"),"connection_id")
	if !ok { return }
	connection, err := h.Queries.GetGitConnection(r.Context(),db.GetGitConnectionParams{ID:id,WorkspaceID:workspace})
	if err != nil { writeError(w,http.StatusNotFound,"Git connection not found"); return }
	if connection.Provider != gitrepo.GitHub { writeError(w,http.StatusNotFound,"Git connection not found"); return }
	err = h.Queries.DeleteGitHubInstallation(r.Context(),db.DeleteGitHubInstallationParams{ID:id,WorkspaceID:workspace})
	if err != nil { writeError(w,http.StatusInternalServerError,"failed to disconnect Git identity"); return }
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ResolveGitRepository(w http.ResponseWriter, r *http.Request) {
	workspace, ok := parseUUIDOrBadRequest(w,workspaceIDFromURL(r,"id"),"workspace id")
	if !ok { return }
	repositoryURL := r.URL.Query().Get("repository")
	if source, normalized, err := detectImportSource(repositoryURL); err == nil && source == sourceSkillsSh {
		owner, repository, _, err := parseSkillsShParts(normalized)
		if err != nil { writeError(w, http.StatusBadRequest, err.Error()); return }
		repositoryURL = "https://github.com/" + owner + "/" + repository
	}
	address, err := gitrepo.ParseAddress(repositoryURL)
	if err != nil { writeError(w,http.StatusBadRequest,err.Error()); return }
	rows, err := h.Queries.ListGitConnections(r.Context(),workspace)
	if err != nil { writeError(w,http.StatusInternalServerError,"failed to list Git connections"); return }
	connections := make([]map[string]any,0)
	for _, row := range rows { if row.Provider == address.Provider { connections=append(connections,gitConnectionResponse(row)) } }
	writeJSON(w,http.StatusOK,map[string]any{"repository_url":address.URL,"provider":address.Provider,"connections":connections})
}
