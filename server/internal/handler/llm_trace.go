package handler

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxLLMTraceRelayBytes = 4 << 20

var (
	errLLMTraceUnauthorized = errors.New("LLM trace capability is unauthorized")
	errLLMTraceExpired      = errors.New("LLM trace capability has expired")
	errLLMTraceUnavailable  = errors.New("LLM trace relay is unavailable")
)

type LLMTraceRouter interface {
	SubmitLLMTrace(
		ctx context.Context,
		callbackPath string,
		capability string,
		payload []byte,
	) (int, error)
}

type llmTraceCallback struct {
	TelemetryURL       string `json:"telemetry_url"`
	TelemetryToken     string `json:"telemetry_token"`
	TelemetryExpiresAt int64  `json:"telemetry_expires_at"`
}

func relayTaskLLMTrace(
	ctx context.Context,
	task db.AgentTaskQueue,
	authorization string,
	payload []byte,
	now time.Time,
	router LLMTraceRouter,
) (int, error) {
	if router == nil {
		return 0, errLLMTraceUnavailable
	}
	var taskContext struct {
		CompletionCallback llmTraceCallback `json:"completion_callback"`
	}
	if json.Unmarshal(task.Context, &taskContext) != nil {
		return 0, errLLMTraceUnavailable
	}
	callback := taskContext.CompletionCallback
	if _, ok := routerTelemetryCallbackTaskID(callback.TelemetryURL); !ok ||
		!validTelemetryToken(callback.TelemetryToken) || callback.TelemetryExpiresAt <= 0 {
		return 0, errLLMTraceUnavailable
	}
	capability, ok := llmTraceBearer(authorization)
	if !ok || subtle.ConstantTimeCompare(
		[]byte(capability),
		[]byte(callback.TelemetryToken),
	) != 1 {
		return 0, errLLMTraceUnauthorized
	}
	if now.UnixMilli() >= callback.TelemetryExpiresAt {
		return 0, errLLMTraceExpired
	}
	return router.SubmitLLMTrace(
		ctx,
		callback.TelemetryURL,
		callback.TelemetryToken,
		payload,
	)
}

func llmTraceBearer(authorization string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return "", false
	}
	capability := strings.TrimPrefix(authorization, prefix)
	return capability, validTelemetryToken(capability)
}

func (h *Handler) RelayTaskLLMTrace(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		slog.Warn("load task for LLM trace relay failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLLMTraceRelayBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "LLM trace payload is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	status, err := relayTaskLLMTrace(
		r.Context(),
		task,
		r.Header.Get("Authorization"),
		payload,
		time.Now(),
		h.AgentMessageRouterLLMTrace,
	)
	if err != nil {
		switch {
		case errors.Is(err, errLLMTraceUnauthorized):
			writeError(w, http.StatusUnauthorized, "invalid LLM trace capability")
		case errors.Is(err, errLLMTraceExpired):
			writeError(w, http.StatusGone, "LLM trace capability expired")
		case errors.Is(err, errLLMTraceUnavailable):
			writeError(w, http.StatusServiceUnavailable, "LLM trace relay unavailable")
		default:
			slog.Warn("forward LLM trace to Agent Message Router failed", "task_id", taskID, "error", err)
			writeError(w, http.StatusBadGateway, "LLM trace relay failed")
		}
		return
	}
	if status < 100 || status > 599 {
		writeError(w, http.StatusBadGateway, "LLM trace relay returned an invalid status")
		return
	}
	w.WriteHeader(status)
}
