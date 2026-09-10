package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCoordinatorTraceContextKeyMatchesInboundCoordinator(t *testing.T) {
	if TaskContextCoordinatorTraceKey != inboundcoord.CoordinatorTraceIDContextKey {
		t.Fatalf("task context key %q != coordinator key %q", TaskContextCoordinatorTraceKey, inboundcoord.CoordinatorTraceIDContextKey)
	}
	stamped := inboundcoord.WithCoordinatorTraceID([]byte(`{"dispatch_source":{"type":"robot"}}`), "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7")
	if got := parseTaskTraceContext(stamped); got.CoordinatorTraceID != "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7" || got.DispatchSource != "robot" {
		t.Fatalf("stamped context = %+v", got)
	}
}

func taskTraceTestClient(t *testing.T) (*langfuse.Client, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	client := langfuse.NewWithExporter(langfuse.Config{PublicKey: "pk", SecretKey: "sk", BaseURL: "https://langfuse.example.test"}, exporter)
	t.Cleanup(func() { _ = client.Shutdown(context.Background()) })
	return client, exporter
}

func taskTraceAttr(span tracetest.SpanStub, key string) string {
	for _, kv := range span.Attributes {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

func TestParseTaskTraceContextExtractsLookupKeys(t *testing.T) {
	raw := []byte(`{
		"chat_trace":{"trace_id":"37d0871a-3657-4c74-91fa-39e846fa90a0","channel":"dingtalk","started_at_unix_ms":1721000000123},
		"coordinator_trace_id":"5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7",
		"coordinator_issue_trigger":"new_issue",
		"dispatch_source":{"type":"digital_employee"},
		"dispatch_surface":{"type":"issue"},
		"external_identity":{"dws":{"uid":"u-1","org_id":"org-1"}},
		"dispatch_event_data":{
			"conversation":{"openConversationId":"cidXYZ==","type":"group","title":"项目群"},
			"sender":{"displayName":"冬翔","staffId":"staff-1","openDingTalkId":"open-1"}
		}
	}`)
	got := parseTaskTraceContext(raw)
	want := taskTraceContext{
		ConversationID: "cidXYZ==", ConversationTitle: "项目群", ConversationKind: "group",
		SenderName: "冬翔", PersonID: "staff-1", DWSUID: "u-1", DWSOrgID: "org-1",
		CoordinatorTraceID: "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7", Channel: "dingtalk",
		DispatchSource: "digital_employee", IssueTrigger: "new_issue", SurfaceType: "issue",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTaskTraceContext = %+v, want %+v", got, want)
	}
	if empty := parseTaskTraceContext(nil); !reflect.DeepEqual(empty, taskTraceContext{}) {
		t.Fatalf("empty context = %+v", empty)
	}
	if malformed := parseTaskTraceContext([]byte(`{"dispatch_event_data":"nope"`)); !reflect.DeepEqual(malformed, taskTraceContext{}) {
		t.Fatalf("malformed context = %+v", malformed)
	}
	tagged := parseTaskTraceContext([]byte(`{"coordinator_trace_tags":["inbound_coordinator"," source:web ",""]}`))
	if strings.Join(tagged.CoordinatorTraceTags, ",") != "inbound_coordinator,source:web" {
		t.Fatalf("coordinator trace tags = %v", tagged.CoordinatorTraceTags)
	}
}

func TestTaskLangfuseTraceOptionsUsesTaskTraceAndDeterministicRoot(t *testing.T) {
	taskID := pgtype.UUID{Bytes: [16]byte{0xaa, 0xbb}, Valid: true}
	createdAt := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	task := db.AgentTaskQueue{
		ID:              taskID,
		AgentID:         pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		IssueID:         pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		ChatSessionID:   pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		InitiatorUserID: pgtype.UUID{Bytes: [16]byte{4}, Valid: true},
		Status:          "completed",
		CreatedAt:       pgtype.Timestamptz{Time: createdAt, Valid: true},
		Context:         []byte(`{"chat_trace":{"trace_id":"37d0871a-3657-4c74-91fa-39e846fa90a0","channel":"web","started_at_unix_ms":1721000000123}}`),
	}
	agent := &db.Agent{Name: "FDE教练", WorkspaceID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}
	runtime := &db.AgentRuntime{Name: "FC candidate", RuntimeMode: "cloud", Provider: "hermes"}
	opts := TaskLangfuseTraceOptions(task, agent, runtime)
	if opts.TraceID != "37d0871a-3657-4c74-91fa-39e846fa90a0" {
		t.Fatalf("trace id = %s", opts.TraceID)
	}
	if opts.RootSpanID != TaskLangfuseRootSpanID("aabb0000-0000-0000-0000-000000000000") || len(opts.RootSpanID) != 16 {
		t.Fatalf("root span id = %s", opts.RootSpanID)
	}
	if opts.UserID != "04000000-0000-0000-0000-000000000000" || opts.SessionID != "03000000-0000-0000-0000-000000000000" {
		t.Fatalf("user/session = %s/%s", opts.UserID, opts.SessionID)
	}
	wantTags := "agent_task,runtime-cloud,provider-hermes,channel-web," +
		"agent-01000000-0000-0000-0000-000000000000,agent_name-FDE教练,workspace-09000000-0000-0000-0000-000000000000," +
		"user-04000000-0000-0000-0000-000000000000,task-aabb0000-0000-0000-0000-000000000000,issue-02000000-0000-0000-0000-000000000000"
	if strings.Join(opts.Tags, ",") != wantTags {
		t.Fatalf("tags = %v", opts.Tags)
	}
	// A task-owned trace reaches task, issue, agent, workspace and user
	// through tags and the chat session through the session, so none of
	// them is indexed as an event.
	keys := TaskIndexKeys(task)
	for _, covered := range []string{"task_id", "issue_id", "agent_id", "workspace_id", "chat_session_id", "initiator_user_id"} {
		if _, indexed := keys[covered]; indexed {
			t.Fatalf("%s is covered by a tag or the session and must not be indexed: %v", covered, keys)
		}
	}
	joined := task
	joined.Context = []byte(`{"coordinator_trace_id":"5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7","coordinator_trace_tags":["inbound_coordinator"]}`)
	joinedKeys := TaskIndexKeys(joined)
	if joinedKeys["task_id"] != "aabb0000-0000-0000-0000-000000000000" || joinedKeys["issue_id"] != "02000000-0000-0000-0000-000000000000" {
		t.Fatalf("coordinator-owned task must index task and issue ids: %v", joinedKeys)
	}
	for key, want := range map[string]string{
		"task_id": "aabb0000-0000-0000-0000-000000000000", "agent_name": "FDE教练", "provider": "hermes",
		"workspace_id": "09000000-0000-0000-0000-000000000000", "issue_id": "02000000-0000-0000-0000-000000000000",
		"channel": "web", "runtime_mode": "cloud",
	} {
		if got, _ := opts.Metadata[key].(string); got != want {
			t.Errorf("metadata %s = %q, want %q", key, got, want)
		}
	}

	if opts.Name != taskTraceName || opts.TraceName != "" {
		t.Fatalf("task-owned trace name = %q (trace name=%q)", opts.Name, opts.TraceName)
	}

	// Without a chat trace the task id is the trace root, as the daemon sees it.
	plain := TaskLangfuseTraceOptions(db.AgentTaskQueue{ID: taskID, CreatedAt: task.CreatedAt}, nil, nil)
	if plain.TraceID != "aabb0000-0000-0000-0000-000000000000" {
		t.Fatalf("plain trace id = %s", plain.TraceID)
	}

	// A task started by a coordinator turn shares that turn's trace and must
	// not rename it.
	owned := TaskLangfuseTraceOptions(db.AgentTaskQueue{
		ID: taskID, CreatedAt: task.CreatedAt,
		Context: []byte(`{"coordinator_trace_id":"5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7","coordinator_trace_tags":["inbound_coordinator","source:digital_employee","kind:group"]}`),
	}, nil, runtime)
	if owned.Name != taskTraceName || owned.TraceName != coordinatorTraceName {
		t.Fatalf("coordinator-owned trace: root name %q, trace name %q", owned.Name, owned.TraceName)
	}
	if strings.Join(owned.Tags, ",") != "inbound_coordinator,source:digital_employee,kind:group" {
		t.Fatalf("coordinator-owned tags = %v", owned.Tags)
	}
	if got, _ := owned.Metadata["coord_trace_id"].(string); got != "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7" {
		t.Fatalf("coord_trace_id metadata = %q", got)
	}
	if got, _ := owned.Metadata["provider"].(string); got != "hermes" {
		t.Fatalf("runtime metadata must survive on coordinator-owned tasks: %q", got)
	}
	// Conversation-level keys belong to the turn: the task must not write
	// them at trace level (last writer wins) nor on its root observation
	// (Langfuse folds root observation metadata into the trace as well).
	for _, key := range []string{"loop", "conversation_kind", "agent_id"} {
		if _, ok := owned.Metadata[key]; ok {
			t.Fatalf("coordinator-owned task must not write trace metadata %q", key)
		}
	}
	if owned.RootMetadata != nil {
		t.Fatalf("coordinator-owned task must not carry root metadata: %v", owned.RootMetadata)
	}
	if plain.Metadata["loop"] != taskTraceName {
		t.Fatalf("task-owned trace keeps loop at trace level: %v", plain.Metadata["loop"])
	}
	bare := TaskLangfuseTraceOptions(db.AgentTaskQueue{
		ID: taskID, CreatedAt: task.CreatedAt,
		Context: []byte(`{"coordinator_trace_id":"5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7"}`),
	}, nil, nil)
	if strings.Join(bare.Tags, ",") != coordinatorTraceName {
		t.Fatalf("coordinator-owned tags without stamp = %v", bare.Tags)
	}
}

func TestEmitTaskMessageObservationsPairsToolUseWithResult(t *testing.T) {
	client, exporter := taskTraceTestClient(t)
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	at := func(sec int) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: base.Add(time.Duration(sec) * time.Second), Valid: true}
	}
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
	messages := []db.TaskMessage{
		{Seq: 0, Type: "thinking", Content: text("先看"), CreatedAt: at(1)},
		{Seq: 1, Type: "thinking", Content: text("看"), CreatedAt: at(1)},
		{Seq: 2, Type: "tool_use", Tool: text("bash"), Input: []byte(`{"cmd":"ls"}`), CreatedAt: at(2)},
		{Seq: 3, Type: "tool_result", Tool: text("bash"), Output: text("README.md"), CreatedAt: at(5)},
		{Seq: 4, Type: "tool_use", Tool: text("dws"), Input: []byte(`{"cmd":"chat"}`), Output: text("sent"), CreatedAt: at(6)},
		{Seq: 5, Type: "text", Content: text("完成了"), CreatedAt: at(7)},
		{Seq: 6, Type: "status", Content: text("running"), CreatedAt: at(7)},
		{Seq: 7, Type: "tool_use", Tool: text("never_finished"), CreatedAt: at(8)},
	}
	trace := client.StartTrace(context.Background(), langfuse.TraceOptions{Name: taskTraceName, StartTime: base})
	emitTaskMessageObservations(trace, messages, base.Add(20*time.Second))
	trace.End(langfuse.EndOptions{EndTime: base.Add(20 * time.Second)})

	spans := exporter.GetSpans()
	byName := map[string]tracetest.SpanStub{}
	for _, span := range spans {
		byName[span.Name] = span
	}
	if len(spans) != 6 {
		names := make([]string, 0, len(spans))
		for _, span := range spans {
			names = append(names, span.Name)
		}
		t.Fatalf("spans = %v", names)
	}
	bash, ok := byName["bash"]
	if !ok {
		t.Fatal("bash tool span missing")
	}
	if !bash.StartTime.Equal(base.Add(2*time.Second)) || !bash.EndTime.Equal(base.Add(5*time.Second)) {
		t.Errorf("bash timing = %s..%s", bash.StartTime, bash.EndTime)
	}
	if got := taskTraceAttr(bash, "langfuse.observation.output"); got != "README.md" {
		t.Errorf("bash output = %q", got)
	}
	if got := taskTraceAttr(bash, "langfuse.observation.input"); got != `{"cmd":"ls"}` {
		t.Errorf("bash input = %q", got)
	}
	if got := taskTraceAttr(bash, "langfuse.observation.type"); got != "tool" {
		t.Errorf("bash type = %q", got)
	}
	if got := taskTraceAttr(byName["dws"], "langfuse.observation.output"); got != "sent" {
		t.Errorf("dws output = %q", got)
	}
	unfinished := byName["never_finished"]
	if !unfinished.EndTime.Equal(base.Add(20 * time.Second)) {
		t.Errorf("unfinished tool must close at task end, got %s", unfinished.EndTime)
	}
	if got := taskTraceAttr(byName["thinking"], "langfuse.observation.level"); got != "DEBUG" {
		t.Errorf("thinking level = %q", got)
	}
	// Streamed thinking chunks are coalesced into one event.
	if got := taskTraceAttr(byName["thinking"], "langfuse.observation.output"); got != "先看看" {
		t.Errorf("coalesced thinking = %q", got)
	}
	if got := taskTraceAttr(byName["assistant_text"], "langfuse.observation.output"); got != "完成了" {
		t.Errorf("assistant text = %q", got)
	}
	if _, ok := byName["status"]; ok {
		t.Error("status messages must not become observations")
	}
}

func TestTaskTraceResultAndClip(t *testing.T) {
	if got, ok := taskTraceResult([]byte(` {"ok":true} `)).(json.RawMessage); !ok || string(got) != `{"ok":true}` {
		t.Fatalf("json result = %#v", got)
	}
	if got := taskTraceResult([]byte("null")); got != nil {
		t.Fatalf("null result = %#v", got)
	}
	if got := taskTraceResult([]byte("plain text")); got != "plain text" {
		t.Fatalf("plain result = %#v", got)
	}
	if got := langfuseClip(strings.Repeat("字", 10), 4); got != "字字字字…" {
		t.Fatalf("clip = %q", got)
	}
}
