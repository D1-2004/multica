package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type recordingLLMTraceObserver struct {
	calls   int
	payload []byte
}

func (r *recordingLLMTraceObserver) ObserveTaskLLMTrace(_ context.Context, _ db.AgentTaskQueue, _ db.Agent, _ db.AgentRuntime, payload []byte) error {
	r.calls++
	r.payload = append([]byte(nil), payload...)
	return nil
}

func TestRelayTaskLLMTraceObserverAloneAcceptsPayload(t *testing.T) {
	observer := &recordingLLMTraceObserver{}
	status, err := relayTaskLLMTrace(
		context.Background(),
		db.AgentTaskQueue{Context: []byte(`{}`)},
		[]byte(`{}`),
		[]byte(`{"sequence":1,"request":{"body":"{}"},"response":{"body":"{}"}}`),
		time.UnixMilli(1786377600000),
		nil,
		nil,
		observer,
	)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if observer.calls != 1 {
		t.Fatalf("observer calls = %d", observer.calls)
	}
}

func TestRelayTaskLLMTraceObserverAcceptsTasksWithoutContext(t *testing.T) {
	// A web-created Issue task has no dispatch context at all. Before the
	// observer existed that was a 503 and the runtime retried every pair.
	for _, raw := range [][]byte{nil, []byte(""), []byte("null"), []byte("not json")} {
		observer := &recordingLLMTraceObserver{}
		status, err := relayTaskLLMTrace(
			context.Background(),
			db.AgentTaskQueue{Context: raw},
			[]byte(`{}`),
			[]byte(`{"sequence":1,"request":{"body":"{}"},"response":{"body":"{}"}}`),
			time.UnixMilli(1786377600000),
			nil,
			nil,
			observer,
		)
		if err != nil || status != http.StatusNoContent {
			t.Fatalf("context %q: status=%d err=%v", raw, status, err)
		}
		if observer.calls != 1 {
			t.Fatalf("context %q: observer calls = %d", raw, observer.calls)
		}
	}
	// Without any destination the old contract still applies.
	if _, err := relayTaskLLMTrace(context.Background(), db.AgentTaskQueue{}, []byte(`{}`), []byte(`{}`), time.Now(), nil, nil, nil); err == nil {
		t.Fatal("relay without destinations must report unavailable")
	}
}

func TestRelayTaskLLMTraceObserverRunsAlongsideRouterAndSink(t *testing.T) {
	observer := &recordingLLMTraceObserver{}
	router := &fakeLLMTraceRouter{status: http.StatusCreated}
	sink := &fakeLLMTraceSink{status: http.StatusAccepted}
	task := db.AgentTaskQueue{Context: []byte(`{
		"completion_callback": {
			"telemetry_url": "/api/v1/dispatch-tasks/task-1/llm-traces",
			"telemetry_token": "task-capability",
			"telemetry_expires_at": 1786464000000
		}
	}`)}
	status, err := relayTaskLLMTrace(
		context.Background(),
		task,
		[]byte(`{"llm_trace":{"enabled":true,"sink_url":"https://trace.example.test/ingest"}}`),
		[]byte(`{"sequence":2,"request":{"body":"{}"},"response":{"body":"{}"}}`),
		time.UnixMilli(1786377600000),
		router,
		sink,
		observer,
	)
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if observer.calls != 1 || router.calls != 1 || sink.calls != 1 {
		t.Fatalf("calls observer=%d router=%d sink=%d", observer.calls, router.calls, sink.calls)
	}
}

