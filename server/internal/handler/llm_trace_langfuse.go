package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// langfuseLLMTraceObserver turns one relayed sandbox request/response pair
// into a Langfuse generation under the task's trace. The generation is
// parented on the deterministic task root observation, so it is attached to
// the same tree the completion hook finishes later.
type langfuseLLMTraceObserver struct {
	client *langfuse.Client
	now    func() time.Time
}

// LLMTraceObservationID is the stable Langfuse observation id of one relayed
// model call, derived from the task and the runtime's sequence number.
func LLMTraceObservationID(taskID string, sequence int64) string {
	return langfuse.DeterministicSpanID("task:" + strings.TrimSpace(taskID) + ":llm:" + strconv.FormatInt(sequence, 10))
}

// NewLangfuseLLMTraceObserver returns nil when client is nil so the handler
// field stays nil and the relay keeps its previous behavior.
func NewLangfuseLLMTraceObserver(client *langfuse.Client) LLMTraceObserver {
	if client == nil {
		return nil
	}
	return &langfuseLLMTraceObserver{client: client, now: time.Now}
}

// llmTracePairedEvent is the runtime's paired-event contract (docs/llm-trace.md).
type llmTracePairedEvent struct {
	Sequence int64 `json:"sequence"`
	Request  struct {
		Body      string `json:"body"`
		Size      int64  `json:"size"`
		Truncated bool   `json:"truncated"`
	} `json:"request"`
	Response struct {
		Body      string `json:"body"`
		Size      int64  `json:"size"`
		Truncated bool   `json:"truncated"`
		Status    int    `json:"status"`
		Complete  *bool  `json:"complete"`
	} `json:"response"`
	// Optional timing fields a newer runtime may add; absent today.
	StartedAtUnixMS int64 `json:"started_at_unix_ms"`
	DurationMS      int64 `json:"duration_ms"`
}

func (o *langfuseLLMTraceObserver) ObserveTaskLLMTrace(ctx context.Context, task db.AgentTaskQueue, agent db.Agent, payload []byte) error {
	if o == nil || o.client == nil {
		return nil
	}
	var event llmTracePairedEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode paired LLM trace: %w", err)
	}
	if strings.TrimSpace(event.Request.Body) == "" && strings.TrimSpace(event.Response.Body) == "" {
		return errors.New("paired LLM trace carries no bodies")
	}
	exchange := langfuse.ParseExchange([]byte(event.Request.Body), []byte(event.Response.Body))

	var agentPtr *db.Agent
	if agent.ID.Valid {
		agentPtr = &agent
	}
	traceOpts := service.TaskLangfuseTraceOptions(task, agentPtr, nil)

	now := o.now()
	start := now
	end := now
	if event.StartedAtUnixMS > 0 {
		start = time.UnixMilli(event.StartedAtUnixMS)
		if event.DurationMS > 0 {
			end = start.Add(time.Duration(event.DurationMS) * time.Millisecond)
		} else {
			end = start
		}
	} else if event.DurationMS > 0 {
		start = now.Add(-time.Duration(event.DurationMS) * time.Millisecond)
	}
	complete := event.Response.Complete == nil || *event.Response.Complete
	metadata := map[string]any{
		"sequence":           event.Sequence,
		"api":                exchange.API,
		"streamed":           exchange.Streamed,
		"response_status":    int64(event.Response.Status),
		"response_complete":  complete,
		"request_bytes":      event.Request.Size,
		"response_bytes":     event.Response.Size,
		"request_truncated":  event.Request.Truncated,
		"response_truncated": event.Response.Truncated,
		"finish_reason":      exchange.FinishReason,
		"source":             "sandbox_relay",
	}
	taskID := util.UUIDToString(task.ID)
	obs := o.client.StartObservationInTrace(ctx, traceOpts, langfuse.ObservationOptions{
		Type:            langfuse.TypeGeneration,
		Name:            fmt.Sprintf("llm.call.%d", event.Sequence),
		StartTime:       start,
		Model:           exchange.Model,
		ModelParameters: exchange.ModelParameters,
		Input:           exchange.Input,
		Metadata:        metadata,
		ParentSpanID:    service.TaskLangfuseRootSpanID(taskID),
		// The runtime retries a paired event with the same sequence and
		// payload; a deterministic id makes Langfuse upsert instead of
		// storing one generation per attempt.
		SpanID: LLMTraceObservationID(taskID, event.Sequence),
	})
	endOpts := langfuse.EndOptions{EndTime: end, Output: exchange.Output, Usage: exchange.Usage}
	switch {
	case exchange.Error != nil || event.Response.Status >= 400:
		endOpts.Level = langfuse.LevelError
		endOpts.StatusMessage = fmt.Sprintf("upstream status %d", event.Response.Status)
		if exchange.Error != nil {
			if raw, err := json.Marshal(exchange.Error); err == nil {
				endOpts.StatusMessage = string(raw)
			}
		}
	case !complete:
		endOpts.Level = langfuse.LevelWarning
		endOpts.StatusMessage = "response stream interrupted"
	}
	obs.End(endOpts)
	return nil
}
