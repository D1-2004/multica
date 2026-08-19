package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
)

const maxDaemonA2ATaskControlBody = 16 << 20

// ControlA2ATask is the server-side target for the daemon's task-scoped MCP
// capability. Daemon authentication and workspace ownership are checked before
// the request reaches the A2A binding, and the body never accepts a public task
// ID or client identity supplied by the child process.
func (h *Handler) ControlA2ATask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}
	if !service.IsA2ATaskOrigin(task.Context) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.A2AService == nil {
		writeError(w, http.StatusServiceUnavailable, "A2A task control is unavailable")
		return
	}

	var request service.A2ATaskControlRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDaemonA2ATaskControlBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	response, err := h.A2AService.ControlA2ATask(r.Context(), task.ID, request)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrA2ATaskControlNotFound):
			writeError(w, http.StatusNotFound, "task not found")
		case errors.Is(err, service.ErrA2ATaskControlConflict):
			writeError(w, http.StatusConflict, "A2A task is not accepting this operation")
		case errors.Is(err, service.ErrA2ATaskControlInvalid):
			writeError(w, http.StatusBadRequest, "invalid A2A task control request")
		default:
			slog.Warn("A2A task control failed", "task_id", taskID, "error", err)
			writeError(w, http.StatusInternalServerError, "A2A task control failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, response)
}
