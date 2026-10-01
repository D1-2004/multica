package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service/scenememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// sceneMemoryResponse is the Scene Memory of one Agent work scene. id and
// scene_id are both the scene_id (docs/agent-scene.md); scene_key is the
// scene's external conversation id, shown for reference only.
type sceneMemoryResponse struct {
	ID             string `json:"id"`
	SceneID        string `json:"scene_id"`
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
		SceneKey:       row.ConversationID(),
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
	agent, row, ok := h.loadManagedSceneMemory(w, r)
	if !ok {
		return
	}
	var body struct {
		MemoryText       string `json:"memory_text"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	names := h.sceneMemorySelfNames(r.Context(), agent)
	body.MemoryText = scenememory.SanitizeMemoryTextForAgent(body.MemoryText, names...)
	updated, err := h.SceneMemoryStore.ReplaceText(r.Context(), row, body.ExpectedRevision, body.MemoryText)
	if errors.Is(err, scenememory.ErrMemoryText) {
		writeError(w, http.StatusBadRequest, "memory_text exceeds 1600 code points")
		return
	}
	if errors.Is(err, scenememory.ErrStaleRevision) {
		writeError(w, http.StatusConflict, "scene memory revision is stale")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update scene memory")
		return
	}
	slog.Info("scene memory owner replace",
		"event", "scene_memory_owner_replace",
		"scene_id", uuidToString(updated.SceneID),
		"memory_revision", updated.MemoryRevision,
	)
	writeJSON(w, http.StatusOK, sceneMemoryToResponse(updated, names...))
}

func (h *Handler) ResetAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	agent, row, ok := h.loadManagedSceneMemory(w, r)
	if !ok {
		return
	}
	updated, err := h.SceneMemoryStore.Reset(r.Context(), row.Scene, scenememory.DirtyTrigger{
		OccurredAt: time.Now().UTC(),
		EvidenceID: "owner-reset",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset scene memory")
		return
	}
	closedEdges := 0
	unlinkedEvents := 0
	if h.Assoc != nil {
		result, closeErr := h.Assoc.CloseSceneAssociations(
			r.Context(),
			uuidToString(agent.WorkspaceID),
			uuidToString(agent.ID),
			uuidToString(row.SceneID),
		)
		if closeErr != nil {
			slog.Error("scene memory owner reset assoc failed",
				"event", "scene_memory_owner_reset",
				"scene_id", uuidToString(row.SceneID),
				"error", closeErr,
			)
			writeError(w, http.StatusInternalServerError, "failed to clear scene associations")
			return
		}
		closedEdges = result.ClosedEdges
		unlinkedEvents = result.UnlinkedEvents
	}
	slog.Info("scene memory owner reset",
		"event", "scene_memory_reset",
		"scene_id", uuidToString(updated.SceneID),
		"memory_revision", updated.MemoryRevision,
		"closed_edges", closedEdges,
		"unlinked_events", unlinkedEvents,
	)
	writeJSON(w, http.StatusOK, sceneMemoryToResponse(updated, h.sceneMemorySelfNames(r.Context(), agent)...))
}

func (h *Handler) ClearAgentSceneRelations(w http.ResponseWriter, r *http.Request) {
	agent, row, ok := h.loadManagedSceneMemory(w, r)
	if !ok {
		return
	}
	if h.Assoc == nil {
		writeError(w, http.StatusServiceUnavailable, "association store is not configured")
		return
	}
	result, err := h.Assoc.CloseSceneAssociations(
		r.Context(),
		uuidToString(agent.WorkspaceID),
		uuidToString(agent.ID),
		uuidToString(row.SceneID),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear scene associations")
		return
	}
	slog.Info("scene memory owner clear relations",
		"event", "scene_memory_owner_clear_relations",
		"scene_id", uuidToString(row.SceneID),
		"closed_edges", result.ClosedEdges,
		"unlinked_events", result.UnlinkedEvents,
	)
	writeJSON(w, http.StatusOK, map[string]int{
		"closed_edges":    result.ClosedEdges,
		"unlinked_events": result.UnlinkedEvents,
	})
}
