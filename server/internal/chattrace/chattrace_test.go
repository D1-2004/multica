package chattrace

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMergeParsePreservesTaskContext(t *testing.T) {
	trace, err := From("37d0871a-3657-4c74-91fa-39e846fa90a0", "dingtalk", 1_721_000_000_123)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge([]byte(`{"identity":{"uid":"1"}}`), trace)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(merged, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["identity"]; !ok {
		t.Fatal("existing task context was dropped")
	}
	got, ok, err := Parse(merged)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != trace {
		t.Fatalf("Parse() = (%+v, %v), want (%+v, true)", got, ok, trace)
	}
}

func TestForTaskUsesDurableTaskIdentityWithoutChatContext(t *testing.T) {
	createdAt := time.UnixMilli(1_721_000_000_123)
	trace, err := ForTask(nil, "37d0871a-3657-4c74-91fa-39e846fa90a0", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if trace.TraceID != "37d0871a-3657-4c74-91fa-39e846fa90a0" || trace.Channel != "task" || trace.StartedAtUnixMS != createdAt.UnixMilli() {
		t.Fatalf("task trace = %+v", trace)
	}
}

func TestParseRejectsMalformedTrace(t *testing.T) {
	_, present, err := Parse([]byte(`{"chat_trace":{"trace_id":"bad","channel":"web","started_at_unix_ms":1}}`))
	if !present || err == nil {
		t.Fatalf("Parse() present=%v err=%v, want present=true and an error", present, err)
	}
}
