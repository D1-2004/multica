package langfuse

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func testClient(t *testing.T) (*Client, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	client := NewWithExporter(Config{
		PublicKey: "pk", SecretKey: "sk", BaseURL: "https://langfuse.example.test",
		Environment: "Pre Release", Release: "v0.4.40",
	}, exporter)
	t.Cleanup(func() { _ = client.Shutdown(context.Background()) })
	return client, exporter
}

func attrValue(t *testing.T, span tracetest.SpanStub, key string) attribute.Value {
	t.Helper()
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	t.Fatalf("span %q has no attribute %q; attributes=%v", span.Name, key, span.Attributes)
	return attribute.Value{}
}

func hasAttr(span tracetest.SpanStub, key string) bool {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return true
		}
	}
	return false
}

func findSpan(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("no span named %q in %d spans", name, len(spans))
	return tracetest.SpanStub{}
}

func TestNilClientIsInert(t *testing.T) {
	var client *Client
	if client.Enabled() {
		t.Fatal("nil client must report disabled")
	}
	tr := client.StartTrace(context.Background(), TraceOptions{Name: "x"})
	if tr != nil || tr.ID() != "" {
		t.Fatalf("nil client produced a trace: %v", tr)
	}
	obs := tr.StartObservation(ObservationOptions{Name: "child"})
	obs.End(EndOptions{})
	tr.AddMetadata(map[string]any{"k": "v"})
	tr.Event(ObservationOptions{Name: "e"}, EndOptions{})
	tr.End(EndOptions{})
	if got := client.StartObservationInTrace(context.Background(), TraceOptions{}, ObservationOptions{}); got != nil {
		t.Fatal("detached observation on nil client must be nil")
	}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if TraceFromContext(ContextWithTrace(context.Background(), nil)) != nil {
		t.Fatal("nil trace must not be stored in context")
	}
}

