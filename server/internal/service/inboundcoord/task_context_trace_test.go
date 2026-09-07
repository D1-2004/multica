package inboundcoord

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/chattrace"
)

func TestWithCoordinatorTraceInstallsChatTraceAndStamp(t *testing.T) {
	const traceID = "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7"
	startedAt := time.UnixMilli(1_721_000_000_123)

	// No context at all: both the chat trace and the stamp are added.
	raw := WithCoordinatorTrace(nil, traceID, "web", startedAt)
	trace, present, err := chattrace.Parse(raw)
	if err != nil || !present {
		t.Fatalf("chat trace = %+v present=%v err=%v", trace, present, err)
	}
	if trace.TraceID != traceID || trace.Channel != "web" || trace.StartedAtUnixMS != startedAt.UnixMilli() {
		t.Fatalf("chat trace = %+v", trace)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload[CoordinatorTraceIDContextKey]) != `"`+traceID+`"` {
		t.Fatalf("stamp = %s", payload[CoordinatorTraceIDContextKey])
	}

	// An existing chat trace (the channel engine's inbound trace) is kept.
	existing, err := chattrace.Merge([]byte(`{"dispatch_source":{"type":"robot"}}`), chattrace.Trace{
		TraceID: "37d0871a-3657-4c74-91fa-39e846fa90a0", Channel: "dingtalk", StartedAtUnixMS: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stamped := WithCoordinatorTrace(existing, traceID, "dingtalk", startedAt)
	kept, _, err := chattrace.Parse(stamped)
	if err != nil || kept.TraceID != "37d0871a-3657-4c74-91fa-39e846fa90a0" {
		t.Fatalf("existing chat trace was replaced: %+v err=%v", kept, err)
	}
	if err := json.Unmarshal(stamped, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload[CoordinatorTraceIDContextKey]) != `"`+traceID+`"` || string(payload["dispatch_source"]) != `{"type":"robot"}` {
		t.Fatalf("stamped context = %s", stamped)
	}

	// Empty ids and undecodable contexts leave the input untouched.
	if got := WithCoordinatorTrace([]byte(`{"a":1}`), "", "web", startedAt); string(got) != `{"a":1}` {
		t.Fatalf("empty id changed context: %s", got)
	}
	if got := WithCoordinatorTrace([]byte(`{"a":`), traceID, "web", startedAt); string(got) != `{"a":` {
		t.Fatalf("broken context changed: %s", got)
	}
	if got := WithCoordinatorTrace(nil, "not-a-uuid", "web", startedAt); string(got) != `{"coordinator_trace_id":"not-a-uuid"}` {
		t.Fatalf("invalid trace id must still stamp: %s", got)
	}

	// StampCoordinatorTrace also records the turn's tags for the task trace.
	stampedAll := StampCoordinatorTrace([]byte(`{"a":1}`), Decision{
		TraceID: traceID, TraceTags: []string{"inbound_coordinator", " source:web ", "", "kind:p2p"},
	}, "web", startedAt)
	if err := json.Unmarshal(stampedAll, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload[CoordinatorTraceTagsContextKey]) != `["inbound_coordinator","source:web","kind:p2p"]` || string(payload["a"]) != "1" {
		t.Fatalf("stamped tags = %s", stampedAll)
	}
	if _, present, err := chattrace.Parse(stampedAll); err != nil || !present {
		t.Fatalf("stamp must install the chat trace: present=%v err=%v", present, err)
	}
	if got := WithCoordinatorTraceTags([]byte(`{"a":1}`), nil); string(got) != `{"a":1}` {
		t.Fatalf("empty tags changed context: %s", got)
	}
}
