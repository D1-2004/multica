package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const runtimeStartEventBodyLimit = 16 * 1024

var runtimeStartErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,95}$`)

type runtimeStartEventRequest struct {
	AttemptID string `json:"runtime_start_attempt_id"`
	Protocol  string `json:"startup_status_protocol"`
	Event     string `json:"event"`
	Stage     string `json:"stage"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func (h *Handler) RecordRuntimeStartEvent(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task_id")
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, runtimeStartEventBodyLimit)
	var req runtimeStartEventRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid runtime start event")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "runtime start event must contain one JSON object")
		return
	}
	attemptID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.AttemptID), "runtime_start_attempt_id")
	if !ok {
		return
	}
	if strings.TrimSpace(req.Protocol) != service.RuntimeStartProtocolHTTPJSONV1 {
		writeError(w, http.StatusConflict, "runtime start protocol mismatch")
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil || !task.RuntimeID.Valid || task.RuntimeID != runtime.ID {
		writeError(w, http.StatusNotFound, "runtime start attempt not found")
		return
	}
	attempt, err := h.Queries.GetAgentTaskRuntimeStartAttempt(r.Context(), db.GetAgentTaskRuntimeStartAttemptParams{
		ID:        attemptID,
		TaskID:    taskID,
		RuntimeID: runtime.ID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime start attempt not found")
		return
	}
	if attempt.Protocol != service.RuntimeStartProtocolHTTPJSONV1 {
		writeError(w, http.StatusConflict, "runtime start protocol mismatch")
		return
	}
	if attempt.Status != "starting" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	event := strings.TrimSpace(req.Event)
	stage := strings.TrimSpace(req.Stage)
	var reportedFailure *service.RuntimeStartFailure
	switch event {
	case "runner_started":
		stage = "runner_started"
	case "stage_started":
	case "daemon_started":
		stage = "daemon_started"
	case "stage_failed":
	default:
		writeError(w, http.StatusBadRequest, "unsupported runtime start event")
		return
	}
	if err := service.ValidateRuntimeStartStage(stage); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if event == "stage_failed" {
		errorCode := strings.TrimSpace(req.ErrorCode)
		if errorCode != "" && !runtimeStartErrorCodePattern.MatchString(errorCode) {
			writeError(w, http.StatusBadRequest, "invalid runtime start error code")
			return
		}
		failure, err := service.RuntimeStartFailureForStage(
			service.CloudSandboxBackend(runtime),
			stage,
			errorCode,
			redact.Text(strings.TrimSpace(req.Message)),
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		reportedFailure = &failure
	}
	if _, err := h.TaskService.RecordRuntimeStartStage(r.Context(), attemptID, taskID, runtime.ID, stage); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record runtime start event")
		return
	}

	if reportedFailure != nil {
		if _, err := h.TaskService.FailTaskRuntimeStart(r.Context(), task.ID, runtime.ID, attemptID, *reportedFailure); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to fail runtime start")
			return
		}
	}

	slog.Info("runtime start event accepted",
		"task_id", uuidToString(task.ID),
		"runtime_id", runtimeID,
		"runtime_start_attempt_id", uuidToString(attemptID),
		"backend", service.CloudSandboxBackend(runtime),
		"startup_status_protocol", req.Protocol,
		"event", event,
		"stage", stage,
	)
	w.WriteHeader(http.StatusNoContent)
}
