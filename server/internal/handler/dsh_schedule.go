package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
)

// taskScheduleActor relies on Auth middleware's overwritten identity headers,
// then cross-checks the workspace context and route. Human JWTs, cloud PATs and
// native browser grants cannot mint standing reminders through this endpoint.
func taskScheduleActor(w http.ResponseWriter, r *http.Request) (service.DSHScheduleActor, bool) {
	var actor service.DSHScheduleActor
	if r.Header.Get("X-Actor-Source") != "task_token" || r.Header.Get("X-Task-ID") != chi.URLParam(r, "taskId") {
		writeError(w, http.StatusForbidden, "DSH schedules require matching task-scoped authentication")
		return actor, false
	}
	workspace := middleware.WorkspaceIDFromContext(r.Context())
	if workspace == "" || workspace != r.Header.Get("X-Workspace-ID") {
		writeError(w, http.StatusForbidden, "task token workspace does not match")
		return actor, false
	}
	wid, ok := parseUUIDOrBadRequest(w, workspace, "workspace_id")
	if !ok {
		return actor, false
	}
	aid, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Agent-ID"), "agent_id")
	if !ok {
		return actor, false
	}
	tid, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task_id")
	if !ok {
		return actor, false
	}
	actor.Key = dshhost.Key{WorkspaceID: uuid.UUID(wid.Bytes), AgentID: uuid.UUID(aid.Bytes)}
	actor.TaskID = uuid.UUID(tid.Bytes)
	return actor, true
}

func writeDSHScheduleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dshhost.ErrNativeAccessDenied):
		writeError(w, http.StatusForbidden, "DSH schedule authority is absent or no longer valid")
	case errors.Is(err, dshschedule.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid DSH schedule")
	case errors.Is(err, dshschedule.ErrConflict):
		writeError(w, http.StatusConflict, "DSH schedule identity already has different content or ownership")
	default:
		writeError(w, http.StatusServiceUnavailable, "DSH schedule persistence is unconfirmed; retry with the same identity")
	}
}

func (h *Handler) DSHSchedules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := taskScheduleActor(w, r)
	if !ok {
		return
	}
	if h.TaskService == nil {
		writeError(w, http.StatusServiceUnavailable, "DSH schedule storage is unavailable")
		return
	}
	if r.Method == http.MethodGet {
		result, err := h.TaskService.ListDSHSchedules(r.Context(), actor, r.URL.Query().Get("session_id"), h.dshNativeInvoke)
		if err != nil {
			writeDSHScheduleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	var input service.DSHScheduleInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid DSH schedule payload")
		return
	}
	if _, ok := parseUUIDOrBadRequest(w, input.SourceTaskID, "source_task_id"); !ok {
		return
	}
	result, err := h.TaskService.RegisterDSHSchedule(r.Context(), actor, input, h.dshNativeInvoke)
	if err != nil {
		writeDSHScheduleError(w, err)
		return
	}
	// The receipt confirms persistence, not a model turn or external delivery.
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) DeleteDSHSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := taskScheduleActor(w, r)
	if !ok {
		return
	}
	if h.TaskService == nil {
		writeError(w, http.StatusServiceUnavailable, "DSH schedule storage is unavailable")
		return
	}
	id := chi.URLParam(r, "scheduleId")
	found, err := h.TaskService.CancelDSHSchedule(r.Context(), actor, r.URL.Query().Get("session_id"), id, h.dshNativeInvoke)
	if err != nil {
		writeDSHScheduleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule_id": id, "deleted": found})
}