func TestStartTracePinsTraceIDAndPropagatesTraceAttributes(t *testing.T) {
	client, exporter := testClient(t)
	const turnID = "11111111-2222-3333-4444-555555555555"
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	tr := client.StartTrace(context.Background(), TraceOptions{
		TraceID:   turnID,
		Name:      "inbound_coordinator",
		Type:      TypeAgent,
		UserID:    "uid-1",
		SessionID: "cid-1",
		Tags:      []string{"coordinator", "", "source:web", "coordinator"},
		Metadata:  map[string]any{"workspace_id": "ws-1", "empty": "", "round": 3, "flag": true},
		Input:     map[string]any{"message": "hi"},
		StartTime: start,
	})
	if got, want := tr.ID(), strings.ReplaceAll(turnID, "-", ""); got != want {
		t.Fatalf("trace id = %s, want %s", got, want)
	}
	gen := tr.StartObservation(ObservationOptions{
		Type: TypeGeneration, Name: "round.1", Model: "qwen3.7-plus",
		ModelParameters: map[string]any{"temperature": 0.3},
		Input:           []string{"a"},
		StartTime:       start.Add(time.Second),
	})
	gen.End(EndOptions{
		Output:  map[string]any{"content": "ok"},
		Usage:   &Usage{Input: 10, Output: 5, CacheRead: 2},
		EndTime: start.Add(2 * time.Second),
	})
	tool := gen.StartObservation(ObservationOptions{Type: TypeTool, Name: "assoc_recall"})
	tool.End(EndOptions{Err: errors.New("boom")})
	tr.AddMetadata(map[string]any{"issue_id": "issue-1"})
	tr.End(EndOptions{Output: map[string]any{"action": "reply"}, EndTime: start.Add(3 * time.Second)})

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("exported %d spans, want 3", len(spans))
	}
	root := findSpan(t, spans, "inbound_coordinator")
	if root.SpanContext.TraceID().String() != tr.ID() {
		t.Fatalf("root trace id = %s, want %s", root.SpanContext.TraceID(), tr.ID())
	}
	if root.Parent.IsValid() {
		t.Fatal("root must not have a parent")
	}
	if got := attrValue(t, root, attrObsType).AsString(); got != "agent" {
		t.Fatalf("root type = %s", got)
	}
	if got := attrValue(t, root, attrTraceName).AsString(); got != "inbound_coordinator" {
		t.Fatalf("trace name = %s", got)
	}
	if got := attrValue(t, root, attrUserID).AsString(); got != "uid-1" {
		t.Fatalf("user id = %s", got)
	}
	if got := attrValue(t, root, attrSessionID).AsString(); got != "cid-1" {
		t.Fatalf("session id = %s", got)
	}
	if got := attrValue(t, root, attrEnvironment).AsString(); got != "pre-release" {
		t.Fatalf("environment = %s", got)
	}
	if got := attrValue(t, root, attrRelease).AsString(); got != "v0.4.40" {
		t.Fatalf("release = %s", got)
	}
	if got := attrValue(t, root, attrTraceTags).AsStringSlice(); strings.Join(got, ",") != "coordinator,source:web" {
		t.Fatalf("tags = %v", got)
	}
	if got := attrValue(t, root, attrTraceMetadataPfx+"workspace_id").AsString(); got != "ws-1" {
		t.Fatalf("workspace metadata = %s", got)
	}
	if hasAttr(root, attrTraceMetadataPfx+"empty") {
		t.Fatal("empty metadata value must be dropped")
	}
	if got := attrValue(t, root, attrTraceMetadataPfx+"round").AsInt64(); got != 3 {
		t.Fatalf("round metadata = %d", got)
	}
	if got := attrValue(t, root, attrTraceMetadataPfx+"issue_id").AsString(); got != "issue-1" {
		t.Fatalf("late metadata = %s", got)
	}
	if got := attrValue(t, root, attrObsInput).AsString(); got != `{"message":"hi"}` {
		t.Fatalf("root input = %s", got)
	}
	if got := attrValue(t, root, attrTraceOutput).AsString(); got != `{"action":"reply"}` {
		t.Fatalf("trace output = %s", got)
	}
	if !root.StartTime.Equal(start) || !root.EndTime.Equal(start.Add(3*time.Second)) {
		t.Fatalf("root timing = %s..%s", root.StartTime, root.EndTime)
	}

	generation := findSpan(t, spans, "round.1")
	if generation.Parent.SpanID() != root.SpanContext.SpanID() {
		t.Fatal("generation must be a child of the root")
	}
	if got := attrValue(t, generation, attrObsType).AsString(); got != "generation" {
		t.Fatalf("generation type = %s", got)
	}
	if got := attrValue(t, generation, attrObsModel).AsString(); got != "qwen3.7-plus" {
		t.Fatalf("model = %s", got)
	}
	if got := attrValue(t, generation, attrObsModelParameters).AsString(); got != `{"temperature":0.3}` {
		t.Fatalf("model parameters = %s", got)
	}
	if got := attrValue(t, generation, attrObsUsageDetails).AsString(); got != `{"cache_read_input_tokens":2,"input":10,"output":5,"total":15}` {
		t.Fatalf("usage = %s", got)
	}
	// The GenAI semantic-convention counters are what older Langfuse builds map.
	if got := attrValue(t, generation, attrGenAIUsageInput).AsInt64(); got != 10 {
		t.Fatalf("gen_ai input tokens = %d", got)
	}
	if got := attrValue(t, generation, attrGenAIUsageOutput).AsInt64(); got != 5 {
		t.Fatalf("gen_ai output tokens = %d", got)
	}
	if got := attrValue(t, generation, attrGenAIUsageTotal).AsInt64(); got != 15 {
		t.Fatalf("gen_ai total tokens = %d", got)
	}
	if got := attrValue(t, generation, attrGenAIUsageCacheRead).AsInt64(); got != 2 {
		t.Fatalf("gen_ai cache read tokens = %d", got)
	}
	// Trace-level attributes ride on every child so Langfuse can filter by them.
	if got := attrValue(t, generation, attrSessionID).AsString(); got != "cid-1" {
		t.Fatalf("child session id = %s", got)
	}
	if got := attrValue(t, generation, attrTraceMetadataPfx+"workspace_id").AsString(); got != "ws-1" {
		t.Fatalf("child workspace metadata = %s", got)
	}
	if hasAttr(generation, attrTraceInput) {
		t.Fatal("children must not carry the trace input")
	}
	// Tags ride on every span: Langfuse freezes a trace's tags on whichever
	// export batch creates the trace, and children usually end first.
	if got := attrValue(t, generation, attrTraceTags).AsStringSlice(); strings.Join(got, ",") != "coordinator,source:web" {
		t.Fatalf("child tags = %v", got)
	}

	toolSpan := findSpan(t, spans, "assoc_recall")
	if toolSpan.Parent.SpanID() != generation.SpanContext.SpanID() {
		t.Fatal("tool must be nested under the generation")
	}
	if got := attrValue(t, toolSpan, attrObsLevel).AsString(); got != "ERROR" {
		t.Fatalf("tool level = %s", got)
	}
	if got := attrValue(t, toolSpan, attrObsStatusMessage).AsString(); got != "boom" {
		t.Fatalf("tool status = %s", got)
	}
}

