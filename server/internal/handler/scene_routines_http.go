package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/scene"
)

// HTTP surface of scene routines (例行任务, scene_routines.go): the mobile
// configure page under /api/context-capabilities/agents/{agentId}/routines and
// the admin Context Builder under
// /api/agents/{id}/tenants/{orgId}/context/scene/{scene_id}/routines. Both
// authorize through contextCapScopeRights (EditRoutines: agent managers of a
// group or 1:1 chat scene); reading needs only access to the scene.

const sceneRoutineBodyLimit = 32 << 10

// writeSceneRoutineError writes a routine refusal with its code, or a 500.
func writeSceneRoutineError(w http.ResponseWriter, r *http.Request, err error, action string) {
	var refusal *sceneRoutineError
	if errors.As(err, &refusal) {
		writeErrorCode(w, refusal.Status, refusal.Code, refusal.Message)
		return
	}
	slog.ErrorContext(r.Context(), "scene routine: "+action+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, "failed to "+action+" routine")
}

func memberRoutineActor(userID string) sceneRoutineActor {
	return sceneRoutineActor{Type: contextcap.RoutineCreatedByMember, UserID: parseUUID(userID)}
}

// sceneRoutineMobileScope authorizes a configure-page caller on the scene
// scope sceneID of agent a (in the requested org) for need.
func (h *Handler) sceneRoutineMobileScope(w http.ResponseWriter, r *http.Request, userID, orgID, sceneID string, need contextCapNeed) (contextCapAgent, bool) {
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return contextCapAgent{}, false
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, orgID); !ok {
		return contextCapAgent{}, false
	}
	if !contextcap.ValidSceneID(sceneID) {
		writeError(w, http.StatusBadRequest, "scene_id is invalid")
		return contextCapAgent{}, false
	}
	if _, ok := h.contextCapRequireScope(w, r, a, userID, contextcap.ScopeScene, sceneID, need); !ok {
		return contextCapAgent{}, false
	}
	return a, true
}

// sceneRoutineMobileRoutine loads the {routineId} routine and authorizes the
// caller on its scene for need.
func (h *Handler) sceneRoutineMobileRoutine(w http.ResponseWriter, r *http.Request, userID, orgID string, need contextCapNeed) (contextCapAgent, contextcap.Routine, bool) {
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return contextCapAgent{}, contextcap.Routine{}, false
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, orgID); !ok {
		return contextCapAgent{}, contextcap.Routine{}, false
	}
	routine, err := h.loadSceneRoutine(r.Context(), a, chi.URLParam(r, "routineId"))
	if err != nil {
		writeSceneRoutineError(w, r, err, "load")
		return contextCapAgent{}, contextcap.Routine{}, false
	}
	if _, ok := h.contextCapRequireScope(w, r, a, userID, contextcap.ScopeScene, routine.SceneID, need); !ok {
		return contextCapAgent{}, contextcap.Routine{}, false
	}
	return a, routine, true
}

// ListContextConfigRoutines lists a scene's routines:
// GET /api/context-capabilities/agents/{agentId}/routines?scene_id=&org_id=
func (h *Handler) ListContextConfigRoutines(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	sceneID := strings.TrimSpace(query.Get("scene_id"))
	a, ok := h.sceneRoutineMobileScope(w, r, userID, query.Get("org_id"), sceneID, contextCapNeedRead)
	if !ok {
		return
	}
	routines, err := h.listSceneRoutines(r.Context(), a, sceneID)
	if err != nil {
		writeSceneRoutineError(w, r, err, "list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routines": routines})
}

// CreateContextConfigRoutine creates a routine in a group or 1:1 chat scene:
// POST /api/context-capabilities/agents/{agentId}/routines
// {scene_id, org_id?, title, instructions, trigger: {kind, cron?, timezone?}}
// → {routine, updated, tell_the_human}. A routine with the same purpose and
// schedule in the scene is updated instead (updated=true).
func (h *Handler) CreateContextConfigRoutine(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	var input struct {
		SceneID string `json:"scene_id"`
		OrgID   string `json:"org_id"`
		sceneRoutineInput
	}
	if !decodeContextCapBody(w, r, sceneRoutineBodyLimit, &input) {
		return
	}
	a, ok := h.sceneRoutineMobileScope(w, r, userID, input.OrgID, input.SceneID, contextCapNeedRoutines)
	if !ok {
		return
	}
	h.createSceneRoutineHTTP(w, r, a, input.SceneID, memberRoutineActor(userID), input.sceneRoutineInput)
}

