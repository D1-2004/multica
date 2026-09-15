package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	writeJSON(w,http.StatusOK,map[string]any{"connections":items,"token_connections_available":h.GitRepoSecrets != nil})
}

// The URL identifies the provider. Only this endpoint accepts a credential;
// neither acquisition responses, manifests nor exports contain it.
func (h *Handler) ConnectGitRepository(w http.ResponseWriter, r *http.Request) {
	ws := workspaceIDFromURL(r,"id")
	if _, ok := h.requireWorkspaceRole(w,r,ws,"workspace not found","owner","admin"); !ok { return }
	workspace, ok := parseUUIDOrBadRequest(w,ws,"workspace id")
	if !ok { return }
	var request struct { RepositoryURL string `json:"repository_url"`; Token string `json:"token"`; ConnectionID string `json:"connection_id"` }
	r.Body = http.MaxBytesReader(w,r.Body,16<<10)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil { writeError(w,http.StatusBadRequest,"invalid connection request"); return }
	address, err := gitrepo.ParseAddress(request.RepositoryURL)
	if err != nil { writeError(w,http.StatusBadRequest,err.Error()); return }
	if address.Provider != gitrepo.AlibabaCode { writeError(w,http.StatusBadRequest,"connect this repository through GitHub App authorization"); return }
	if h.GitRepoSecrets == nil { writeError(w,http.StatusServiceUnavailable,"Git credential encryption is not configured"); return }
	client, err := gitrepo.NewCodeClient(h.GitRepoCodeConfig,request.Token)
	if err != nil { writeGitRepoError(w,err); return }
	account, err := client.Account(r.Context())
	if err != nil { writeGitRepoError(w,err); return }
	if _, err := client.Open(r.Context(),address); err != nil { writeGitRepoError(w,err); return }
	sealed, err := h.GitRepoSecrets.Seal([]byte(request.Token))
	request.Token = ""
	if err != nil { writeError(w,http.StatusInternalServerError,"failed to protect Git credential"); return }
	var row db.GitConnection
	if request.ConnectionID != "" {
		id, valid := parseUUIDOrBadRequest(w,request.ConnectionID,"connection_id")
		if !valid { return }
		existing, err := h.Queries.GetGitConnection(r.Context(),db.GetGitConnectionParams{ID:id,WorkspaceID:workspace})
		if err != nil { writeError(w,http.StatusNotFound,"Git connection not found"); return }
		if existing.Provider != gitrepo.AlibabaCode || existing.AccountLogin != account.Username { writeError(w,http.StatusConflict,"replacement token must belong to the same Code identity"); return }
		row, err = h.Queries.UpdateCodeGitConnection(r.Context(),db.UpdateCodeGitConnectionParams{ID:id,WorkspaceID:workspace,AccountLogin:account.Username,TokenCiphertext:sealed})
	} else {
		rows, listErr := h.Queries.ListGitConnections(r.Context(),workspace)
		if listErr != nil { writeError(w,http.StatusInternalServerError,"failed to read Git connections"); return }
		for _, existing := range rows { if existing.Provider == gitrepo.AlibabaCode && existing.AccountLogin == account.Username { writeError(w,http.StatusConflict,"this Code identity is already connected; replace its token instead"); return } }
		row, err = h.Queries.CreateCodeGitConnection(r.Context(),db.CreateCodeGitConnectionParams{WorkspaceID:workspace,AccountLogin:account.Username,TokenCiphertext:sealed,CreatedBy:parseUUID(requestUserID(r))})
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "this Code identity is already connected; replace its token instead")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to save Git connection")
		return
	}
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusOK,gitConnectionResponse(row))
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
	if connection.Provider == gitrepo.GitHub {
		err = h.Queries.DeleteGitHubInstallation(r.Context(),db.DeleteGitHubInstallationParams{ID:id,WorkspaceID:workspace})
	} else {
		err = h.Queries.DeleteCodeGitConnection(r.Context(),db.DeleteCodeGitConnectionParams{ID:id,WorkspaceID:workspace})
	}
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