func TestDeterministicIDsLinkDetachedObservations(t *testing.T) {
	client, exporter := testClient(t)
	const taskID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	rootID := DeterministicSpanID("task:" + taskID)
	if rootID != DeterministicSpanID("task:"+taskID) || len(rootID) != 16 {
		t.Fatalf("deterministic span id = %q", rootID)
	}

	// The relay exports a generation before the task root exists.
	relay := client.StartObservationInTrace(context.Background(), TraceOptions{
		TraceID: taskID, Name: "agent_task", SessionID: "cid-9", Tags: []string{"agent_task"},
		Metadata: map[string]any{"task_id": taskID},
	}, ObservationOptions{
		Type: TypeGeneration, Name: "llm.call.1", ParentSpanID: rootID, Model: "gpt-5.6",
	})
	relay.End(EndOptions{Output: "ok"})

	tr := client.StartTrace(context.Background(), TraceOptions{
		TraceID: taskID, RootSpanID: rootID, Name: "agent_task", Type: TypeAgent,
	})
	child := tr.StartObservation(ObservationOptions{Type: TypeTool, Name: "bash"})
	child.End(EndOptions{})
	tr.End(EndOptions{})

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("exported %d spans, want 3", len(spans))
	}
	wantTrace := strings.ReplaceAll(taskID, "-", "")
	for _, span := range spans {
		if span.SpanContext.TraceID().String() != wantTrace {
			t.Fatalf("span %q trace id = %s, want %s", span.Name, span.SpanContext.TraceID(), wantTrace)
		}
	}
	root := findSpan(t, spans, "agent_task")
	if root.SpanContext.SpanID().String() != rootID {
		t.Fatalf("root span id = %s, want %s", root.SpanContext.SpanID(), rootID)
	}
	relaySpan := findSpan(t, spans, "llm.call.1")
	if relaySpan.Parent.SpanID().String() != rootID {
		t.Fatalf("relay parent = %s, want %s", relaySpan.Parent.SpanID(), rootID)
	}
	if got := attrValue(t, relaySpan, attrSessionID).AsString(); got != "cid-9" {
		t.Fatalf("relay session id = %s", got)
	}
	if got := attrValue(t, relaySpan, attrTraceTags).AsStringSlice(); strings.Join(got, ",") != "agent_task" {
		t.Fatalf("detached observation tags = %v", got)
	}
	toolSpan := findSpan(t, spans, "bash")
	if toolSpan.Parent.SpanID().String() != rootID {
		t.Fatal("child must be parented under the pinned root")
	}
	if toolSpan.SpanContext.SpanID().String() == rootID {
		t.Fatal("child must not reuse the pinned root span id")
	}
}

func TestNoTraceNameKeepsRootObservationName(t *testing.T) {
	client, exporter := testClient(t)
	tr := client.StartTrace(context.Background(), TraceOptions{Name: "agent_task", Type: TypeAgent, NoTraceName: true})
	child := tr.StartObservation(ObservationOptions{Type: TypeTool, Name: "bash"})
	child.End(EndOptions{})
	tr.End(EndOptions{})
	spans := exporter.GetSpans()
	root := findSpan(t, spans, "agent_task")
	if hasAttr(root, attrTraceName) {
		t.Fatal("NoTraceName must keep langfuse.trace.name off the root")
	}
	if got := attrValue(t, root, attrObsType).AsString(); got != "agent" {
		t.Fatalf("root type = %s", got)
	}
	if hasAttr(findSpan(t, spans, "bash"), attrTraceName) {
		t.Fatal("NoTraceName must keep langfuse.trace.name off children")
	}
}

func TestDetachedObservationWithoutParentIsTopLevel(t *testing.T) {
	client, exporter := testClient(t)
	obs := client.StartObservationInTrace(context.Background(), TraceOptions{TraceID: "not-a-uuid"},
		ObservationOptions{Type: TypeEvent, Name: "orphan"})
	obs.End(EndOptions{})
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Parent.IsValid() {
		t.Fatalf("spans = %+v", spans)
	}
	if !spans[0].SpanContext.TraceID().IsValid() {
		t.Fatal("random trace id must be valid")
	}
}

func TestEventIsZeroDuration(t *testing.T) {
	client, exporter := testClient(t)
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	tr := client.StartTrace(context.Background(), TraceOptions{Name: "t"})
	tr.Event(ObservationOptions{Name: "nudge", StartTime: at, Input: "content"}, EndOptions{Level: LevelWarning})
	tr.End(EndOptions{})
	event := findSpan(t, exporter.GetSpans(), "nudge")
	if !event.StartTime.Equal(at) || !event.EndTime.Equal(at) {
		t.Fatalf("event timing = %s..%s", event.StartTime, event.EndTime)
	}
	if got := attrValue(t, event, attrObsType).AsString(); got != "event" {
		t.Fatalf("event type = %s", got)
	}
	if got := attrValue(t, event, attrObsLevel).AsString(); got != "WARNING" {
		t.Fatalf("event level = %s", got)
	}
}

