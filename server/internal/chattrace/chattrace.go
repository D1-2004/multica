package chattrace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ContextKey                 = "chat_trace"
	TraceIDEnvKey              = "MULTICA_TRACE_ID"
	TraceStartedAtUnixMSEnvKey = "MULTICA_TRACE_STARTED_AT_UNIX_MS"
)

// Trace is the durable correlation root for one user-visible chat turn.
// It is stored under task.context.chat_trace so asynchronous launchers,
// retries, FC sandboxes, completion broadcasts, and delivery receipts all
// retain the same identity after the ingress request context has ended.
type Trace struct {
	TraceID         string `json:"trace_id"`
	Channel         string `json:"channel"`
	StartedAtUnixMS int64  `json:"started_at_unix_ms"`
}

func New(channel string) Trace {
	return Trace{
		TraceID:         uuid.NewString(),
		Channel:         strings.TrimSpace(channel),
		StartedAtUnixMS: time.Now().UnixMilli(),
	}
}

func From(traceID, channel string, startedAtUnixMS int64) (Trace, error) {
	trace := Trace{
		TraceID:         strings.TrimSpace(traceID),
		Channel:         strings.TrimSpace(channel),
		StartedAtUnixMS: startedAtUnixMS,
	}
	if err := trace.Validate(); err != nil {
		return Trace{}, err
	}
	return trace, nil
}

func (t Trace) Validate() error {
	if _, err := uuid.Parse(strings.TrimSpace(t.TraceID)); err != nil {
		return fmt.Errorf("chat trace id: %w", err)
	}
	if strings.TrimSpace(t.Channel) == "" {
		return errors.New("chat trace channel is empty")
	}
	if t.StartedAtUnixMS <= 0 {
		return errors.New("chat trace started_at_unix_ms must be positive")
	}
	return nil
}

func Merge(raw []byte, trace Trace) ([]byte, error) {
	if err := trace.Validate(); err != nil {
		return nil, err
	}
	payload := make(map[string]json.RawMessage)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			return nil, fmt.Errorf("decode task context: %w", err)
		}
	}
	encoded, err := json.Marshal(trace)
	if err != nil {
		return nil, fmt.Errorf("encode chat trace: %w", err)
	}
	payload[ContextKey] = encoded
	merged, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode task context: %w", err)
	}
	return merged, nil
}

func Parse(raw []byte) (Trace, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Trace{}, false, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return Trace{}, false, fmt.Errorf("decode task context: %w", err)
	}
	encoded, present := payload[ContextKey]
	if !present {
		return Trace{}, false, nil
	}
	var trace Trace
	if err := json.Unmarshal(encoded, &trace); err != nil {
		return Trace{}, true, fmt.Errorf("decode chat trace: %w", err)
	}
	if err := trace.Validate(); err != nil {
		return Trace{}, true, err
	}
	return trace, true, nil
}

// ForTask returns the persisted chat trace when one exists. Tasks created by
// non-chat entrypoints use their durable task UUID and created_at as the
// canonical trace root, so every daemon claim and FC runner receives trace
// metadata under one explicit task-wide rule.
func ForTask(raw []byte, taskID string, createdAt time.Time) (Trace, error) {
	trace, present, err := Parse(raw)
	if err != nil {
		return Trace{}, err
	}
	if present {
		return trace, nil
	}
	if createdAt.IsZero() {
		return Trace{}, errors.New("task trace created_at is missing")
	}
	return From(taskID, "task", createdAt.UnixMilli())
}

// LogStage emits the common SLS event shape for one chat stage. elapsed_ms is
// the user-observed wall time since ingress; callers may add stage_elapsed_ms
// and resource identifiers through attrs.
func LogStage(logger *slog.Logger, trace Trace, stage, status string, attrs ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := trace.Validate(); err != nil {
		logger.Error("chat trace stage rejected", "event", "chat_trace_invalid", "stage", stage, "error", err)
		return
	}
	elapsedMS := time.Now().UnixMilli() - trace.StartedAtUnixMS
	if elapsedMS < 0 {
		elapsedMS = 0
	}
	base := []any{
		"event", "chat_trace_stage",
		"trace_id", trace.TraceID,
		"channel", trace.Channel,
		"stage", strings.TrimSpace(stage),
		"status", strings.TrimSpace(status),
		"trace_started_at_unix_ms", trace.StartedAtUnixMS,
		"elapsed_ms", elapsedMS,
	}
	logger.Info("chat trace stage", append(base, attrs...)...)
}
