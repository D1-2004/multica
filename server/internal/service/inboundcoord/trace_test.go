package inboundcoord

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	openai "github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/pkg/llm"
)

func langfuseTestClient(t *testing.T) (*langfuse.Client, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	client := langfuse.NewWithExporter(langfuse.Config{
		PublicKey: "pk", SecretKey: "sk", BaseURL: "https://langfuse.example.test", Environment: "test",
	}, exporter)
	t.Cleanup(func() { _ = client.Shutdown(context.Background()) })
	return client, exporter
}

func spanAttr(span tracetest.SpanStub, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func spansNamed(spans tracetest.SpanStubs, name string) []tracetest.SpanStub {
	var out []tracetest.SpanStub
	for _, span := range spans {
		if span.Name == name {
			out = append(out, span)
		}
	}
	return out
}

func TestDecideExportsOneLangfuseTracePerTurn(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("done", toolFinish, `{"action":"reply","text":"好的，我记下了。","reason":"simple ack"}`),
	}}
	chat.rounds[1].Usage = openai.CompletionUsage{PromptTokens: 200, CompletionTokens: 12, TotalTokens: 212}
	tools := &stubTools{recall: `{"items":[]}`}
	agentID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	userID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	coord := &Coordinator{
		LLM:      llm.New(llm.Config{APIKey: "test-key"}),
		Chat:     chat,
		Tools:    tools,
		Langfuse: client,
	}
	const traceID = "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7"
	decision := coord.Decide(context.Background(), Turn{
		Source:            SourceWeb,
		Addressed:         true,
		ChatType:          "p2p",
		ConversationTitle: "冬翔的会话",
		SenderName:        "冬翔",
		Message:           "记一下：明天三点打球",
		AgentID:           agentID,
		UserID:            userID,
		AgentName:         "FDE教练",
		WorkspaceID:       "ws-1",
		ChatSessionID:     "session-1",
		EvidenceID:        "msg-1",
		TraceID:           traceID,
	})
	if decision.Action != ActionReply {
		t.Fatalf("decision = %#v", decision)
	}
	if decision.TraceID != traceID {
		t.Fatalf("decision trace id = %q, want %q", decision.TraceID, traceID)
	}

	spans := exporter.GetSpans()
	roots := spansNamed(spans, coordinatorTraceName)
	if len(roots) != 1 {
		t.Fatalf("root spans = %d (all: %v)", len(roots), spanNames(spans))
	}
	root := roots[0]
	if got, want := root.SpanContext.TraceID().String(), strings.ReplaceAll(traceID, "-", ""); got != want {
		t.Fatalf("trace id = %s, want %s", got, want)
	}
	wantMetadata := map[string]string{
		"coord_trace_id":  traceID,
		"agent_id":        "01000000-0000-0000-0000-000000000000",
		"user_id":         "02000000-0000-0000-0000-000000000000",
		"agent_name":      "FDE教练",
		"workspace_id":    "ws-1",
		"chat_session_id": "session-1",
		"sender_name":     "冬翔",
		"source":          "web",
		"action":          "reply",
		"model":           coordinatorModel,
	}
	for key, want := range wantMetadata {
		got, ok := spanAttr(root, "langfuse.trace.metadata."+key)
		if !ok || got.AsString() != want {
			t.Errorf("metadata %s = %q (present=%v), want %q", key, got.AsString(), ok, want)
		}
	}
	if got, _ := spanAttr(root, "langfuse.session.id"); got.AsString() != "session-1" {
		t.Errorf("session id = %q", got.AsString())
	}
	if got, _ := spanAttr(root, "langfuse.user.id"); got.AsString() != "02000000-0000-0000-0000-000000000000" {
		t.Errorf("user id = %q", got.AsString())
	}
	wantTags := "inbound_coordinator,source-web,kind-p2p,agent-01000000-0000-0000-0000-000000000000,workspace-ws-1,user-02000000-0000-0000-0000-000000000000"
	if got, _ := spanAttr(root, "langfuse.trace.tags"); strings.Join(got.AsStringSlice(), ",") != wantTags {
		t.Errorf("tags = %v", got.AsStringSlice())
	}
	if got, _ := spanAttr(root, "langfuse.observation.input"); got.AsString() != "记一下：明天三点打球" {
		t.Errorf("root input = %q, want the inbound message", got.AsString())
	}
	if got, _ := spanAttr(root, "langfuse.observation.metadata.conversation_title"); got.AsString() != "冬翔的会话" {
		t.Errorf("root metadata conversation_title = %q", got.AsString())
	}
	// Only ids no trace id, tag or session covers are indexed, under one
	// "index" node.
	if len(spansNamed(spans, "idx.evidence_id.msg-1")) != 1 || len(spansNamed(spans, "index")) != 1 {
		t.Errorf("index node or evidence event missing: %v", spanNames(spans))
	}
	for _, name := range []string{"idx.coord_trace_id." + traceID, "idx.agent_id.01000000-0000-0000-0000-000000000000", "idx.chat_session_id.session-1", "idx.workspace_id.ws-1"} {
		if len(spansNamed(spans, name)) != 0 {
			t.Errorf("%q is covered by the trace id, a tag or the session and must not be indexed", name)
		}
	}
	if got := len(spansNamed(spans, "idx.conversation_id.")); got != 0 {
		t.Errorf("empty conversation id must not be indexed")
	}
	if got, _ := spanAttr(root, "langfuse.trace.output"); !strings.Contains(got.AsString(), `"action":"reply"`) || !strings.Contains(got.AsString(), "好的") {
		t.Errorf("trace output = %s", got.AsString())
	}
	if got, _ := spanAttr(root, "langfuse.observation.level"); got.AsString() == "ERROR" {
		t.Errorf("successful turn must not be ERROR")
	}

	generations := spansNamed(spans, "coordinator.round.1")
	generations = append(generations, spansNamed(spans, "coordinator.round.2")...)
	if len(generations) != 2 {
		t.Fatalf("generation spans = %d (all: %v)", len(generations), spanNames(spans))
	}
	for _, gen := range generations {
		if got, _ := spanAttr(gen, "langfuse.observation.type"); got.AsString() != "generation" {
			t.Errorf("%s type = %s", gen.Name, got.AsString())
		}
		if got, _ := spanAttr(gen, "langfuse.observation.model.name"); got.AsString() != coordinatorModel {
			t.Errorf("%s model = %s", gen.Name, got.AsString())
		}
		if got, _ := spanAttr(gen, "langfuse.trace.metadata.conversation_name"); got.AsString() != "冬翔的会话" {
			t.Errorf("%s lookup key missing on child: %q", gen.Name, got.AsString())
		}
		if got, _ := spanAttr(gen, "langfuse.observation.input"); !strings.Contains(got.AsString(), `"role":"system"`) {
			t.Errorf("%s input = %s", gen.Name, got.AsString())
		}
		if gen.Parent.SpanID() != root.SpanContext.SpanID() {
			t.Errorf("%s must be a child of the root", gen.Name)
		}
	}
	if got, _ := spanAttr(generations[1], "langfuse.observation.usage_details"); got.AsString() != `{"input":200,"output":12,"total":212}` {
		t.Errorf("round 2 usage = %s", got.AsString())
	}
	if got, _ := spanAttr(generations[1], "langfuse.observation.output"); !strings.Contains(got.AsString(), toolFinish) {
		t.Errorf("round 2 output = %s", got.AsString())
	}

	recalls := spansNamed(spans, toolAssocRecall)
	if len(recalls) != 1 {
		t.Fatalf("recall tool spans = %d", len(recalls))
	}
	if got, _ := spanAttr(recalls[0], "langfuse.observation.type"); got.AsString() != "tool" {
		t.Errorf("recall type = %s", got.AsString())
	}
	if got, _ := spanAttr(recalls[0], "langfuse.observation.output"); got.AsString() != `{"items":[]}` {
		t.Errorf("recall output = %s", got.AsString())
	}
	finishes := spansNamed(spans, toolFinish)
	if len(finishes) != 1 {
		t.Fatalf("finish tool spans = %d", len(finishes))
	}
	if got, _ := spanAttr(finishes[0], "langfuse.observation.output"); !strings.Contains(got.AsString(), `"action":"reply"`) {
		t.Errorf("finish output = %s", got.AsString())
	}
}

