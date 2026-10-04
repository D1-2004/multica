package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/userdecision"
)

func TestUserDecisionChoiceTraceUsesFrozenQuestionAndAcceptedChoice(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	c := &Coordinator{Langfuse: client}
	turn := Turn{TraceID: "12345678-1234-1234-1234-123456789abc", Source: SourceDigitalEmployee, SceneID: testSceneID("cid"), ConversationID: "cid", SenderName: "test"}
	root := c.startTurnTrace(context.Background(), turn, time.Now())
	snapshot := UserDecisionSnapshot{Turn: turn, DecisionID: "decision", TraceRootSpanID: root.RootSpanID()}
	raw, _ := json.Marshal(snapshot)
	r := userdecision.Request{ID: "decision", JobID: turn.TraceID, State: "waiting", Snapshot: raw, CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now(), Proposal: userdecision.Proposal{Question: "如何处理？", RecommendedID: "new", Options: []userdecision.Option{{ID: "new", Label: "新建", Kind: "start_work"}, {ID: "reply", Label: "回复", Kind: "reply"}}}}
	if err := c.ObserveUserDecision(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	r.State = "dispatched"
	r.Submission = &userdecision.Submission{EventID: "trusted-event", OptionID: "reply", Custom: "token=private-secret", OperatorID: "initiator"}
	if err := c.ObserveUserDecision(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	resolution := c.startUserDecisionResolutionTrace(context.Background(), snapshot, *r.Submission)
	child := resolution.StartObservation(langfuse.ObservationOptions{Name: "coordinator.interpret_submission"})
	child.End(langfuse.EndOptions{})
	resolution.End(langfuse.EndOptions{Output: "resolved"})
	root.End(langfuse.EndOptions{Output: "awaiting_user"})
	spans := exporter.GetSpans()
	choices := spansNamed(spans, "coordinator.user_decision.choice")
	if len(choices) != 2 {
		t.Fatal(len(choices))
	}
	if choices[0].SpanContext.SpanID() != choices[1].SpanContext.SpanID() {
		t.Fatal("choice duplicated on retry")
	}
	for _, s := range choices {
		if s.Parent.SpanID().String() != root.RootSpanID() {
			t.Fatal("choice not parented to original turn")
		}
		input, _ := spanAttr(s, "langfuse.observation.input")
		if !strings.Contains(input.AsString(), "如何处理") || !strings.Contains(input.AsString(), "recommended_id") {
			t.Fatal(input)
		}
	}
	first, _ := spanAttr(choices[0], "langfuse.observation.output")
	if !strings.Contains(first.AsString(), `"accepted":false`) || strings.Contains(first.AsString(), "submission") {
		t.Fatal(first)
	}
	last, _ := spanAttr(choices[1], "langfuse.observation.output")
	if !strings.Contains(last.AsString(), `"option_label":"回复"`) || strings.Contains(last.AsString(), "private-secret") {
		t.Fatal(last)
	}
	for _, s := range spans {
		if s.SpanContext.TraceID().String() != langfuse.TraceIDHex(turn.TraceID) {
			t.Fatal("resolution created a separate trace")
		}
		if s.Name != "inbound_coordinator" {
			if _, ok := spanAttr(s, "langfuse.trace.input"); ok {
				t.Fatal("overwrote root input")
			}
			if _, ok := spanAttr(s, "langfuse.trace.output"); ok {
				t.Fatal("overwrote root output")
			}
		}
	}
}

func TestHistoricalDecisionChoiceGetsContentFreeParent(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	c := &Coordinator{Langfuse: client}
	r := userdecision.Request{ID: "old-decision", JobID: "12345678-1234-1234-1234-123456789abc", Snapshot: json.RawMessage(`{"turn":{}}`), State: "expired", CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now()}
	if err := c.ObserveUserDecision(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	group := spansNamed(spans, "coordinator.user_decision")[0]
	choice := spansNamed(spans, "coordinator.user_decision.choice")[0]
	if choice.Parent.SpanID() != group.SpanContext.SpanID() {
		t.Fatal("historical choice is a root")
	}
	for _, key := range []string{"langfuse.observation.input", "langfuse.observation.output", "langfuse.trace.input", "langfuse.trace.output"} {
		if _, ok := spanAttr(group, key); ok {
			t.Fatal("historical group has content")
		}
	}
}