func TestLangfuseLLMTraceObserverEmitsGenerationUnderTaskRoot(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	client := langfuse.NewWithExporter(langfuse.Config{PublicKey: "pk", SecretKey: "sk", BaseURL: "https://langfuse.example.test"}, exporter)
	t.Cleanup(func() { _ = client.Shutdown(context.Background()) })
	observer := NewLangfuseLLMTraceObserver(client)
	if observer == nil {
		t.Fatal("observer must be constructed for an enabled client")
	}
	if NewLangfuseLLMTraceObserver(nil) != nil {
		t.Fatal("nil client must yield nil observer")
	}

	taskID := pgtype.UUID{Bytes: [16]byte{0xab, 0xcd}, Valid: true}
	task := db.AgentTaskQueue{
		ID:        taskID,
		AgentID:   pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		CreatedAt: pgtype.Timestamptz{Time: time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC), Valid: true},
		Context:   []byte(`{"dispatch_event_data":{"conversation":{"openConversationId":"cid-1"}}}`),
	}
	agent := db.Agent{ID: task.AgentID, Name: "FDE教练", WorkspaceID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}
	runtime := db.AgentRuntime{ID: pgtype.UUID{Bytes: [16]byte{7}, Valid: true}, Name: "FC-Hermes-Stable", RuntimeMode: "cloud", Provider: "hermes"}
	payload := []byte(`{
		"sequence": 3,
		"request": {"body": "{\"model\":\"qwen3.7-plus\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"temperature\":0.2}", "size": 80, "truncated": false},
		"response": {"body": "{\"choices\":[{\"finish_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"hello\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}", "size": 120, "truncated": false, "status": 200, "complete": true}
	}`)
	if err := observer.ObserveTaskLLMTrace(context.Background(), task, agent, runtime, payload); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d (sequence 3 must not emit index events)", len(spans))
	}
	span := spans[0]
	if span.Name != "llm.call.3" {
		t.Fatalf("span name = %s", span.Name)
	}
	if got, want := span.SpanContext.TraceID().String(), "abcd0000000000000000000000000000"; got != want {
		t.Fatalf("trace id = %s, want %s", got, want)
	}
	if got, want := span.Parent.SpanID().String(), service.TaskLangfuseRootSpanID("abcd0000-0000-0000-0000-000000000000"); got != want {
		t.Fatalf("parent = %s, want task root %s", got, want)
	}
	attrs := map[string]string{}
	for _, kv := range span.Attributes {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["langfuse.observation.type"] != "generation" || attrs["langfuse.observation.model.name"] != "qwen3.7-plus" {
		t.Fatalf("attrs = %v", attrs)
	}
	if attrs["langfuse.observation.usage_details"] != `{"input":5,"output":2,"total":7}` {
		t.Fatalf("usage = %s", attrs["langfuse.observation.usage_details"])
	}
	if !strings.Contains(attrs["langfuse.observation.output"], "hello") || !strings.Contains(attrs["langfuse.observation.input"], `"role":"user"`) {
		t.Fatalf("payloads = %v", attrs)
	}
	if attrs["langfuse.session.id"] != "cid-1" || attrs["langfuse.trace.metadata.agent_name"] != "FDE教练" || attrs["langfuse.trace.metadata.task_id"] != "abcd0000-0000-0000-0000-000000000000" {
		t.Fatalf("lookup keys = %v", attrs)
	}
	// The relay usually creates the trace record, so it carries the runtime
	// and provider tags the completion hook would otherwise add too late.
	wantTags := `["agent_task","runtime-cloud","provider-hermes","agent-01000000-0000-0000-0000-000000000000","workspace-09000000-0000-0000-0000-000000000000","task-abcd0000-0000-0000-0000-000000000000"]`
	if attrs["langfuse.trace.tags"] != wantTags || attrs["langfuse.trace.metadata.provider"] != "hermes" {
		t.Fatalf("relay tags/provider = %q / %q", attrs["langfuse.trace.tags"], attrs["langfuse.trace.metadata.provider"])
	}
	if attrs["langfuse.trace.name"] != "agent_task" {
		t.Fatalf("relay trace name = %q, want the task's own name", attrs["langfuse.trace.name"])
	}

	// A task that joined a coordinator turn's trace repeats that turn's name
	// and tags on every relayed generation.
	owned := task
	owned.Context = []byte(`{"coordinator_trace_id":"5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7","coordinator_trace_tags":["inbound_coordinator","source:web","kind:p2p"]}`)
	exporter.Reset()
	if err := observer.ObserveTaskLLMTrace(context.Background(), owned, agent, runtime, payload); err != nil {
		t.Fatal(err)
	}
	ownedAttrs := map[string]string{}
	for _, kv := range exporter.GetSpans()[0].Attributes {
		ownedAttrs[string(kv.Key)] = kv.Value.Emit()
	}
	if ownedAttrs["langfuse.trace.name"] != "inbound_coordinator" || ownedAttrs["langfuse.trace.tags"] != `["inbound_coordinator","source:web","kind:p2p"]` {
		t.Fatalf("coordinator-owned relay name/tags = %q / %q", ownedAttrs["langfuse.trace.name"], ownedAttrs["langfuse.trace.tags"])
	}
	if attrs["langfuse.observation.metadata.sequence"] != "3" || attrs["langfuse.observation.metadata.api"] != "chat.completions" {
		t.Fatalf("observation metadata = %v", attrs)
	}
	if attrs["gen_ai.usage.input_tokens"] != "5" || attrs["gen_ai.usage.output_tokens"] != "2" {
		t.Fatalf("gen_ai usage attributes = %v", attrs)
	}
	if got, want := span.SpanContext.SpanID().String(), LLMTraceObservationID("abcd0000-0000-0000-0000-000000000000", 3); got != want {
		t.Fatalf("observation id = %s, want deterministic %s", got, want)
	}
	// A runtime retry of the same sequence must reuse the observation id so
	// Langfuse upserts instead of duplicating the generation.
	if err := observer.ObserveTaskLLMTrace(context.Background(), task, agent, runtime, payload); err != nil {
		t.Fatal(err)
	}
	retried := exporter.GetSpans()
	if len(retried) != 2 || retried[1].SpanContext.SpanID() != retried[0].SpanContext.SpanID() {
		t.Fatalf("retry produced a different observation id: %v", retried)
	}

	// The first pair also indexes the task ids under the deterministic root.
	exporter.Reset()
	first := []byte(strings.Replace(string(payload), `"sequence": 3`, `"sequence": 1`, 1))
	if err := observer.ObserveTaskLLMTrace(context.Background(), task, agent, runtime, first); err != nil {
		t.Fatal(err)
	}
	indexed := map[string]bool{}
	for _, s := range exporter.GetSpans() {
		if strings.HasPrefix(s.Name, "idx.") {
			indexed[s.Name] = s.Parent.SpanID().String() == service.TaskLangfuseRootSpanID("abcd0000-0000-0000-0000-000000000000")
		}
	}
	if !indexed["idx.task_id.abcd0000-0000-0000-0000-000000000000"] || !indexed["idx.conversation_id.cid-1"] || !indexed["idx.agent_id.01000000-0000-0000-0000-000000000000"] {
		t.Fatalf("index events = %v", indexed)
	}

	if err := observer.ObserveTaskLLMTrace(context.Background(), task, agent, runtime, []byte(`not json`)); err == nil {
		t.Fatal("malformed payload must be reported")
	}
	if err := observer.ObserveTaskLLMTrace(context.Background(), task, agent, runtime, []byte(`{"sequence":4}`)); err == nil {
		t.Fatal("payload without bodies must be reported")
	}

	errPayload := []byte(`{"sequence":5,"request":{"body":"{\"model\":\"m\",\"messages\":[]}"},"response":{"body":"{\"error\":{\"message\":\"rate limited\"}}","status":429,"complete":true}}`)
	if err := observer.ObserveTaskLLMTrace(context.Background(), task, agent, runtime, errPayload); err != nil {
		t.Fatal(err)
	}
	spans = exporter.GetSpans()
	last := spans[len(spans)-1]
	level := ""
	for _, kv := range last.Attributes {
		if string(kv.Key) == "langfuse.observation.level" {
			level = kv.Value.Emit()
		}
	}
	if level != "ERROR" {
		t.Fatalf("error generation level = %q", level)
	}
}