func TestDecideTraceRecordsLoopFailureAsError(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		{Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "我直接回答"}}}},
	}}
	coord := &Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: chat, Tools: &stubTools{}, Langfuse: client}
	decision := coord.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, ChatType: "p2p", Message: "hi"})
	if decision.Action != ActionContinue {
		t.Fatalf("fail-open decision = %#v", decision)
	}
	spans := exporter.GetSpans()
	roots := spansNamed(spans, coordinatorTraceName)
	if len(roots) != 1 {
		t.Fatalf("root spans = %d", len(roots))
	}
	if got, _ := spanAttr(roots[0], "langfuse.observation.level"); got.AsString() != "ERROR" {
		t.Errorf("root level = %s, want ERROR", got.AsString())
	}
	if got, _ := spanAttr(roots[0], "langfuse.trace.metadata.fail_open"); !got.AsBool() {
		t.Errorf("fail_open metadata missing")
	}
	if got, _ := spanAttr(roots[0], "langfuse.trace.metadata.action"); got.AsString() != "continue" {
		t.Errorf("action metadata = %q, want continue", got.AsString())
	}
	if len(spansNamed(spans, "coordinator.nudge")) != 1 {
		t.Errorf("nudge event missing: %v", spanNames(spans))
	}
	// The second round fails inside the completer, so the error lands on that
	// generation rather than on a separate loop-error event.
	second := spansNamed(spans, "coordinator.round.2")
	if len(second) != 1 {
		t.Fatalf("round 2 spans = %d: %v", len(second), spanNames(spans))
	}
	if got, _ := spanAttr(second[0], "langfuse.observation.level"); got.AsString() != "ERROR" {
		t.Errorf("round 2 level = %s, want ERROR", got.AsString())
	}
	if got, _ := spanAttr(second[0], "langfuse.observation.status_message"); !strings.Contains(got.AsString(), "unexpected extra round") {
		t.Errorf("round 2 status = %s", got.AsString())
	}
}

