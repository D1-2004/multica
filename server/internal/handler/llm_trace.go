package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxLLMTraceRelayBytes = 4 << 20

var (
	errLLMTraceExpired     = errors.New("LLM trace capability has expired")
	errLLMTraceUnavailable = errors.New("LLM trace relay is unavailable")
)

type LLMTraceRouter interface {
	SubmitLLMTrace(
		ctx context.Context,
		callbackPath string,
		capability string,
		payload []byte,
	) (int, error)
}

type LLMTraceExternalSink interface {
	SubmitLLMTrace(
		ctx context.Context,
		sinkURL string,
		payload []byte,
	) (int, error)
}

// LLMTraceObserver is a server-side consumer of relayed model
// request/response pairs (the Langfuse exporter). It counts as a delivery
// destination: when it is configured the relay accepts payloads even if
// neither Router telemetry nor the Agent static sink is set up. Its errors
// are logged, never surfaced to the sandbox.
type LLMTraceObserver interface {
	ObserveTaskLLMTrace(
		ctx context.Context,
		task db.AgentTaskQueue,
		agent db.Agent,
		payload []byte,
	) error
}

type httpLLMTraceExternalSink struct {
	client *http.Client
}

func newHTTPTraceExternalSink() *httpLLMTraceExternalSink {
	return &httpLLMTraceExternalSink{client: &http.Client{
		Timeout: 800 * time.Millisecond,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (s *httpLLMTraceExternalSink) SubmitLLMTrace(
	ctx context.Context,
	sinkURL string,
	payload []byte,
) (int, error) {
	parsed, err := url.Parse(strings.TrimSpace(sinkURL))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return 0, errors.New("LLM trace external sink URL is invalid")
	}
	if s == nil || s.client == nil {
		return 0, errLLMTraceUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return 0, errors.New("create LLM trace external sink request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return 0, errors.New("LLM trace external sink request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return response.StatusCode, nil
}

type llmTraceCallback struct {
	TelemetryURL       string `json:"telemetry_url"`
	TelemetryToken     string `json:"telemetry_token"`
	TelemetryExpiresAt int64  `json:"telemetry_expires_at"`
}

func relayTaskLLMTrace(
	ctx context.Context,
	task db.AgentTaskQueue,
	runtimeConfig []byte,
	payload []byte,
	now time.Time,
	router LLMTraceRouter,
	externalSink LLMTraceExternalSink,
	observer LLMTraceObserver,
) (int, error) {
	return relayTaskLLMTraceForAgent(ctx, task, db.Agent{}, runtimeConfig, payload, now, router, externalSink, observer)
}

func relayTaskLLMTraceForAgent(
	ctx context.Context,
	task db.AgentTaskQueue,
	agent db.Agent,
	runtimeConfig []byte,
	payload []byte,
	now time.Time,
	router LLMTraceRouter,
	externalSink LLMTraceExternalSink,
	observer LLMTraceObserver,
) (int, error) {
	observed := false
	if observer != nil {
		// Fan out to the server-side observer first: it only parses and
		// enqueues, so it never delays the network destinations below.
		if err := observer.ObserveTaskLLMTrace(ctx, task, agent, payload); err != nil {
			slog.Warn("LLM trace observer rejected payload",
				"event", "llm_trace_observer_failed",
				"task_id", util.UUIDToString(task.ID),
				"error", err,
			)
		} else {
			observed = true
		}
	}
	var config struct {
		LLMTrace struct {
			Enabled bool   `json:"enabled"`
			SinkURL string `json:"sink_url"`
		} `json:"llm_trace"`
	}
	_ = json.Unmarshal(runtimeConfig, &config)
	var taskContext struct {
		CompletionCallback llmTraceCallback `json:"completion_callback"`
	}
	// A task created without a dispatch context (a web-created Issue) has an
	// empty context; that means "no Router callback", not a broken relay.
	if trimmed := bytes.TrimSpace(task.Context); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if json.Unmarshal(trimmed, &taskContext) != nil {
			if observed {
				return http.StatusNoContent, nil
			}
			return 0, errLLMTraceUnavailable
		}
	}
	callback := taskContext.CompletionCallback
	staticSinkURL := ""
	if config.LLMTrace.Enabled {
		staticSinkURL = strings.TrimSpace(config.LLMTrace.SinkURL)
	}
	hasCallback := strings.TrimSpace(callback.TelemetryURL) != "" ||
		strings.TrimSpace(callback.TelemetryToken) != "" || callback.TelemetryExpiresAt != 0
	if !hasCallback && staticSinkURL == "" {
		if observed {
			return http.StatusNoContent, nil
		}
		return 0, errLLMTraceUnavailable
	}

	type deliveryResult struct {
		status int
		err    error
	}
	results := make(chan deliveryResult, 2)
	pending := 0
	var firstErr error
	if hasCallback {
		if _, ok := routerTelemetryCallbackTaskID(callback.TelemetryURL); !ok ||
			!validTelemetryToken(callback.TelemetryToken) || callback.TelemetryExpiresAt <= 0 {
			firstErr = errLLMTraceUnavailable
		} else if now.UnixMilli() >= callback.TelemetryExpiresAt {
			firstErr = errLLMTraceExpired
		} else if router == nil {
			firstErr = errLLMTraceUnavailable
		} else {
			pending++
			go func() {
				status, err := router.SubmitLLMTrace(
					ctx,
					callback.TelemetryURL,
					callback.TelemetryToken,
					payload,
				)
				results <- deliveryResult{status: status, err: err}
			}()
		}
	}
	if staticSinkURL != "" {
		if externalSink == nil {
			if firstErr == nil {
				firstErr = errLLMTraceUnavailable
			}
		} else {
			pending++
			go func() {
				status, err := externalSink.SubmitLLMTrace(ctx, staticSinkURL, payload)
				results <- deliveryResult{status: status, err: err}
			}()
		}
	}
	failedStatus := 0
	for range pending {
		result := <-results
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		} else if result.err == nil &&
			(result.status < 200 || result.status >= 300) && failedStatus == 0 {
			failedStatus = result.status
		}
	}
	if firstErr != nil {
		return 0, firstErr
	}
	if failedStatus != 0 {
		return failedStatus, nil
	}
	return http.StatusNoContent, nil
}

func (h *Handler) RelayTaskLLMTrace(w http.ResponseWriter, r *http.Request) {
	if middleware.DaemonAuthPathFromContext(r.Context()) != middleware.DaemonAuthPathDaemonToken {
		writeError(w, http.StatusUnauthorized, "daemon token required")
		return
	}
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "runtime not found")
			return
		}
		slog.Warn("load runtime for LLM trace relay failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load runtime")
		return
	}
	if daemonID := middleware.DaemonIDFromContext(r.Context()); daemonID == "" ||
		!runtime.DaemonID.Valid || runtime.DaemonID.String != daemonID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		slog.Warn("load agent for LLM trace relay failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load agent")
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
	status, err := relayTaskLLMTraceForAgent(
		r.Context(),
		task,
		agent,
		agent.RuntimeConfig,
		payload,
		time.Now(),
		h.AgentMessageRouterLLMTrace,
		h.LLMTraceExternalSink,
		h.LLMTraceObserver,
	)
	if err != nil {
		switch {
		case errors.Is(err, errLLMTraceExpired):
			writeError(w, http.StatusGone, "LLM trace capability expired")
		case errors.Is(err, errLLMTraceUnavailable):
			writeError(w, http.StatusServiceUnavailable, "LLM trace relay unavailable")
		default:
			slog.Warn("forward LLM trace from Multica failed", "task_id", taskID, "error", err)
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
