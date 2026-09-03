package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
		SceneTitle:     scenememory.DisplayTitle(row.SceneTitle, row.MemoryText),
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

func (h *Handler) loadManagedSceneMemory(w http.ResponseWriter, r *http.Request) (db.Agent, db.SceneMemory, bool) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return db.Agent{}, db.SceneMemory{}, false
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(agent.WorkspaceID))
	if actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not manage scene memory")
		return db.Agent{}, db.SceneMemory{}, false
	}
	if !h.canManageAgent(w, r, agent) {
		return db.Agent{}, db.SceneMemory{}, false
	}
	memoryID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memoryId"), "memory id")
	if !ok {
		return db.Agent{}, db.SceneMemory{}, false
	}
	if h.SceneMemoryStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scene memory is not configured")
		return db.Agent{}, db.SceneMemory{}, false
	}
	row, err := h.SceneMemoryStore.GetByID(r.Context(), agent.WorkspaceID, agent.ID, memoryID)
	if err != nil {
		writeError(w, http.StatusNotFound, "scene memory not found")
		return db.Agent{}, db.SceneMemory{}, false
	}
	return agent, row, true
}

func (h *Handler) UpdateAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	_, row, ok := h.loadManagedSceneMemory(w, r)
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
	updated, err := h.SceneMemoryStore.ReplaceText(
		r.Context(), row.WorkspaceID, row.AgentID, row.ID, body.ExpectedRevision, body.MemoryText,
	)
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
		"scene_key", updated.SceneKey,
		"memory_revision", updated.MemoryRevision,
	)
	writeJSON(w, http.StatusOK, sceneMemoryToResponse(updated))
}

func (h *Handler) ResetAgentSceneMemory(w http.ResponseWriter, r *http.Request) {
	agent, row, ok := h.loadManagedSceneMemory(w, r)
	if !ok {
		return
	}
	identity := scenememory.IdentityFromRow(row)
	updated, err := h.SceneMemoryStore.Reset(r.Context(), identity, scenememory.DirtyTrigger{
		OccurredAt: time.Now().UTC(),
		EvidenceID: "owner-reset",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset scene memory")
		return
	}
	closedEdges := 0
	unlinkedEvents := 0
	if h.Assoc != nil && strings.TrimSpace(row.SceneKey) != "" {
		result, closeErr := h.Assoc.CloseSceneAssociations(
			r.Context(),
			uuidToString(agent.WorkspaceID),
			uuidToString(agent.ID),
			row.SceneKey,
		)
		if closeErr != nil {
			slog.Error("scene memory owner reset assoc failed",
				"event", "scene_memory_owner_reset",
				"scene_key", row.SceneKey,
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
		"scene_key", updated.SceneKey,
		"memory_revision", updated.MemoryRevision,
		"closed_edges", closedEdges,
		"unlinked_events", unlinkedEvents,
	)
	writeJSON(w, http.StatusOK, sceneMemoryToResponse(updated))
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
		row.SceneKey,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear scene associations")
		return
	}
	slog.Info("scene memory owner clear relations",
		"event", "scene_memory_owner_clear_relations",
		"scene_key", row.SceneKey,
		"closed_edges", result.ClosedEdges,
		"unlinked_events", result.UnlinkedEvents,
	)
	writeJSON(w, http.StatusOK, map[string]int{
		"closed_edges":    result.ClosedEdges,
		"unlinked_events": result.UnlinkedEvents,
	})
}