func TestDecideTraceRecordsExhaustedRoundsAsLoopError(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	rounds := make([]openai.ChatCompletion, 0, maxLoopRounds)
	for range maxLoopRounds {
		rounds = append(rounds, assistantTool("recall", toolAssocRecall, `{"since":"48h"}`))
	}
	coord := &Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: &scriptedCompleter{rounds: rounds}, Tools: &stubTools{}, Langfuse: client}
	decision := coord.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, ChatType: "p2p", Message: "hi"})
	if decision.Action != ActionContinue {
		t.Fatalf("decision = %#v", decision)
	}
	spans := exporter.GetSpans()
	if len(spansNamed(spans, "coordinator.loop_error")) != 1 {
		t.Errorf("loop error event missing: %v", spanNames(spans))
	}
	if got := len(spansNamed(spans, toolAssocRecall)); got != maxLoopRounds {
		t.Errorf("recall tool spans = %d, want one per round", got)
	}
}

func TestDecideTakesTraceIDFromContextWhenTurnHasNone(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("done", toolFinish, `{"action":"reply","text":"好的"}`),
	}}
	coord := &Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: chat, Tools: &stubTools{}, Langfuse: client}
	const jobID = "0f0f0f0f-1111-4222-8333-444444444444"
	ctx := ContextWithTraceID(context.Background(), jobID)
	decision := coord.Decide(ctx, Turn{Source: SourceWeb, Addressed: true, ChatType: "p2p", Message: "hi"})
	if decision.TraceID != jobID {
		t.Fatalf("decision trace id = %q, want the context id %q", decision.TraceID, jobID)
	}
	roots := spansNamed(exporter.GetSpans(), coordinatorTraceName)
	if len(roots) != 1 || roots[0].SpanContext.TraceID().String() != strings.ReplaceAll(jobID, "-", "") {
		t.Fatalf("root spans = %d, trace id mismatch", len(roots))
	}
	// An explicit turn id still wins over the context.
	explicit := coord.Decide(ContextWithTraceID(context.Background(), jobID), Turn{Source: SourceWeb, Addressed: true, ChatType: "p2p", Message: "hi", TraceID: "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7"})
	if explicit.TraceID != "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7" {
		t.Fatalf("explicit trace id overridden: %q", explicit.TraceID)
	}
}

func TestConversationNameFallsBackToSceneTitleBeforeSender(t *testing.T) {
	if got := conversationName(Turn{SceneTitle: "冬翔", SenderName: "Dv6WPxM5cBX83sOS9u0PAAwiEiE"}); got != "冬翔" {
		t.Fatalf("conversation name = %q, want the scene title", got)
	}
	if got := conversationName(Turn{ConversationTitle: "项目群", SceneTitle: "冬翔"}); got != "项目群" {
		t.Fatalf("conversation name = %q, want the channel title", got)
	}
	if got := conversationName(Turn{SenderName: "uid-1"}); got != "uid-1" {
		t.Fatalf("conversation name = %q, want the sender", got)
	}
}

func TestRunLoopWithoutTraceIsUnchanged(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("done", toolFinish, `{"action":"reply","text":"ok"}`),
	}}
	decision, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), Turn{Source: SourceWeb, Message: "hi"})
	if err != nil || decision.Action != ActionReply {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	return names
}
