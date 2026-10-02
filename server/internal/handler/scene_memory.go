package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service/scenememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// sceneMemoryResponse is the Scene Memory of one Agent work scene. id,
// scene_id and scene_key all carry the scene_id (docs/agent-scene.md);
// conversation_id is the scene's DingTalk openConversationId, for display
// and for reading the scene's relations by conversation.
type sceneMemoryResponse struct {
	ID             string `json:"id"`
	SceneID        string `json:"scene_id"`
	WorkspaceID    string `json:"workspace_id"`
	AgentID        string `json:"agent_id"`
	OrgID          string `json:"org_id"`
	SceneKey       string `json:"scene_key"`
	ConversationID string `json:"conversation_id"`
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

func (h *Handler) sceneMemorySelfNames(ctx context.Context, agent db.Agent) []string {
	names := []string{agent.Name}
	if h == nil || h.Queries == nil {
		return names
	}
	ident, err := h.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
	})
	if err != nil {
		return names
	}
	return append(names, ident.AccountDisplayName)
}

func sceneMemoryToResponse(row scenememory.Memory, selfNames ...string) sceneMemoryResponse {
	resp := sceneMemoryResponse{
		ID:             uuidToString(row.SceneID),
		SceneID:        uuidToString(row.SceneID),
		WorkspaceID:    uuidToString(row.WorkspaceID),
		AgentID:        uuidToString(row.AgentID),
		OrgID:          row.OrgID(),
		SceneKey:       uuidToString(row.SceneID),
		ConversationID: row.ConversationID(),
		SceneKind:      row.Kind(),
		SceneTitle:     scenememory.DisplayTitle(row.Title(), row.MemoryText),
		MemoryText:     scenememory.SanitizeMemoryTextForAgent(row.MemoryText, selfNames...),
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
	if h.handleSelectedSceneMemory(w, r, "list") {
		return
	}
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
	rows, err := h.SceneMemoryStore.List(r.Context(), agent.WorkspaceID, agent.ID, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list scene memory")
		return
	}
	out := make([]sceneMemoryResponse, 0, len(rows))
	names := h.sceneMemorySelfNames(r.Context(), agent)
	for _, row := range rows {
		out = append(out, sceneMemoryToResponse(row, names...))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetAgentSceneMemory returns the memory of one scene by its scene_id:
// GET /api/agents/{id}/scene-memory/{sceneId}. The scene detail (场域 →
// 记忆) opens the scene it lists, which the list above may not contain once
// the agent has more than 200 rows.
func (h *Handler) GetAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	if h.handleSelectedSceneMemory(w, r, "get") {
		return
	}
	agent, row, ok := h.loadManagedSceneMemory(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, sceneMemoryToResponse(row, h.sceneMemorySelfNames(r.Context(), agent)...))
}

func (h *Handler) loadManagedSceneMemory(w http.ResponseWriter, r *http.Request) (db.Agent, scenememory.Memory, bool) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return db.Agent{}, scenememory.Memory{}, false
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	if actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not manage scene memory")
		return db.Agent{}, scenememory.Memory{}, false
	}
	if !h.canManageAgent(w, r, agent) {
		return db.Agent{}, scenememory.Memory{}, false
	}
	sceneID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sceneId"), "scene id")
	if !ok {
		return db.Agent{}, scenememory.Memory{}, false
	}
	if h.SceneMemoryStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scene memory is not configured")
		return db.Agent{}, scenememory.Memory{}, false
	}
	row, err := h.SceneMemoryStore.GetByScene(r.Context(), agent.WorkspaceID, agent.ID, sceneID)
	if err != nil {
		writeError(w, http.StatusNotFound, "scene memory not found")
		return db.Agent{}, scenememory.Memory{}, false
	}
	return agent, row, true
}

func (h *Handler) UpdateAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	h.handleSelectedSceneMemory(w, r, "update")
}
func (h *Handler) ResetAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	h.handleSelectedSceneMemory(w, r, "reset")
}
func (h *Handler) ClearAgentSceneRelations(w http.ResponseWriter, r *http.Request) {
	h.handleSelectedSceneMemory(w, r, "clear")
}