// createSceneRoutineHTTP creates a routine for a configure page: a 1:1 chat
// scene sends to the counterpart of its newest inbound message.
func (h *Handler) createSceneRoutineHTTP(w http.ResponseWriter, r *http.Request, a contextCapAgent, sceneID string, actor sceneRoutineActor, input sceneRoutineInput) {
	ctx := r.Context()
	sc, err := h.sceneRoutineScene(ctx, a, sceneID)
	if err != nil {
		writeSceneRoutineError(w, r, err, "create")
		return
	}
	var cp sceneRoutineCounterpart
	if sc.SceneKind == scene.KindDM {
		if cp, err = h.sceneRoutineDMCounterpart(ctx, a, sceneID); err != nil {
			writeSceneRoutineError(w, r, err, "create")
			return
		}
	}
	result, err := h.createSceneRoutine(ctx, a, sc, actor, cp, input)
	if err != nil {
		writeSceneRoutineError(w, r, err, "create")
		return
	}
	status := http.StatusCreated
	if result.Updated {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

// UpdateContextConfigRoutine edits a routine:
// PATCH /api/context-capabilities/agents/{agentId}/routines/{routineId}
// {org_id?, title?, instructions?, enabled?, cron?, timezone?} → {routine}.
func (h *Handler) UpdateContextConfigRoutine(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	var input struct {
		OrgID string `json:"org_id"`
		sceneRoutinePatch
	}
	if !decodeContextCapBody(w, r, sceneRoutineBodyLimit, &input) {
		return
	}
	a, routine, ok := h.sceneRoutineMobileRoutine(w, r, userID, input.OrgID, contextCapNeedRoutines)
	if !ok {
		return
	}
	result, err := h.updateSceneRoutine(r.Context(), a, routine, memberRoutineActor(userID), input.sceneRoutinePatch)
	if err != nil {
		writeSceneRoutineError(w, r, err, "update")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteContextConfigRoutine deletes a routine (its run history stays):
// DELETE /api/context-capabilities/agents/{agentId}/routines/{routineId}?org_id=
func (h *Handler) DeleteContextConfigRoutine(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, routine, ok := h.sceneRoutineMobileRoutine(w, r, userID, r.URL.Query().Get("org_id"), contextCapNeedRoutines)
	if !ok {
		return
	}
	if err := h.deleteSceneRoutine(r.Context(), a, routine, memberRoutineActor(userID)); err != nil {
		writeSceneRoutineError(w, r, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunContextConfigRoutine runs a routine now:
// POST /api/context-capabilities/agents/{agentId}/routines/{routineId}/run?org_id=
// → {run}.
func (h *Handler) RunContextConfigRoutine(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	_, routine, ok := h.sceneRoutineMobileRoutine(w, r, userID, r.URL.Query().Get("org_id"), contextCapNeedRoutines)
	if !ok {
		return
	}
	run, err := h.runSceneRoutine(r.Context(), routine, memberRoutineActor(userID))
	if err != nil {
		writeSceneRoutineError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

// RotateContextConfigRoutineWebhook mints a new webhook URL (the old one
// stops working):
// POST /api/context-capabilities/agents/{agentId}/routines/{routineId}/rotate-webhook?org_id=
// → {routine} with trigger.webhook_url shown once.
func (h *Handler) RotateContextConfigRoutineWebhook(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	_, routine, ok := h.sceneRoutineMobileRoutine(w, r, userID, r.URL.Query().Get("org_id"), contextCapNeedRoutines)
	if !ok {
		return
	}
	view, err := h.rotateSceneRoutineWebhook(r.Context(), routine, memberRoutineActor(userID))
	if err != nil {
		writeSceneRoutineError(w, r, err, "rotate")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routine": view})
}

// ── Admin Context Builder ───────────────────────────────────────────────────

// agentRoutineNode resolves the admin scene node of a routines route for
// need; routines exist on scene nodes only.
func (h *Handler) agentRoutineNode(w http.ResponseWriter, r *http.Request, need contextCapNeed) (agentContextNode, bool) {
	if chi.URLParam(r, "scopeType") != contextcap.ScopeScene {
		writeErrorCode(w, http.StatusBadRequest, "scene_kind_without_routines", "only group and 1:1 chat scenes take routines")
		return agentContextNode{}, false
	}
	node, ok := h.agentContextNodeFromRoute(w, r, need)
	if !ok {
		return agentContextNode{}, false
	}
	if node.scope.ScopeType != contextcap.ScopeScene || node.scene == nil {
		writeErrorCode(w, http.StatusBadRequest, "scene_kind_without_routines", "only group and 1:1 chat scenes take routines")
		return agentContextNode{}, false
	}
	return node, true
}

func (h *Handler) agentRoutineFromNode(w http.ResponseWriter, r *http.Request, node agentContextNode) (contextcap.Routine, bool) {
	routine, err := h.loadSceneRoutine(r.Context(), node.agent, chi.URLParam(r, "routineId"))
	if err == nil && routine.SceneID != node.scene.SceneID {
		err = routineRefusal(http.StatusNotFound, "routine_not_found", "routine not found")
	}
	if err != nil {
		writeSceneRoutineError(w, r, err, "load")
		return contextcap.Routine{}, false
	}
	return routine, true
}

// ListAgentContextRoutines: GET .../context/scene/{scene_id}/routines.
func (h *Handler) ListAgentContextRoutines(w http.ResponseWriter, r *http.Request) {
	node, ok := h.agentRoutineNode(w, r, contextCapNeedRead)
	if !ok {
		return
	}
	routines, err := h.listSceneRoutines(r.Context(), node.agent, node.scene.SceneID)
	if err != nil {
		writeSceneRoutineError(w, r, err, "list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routines": routines})
}

// CreateAgentContextRoutine: POST .../context/scene/{scene_id}/routines.
func (h *Handler) CreateAgentContextRoutine(w http.ResponseWriter, r *http.Request) {
	var input sceneRoutineInput
	if !decodeContextCapBody(w, r, sceneRoutineBodyLimit, &input) {
		return
	}
	node, ok := h.agentRoutineNode(w, r, contextCapNeedRoutines)
	if !ok {
		return
	}
	h.createSceneRoutineHTTP(w, r, node.agent, node.scene.SceneID, memberRoutineActor(requestUserID(r)), input)
}

// UpdateAgentContextRoutine: PATCH .../context/scene/{scene_id}/routines/{routineId}.
func (h *Handler) UpdateAgentContextRoutine(w http.ResponseWriter, r *http.Request) {
	var patch sceneRoutinePatch
	if !decodeContextCapBody(w, r, sceneRoutineBodyLimit, &patch) {
		return
	}
	node, ok := h.agentRoutineNode(w, r, contextCapNeedRoutines)
	if !ok {
		return
	}
	routine, ok := h.agentRoutineFromNode(w, r, node)
	if !ok {
		return
	}
	result, err := h.updateSceneRoutine(r.Context(), node.agent, routine, memberRoutineActor(requestUserID(r)), patch)
	if err != nil {
		writeSceneRoutineError(w, r, err, "update")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// DeleteAgentContextRoutine: DELETE .../context/scene/{scene_id}/routines/{routineId}.
func (h *Handler) DeleteAgentContextRoutine(w http.ResponseWriter, r *http.Request) {
	node, ok := h.agentRoutineNode(w, r, contextCapNeedRoutines)
	if !ok {
		return
	}
	routine, ok := h.agentRoutineFromNode(w, r, node)
	if !ok {
		return
	}
	if err := h.deleteSceneRoutine(r.Context(), node.agent, routine, memberRoutineActor(requestUserID(r))); err != nil {
		writeSceneRoutineError(w, r, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunAgentContextRoutine: POST .../context/scene/{scene_id}/routines/{routineId}/run.
func (h *Handler) RunAgentContextRoutine(w http.ResponseWriter, r *http.Request) {
	node, ok := h.agentRoutineNode(w, r, contextCapNeedRoutines)
	if !ok {
		return
	}
	routine, ok := h.agentRoutineFromNode(w, r, node)
	if !ok {
		return
	}
	run, err := h.runSceneRoutine(r.Context(), routine, memberRoutineActor(requestUserID(r)))
	if err != nil {
		writeSceneRoutineError(w, r, err, "run")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

// RotateAgentContextRoutineWebhook: POST .../context/scene/{scene_id}/routines/{routineId}/rotate-webhook.
func (h *Handler) RotateAgentContextRoutineWebhook(w http.ResponseWriter, r *http.Request) {
	node, ok := h.agentRoutineNode(w, r, contextCapNeedRoutines)
	if !ok {
		return
	}
	routine, ok := h.agentRoutineFromNode(w, r, node)
	if !ok {
		return
	}
	view, err := h.rotateSceneRoutineWebhook(r.Context(), routine, memberRoutineActor(requestUserID(r)))
	if err != nil {
		writeSceneRoutineError(w, r, err, "rotate")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routine": view})
}