func TestPayloadClipping(t *testing.T) {
	long := strings.Repeat("字", maxPayloadBytes)
	got := encodePayload(long)
	if len(got) > maxPayloadBytes+64 || !strings.Contains(got, "[truncated") {
		t.Fatalf("clipped payload len=%d tail=%q", len(got), got[len(got)-40:])
	}
	if !strings.HasSuffix(strings.TrimSuffix(got[:strings.Index(got, "…")], ""), "字") {
		t.Fatal("clip must land on a rune boundary")
	}
	if encodePayload(nil) != "" || encodePayload("plain") != "plain" {
		t.Fatal("plain strings pass through")
	}
}

// TestExporterPostsToLangfuseOTLPEndpoint runs the real OTLP/HTTP exporter
// against a local server and checks the wire contract Langfuse documents:
// the /api/public/otel/v1/traces path, Basic auth, and a protobuf
// ExportTraceServiceRequest carrying the langfuse.* attributes.
func TestExporterPostsToLangfuseOTLPEndpoint(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []*http.Request
		bodies   [][]byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Encoding") == "gzip" {
			reader, err := gzip.NewReader(strings.NewReader(string(body)))
			if err != nil {
				t.Errorf("gzip: %v", err)
			} else {
				body, _ = io.ReadAll(reader)
			}
		}
		mu.Lock()
		requests = append(requests, r)
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := New(context.Background(), Config{
		PublicKey: "pk-lf-test", SecretKey: "sk-lf-test", BaseURL: server.URL + "/",
		Environment: "pre", Release: "v1", BatchTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !client.Enabled() {
		t.Fatal("client must be enabled")
	}
	if got, want := client.Endpoint(), server.URL+"/api/public/otel/v1/traces"; got != want {
		t.Fatalf("endpoint = %s, want %s", got, want)
	}
	tr := client.StartTrace(context.Background(), TraceOptions{
		TraceID: "0f0f0f0f-0f0f-0f0f-0f0f-0f0f0f0f0f0f", Name: "wire", UserID: "u",
	})
	tr.End(EndOptions{Output: "done"})
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.ForceFlush(flushCtx); err != nil {
		t.Fatal(err)
	}
	if err := client.Shutdown(flushCtx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("exporter sent no request")
	}
	req := requests[0]
	if req.URL.Path != "/api/public/otel/v1/traces" {
		t.Fatalf("path = %s", req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Basic cGstbGYtdGVzdDpzay1sZi10ZXN0" {
		t.Fatalf("authorization = %q", got)
	}
	if got := req.Header.Get("x-langfuse-ingestion-version"); got != "4" {
		t.Fatalf("ingestion version header = %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/x-protobuf" {
		t.Fatalf("content type = %q", got)
	}
	var export collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(bodies[0], &export); err != nil {
		t.Fatalf("decode OTLP body: %v", err)
	}
	if len(export.ResourceSpans) != 1 || len(export.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatalf("unexpected resource layout: %+v", export.ResourceSpans)
	}
	spans := export.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 1 || spans[0].Name != "wire" {
		t.Fatalf("spans = %+v", spans)
	}
	found := map[string]string{}
	for _, kv := range spans[0].Attributes {
		found[kv.Key] = kv.Value.GetStringValue()
	}
	if found[attrTraceName] != "wire" || found[attrUserID] != "u" || found[attrEnvironment] != "pre" || found[attrTraceOutput] != "done" {
		t.Fatalf("attributes = %v", found)
	}
	if got := len(spans[0].TraceId); got != 16 {
		t.Fatalf("trace id bytes = %d", got)
	}
}

func TestNewReturnsNilWhenUnconfigured(t *testing.T) {
	client, err := New(context.Background(), Config{})
	if err != nil || client != nil {
		t.Fatalf("New(empty) = %v, %v", client, err)
	}
	client, err = New(context.Background(), Config{PublicKey: "pk", SecretKey: "sk", BaseURL: "https://x", Disabled: true})
	if err != nil || client != nil {
		t.Fatalf("New(disabled) = %v, %v", client, err)
	}
	if _, err = New(context.Background(), Config{PublicKey: "pk", SecretKey: "sk", BaseURL: "ftp://x"}); err == nil {
		t.Fatal("invalid scheme must fail")
	}
}

var _ sdktrace.SpanExporter = (*tracetest.InMemoryExporter)(nil)
