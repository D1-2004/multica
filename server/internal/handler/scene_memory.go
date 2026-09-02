package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type sceneMemoryResponse struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	AgentID        string `json:"agent_id"`
	OrgID          string `json:"org_id"`
	SceneKey       string `json:"scene_key"`
	SceneKind      string `json:"scene_kind"`
	SceneTitle     string `json:"scene_title"`
	MemoryText     string `json:"memory_text"`
	MemoryRevision int64  `json:"memory_revision"`
	Status         string `json:"status"`
	LastError      string `json:"last_error"`
	LastErrorCode  string `json:"last_error_code"`
	UpdatedAt      string `json:"updated_at"`
	BootstrappedAt string `json:"bootstrapped_at,omitempty"`
	LastFlushedAt  string `json:"last_flushed_at,omitempty"`
}

func sceneMemoryToResponse(row db.SceneMemory) sceneMemoryResponse {
	resp := sceneMemoryResponse{
		ID:             uuidToString(row.ID),
		WorkspaceID:    uuidToString(row.WorkspaceID),
		AgentID:        uuidToString(row.AgentID),
		OrgID:          row.OrgID,
		SceneKey:       row.SceneKey,
		SceneKind:      row.SceneKind,
		SceneTitle:     row.SceneTitle,
		MemoryText:     row.MemoryText,
		MemoryRevision: row.MemoryRevision,
		Status:         scenememory.StatusOf(row),
		LastError:      row.LastError,
		LastErrorCode:  row.LastErrorCode,
		UpdatedAt:      timestampToString(row.UpdatedAt),
	}
	if row.BootstrappedAt.Valid {
		resp.BootstrappedAt = timestampToString(row.BootstrappedAt)
	}
	if row.LastFlushedAt.Valid {
		resp.LastFlushedAt = timestampToString(row.LastFlushedAt)
	}
	return resp
}

func (h *Handler) ListAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	if actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not read scene memory")
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if h.SceneMemoryStore == nil {
		writeJSON(w, http.StatusOK, []sceneMemoryResponse{})
		return
	}
	rows, err := h.SceneMemoryStore.List(r.Context(), agent.WorkspaceID, agent.ID, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list scene memory")
		return
	}
	out := make([]sceneMemoryResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, sceneMemoryToResponse(row))
	}
	writeJSON(w, http.StatusOK, out)
}
