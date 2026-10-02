package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

func employeeMemoryFixture(t *testing.T) (*dingTalkResponseFixture, agentDispatchContext) {
	t.Helper()
	f, _, dc := employeeFixture(t)
	f.command.Event.Data.Conversation.Type = "single"
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	t.Cleanup(func() {
		for _, table := range []string{"employee_learning", "employee_memory_state"} {
			if _, err := testPool.Exec(context.Background(), "DELETE FROM "+table+" WHERE agent_id=$1", f.agentID); err != nil {
				t.Error(err)
			}
		}
	})
	return f, dc
}
func employeeMemoryTurn(t *testing.T, f *dingTalkResponseFixture, dc agentDispatchContext, text string, run func(int, string, openai.ChatCompletionNewParams) *openai.ChatCompletion) int {
	t.Helper()
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: uuid.NewString(), Text: text, SenderOpenDingTalkID: "requester-open-id"}}
	if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != http.StatusAccepted {
		t.Fatal(r.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	source := receipt + "/" + f.command.Event.Data.Messages[0].OpenMsgID
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		return run(calls, source, p), nil
	})
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	return calls
}
func employeeMemoryAnswer(t *testing.T) *openai.ChatCompletion {
	t.Helper()
	out, err := (&employeeTestModel{}).Chat(context.Background(), openai.ChatCompletionNewParams{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func employeeMemoryRecord(t *testing.T, f *dingTalkResponseFixture, key string) employeememory.LearningRecord {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT record FROM employee_learning WHERE agent_id=$1 AND record->>'key'=$2 AND superseded_by IS NULL AND forgotten_at IS NULL`, f.agentID, key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record employeememory.LearningRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
func TestEmployeeMemoryCaptureCorrectRecallForgetWithoutTask(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	capture := func(value string) {
		t.Helper()
		calls := employeeMemoryTurn(t, f, dc, "请记住 color="+value, func(n int, source string, p openai.ChatCompletionNewParams) *openai.ChatCompletion {
			if n == 1 {
				return employeeReplyCompletion(t, employeeReplyCall(t, "capture", "memory_capture", map[string]any{"source_ref": source, "key": "color", "type": "preference", "quote": "color=" + value}))
			}
			raw, _ := json.Marshal(p.Messages)
			if !strings.Contains(string(raw), `observed`) || !strings.Contains(string(raw), `active`) {
				t.Fatalf("model did not receive committed memory: %s", raw)
			}
			return employeeMemoryAnswer(t)
		})
		if calls != 2 {
			t.Fatalf("capture calls=%d", calls)
		}
	}
	recall := func(want, absent string) {
		t.Helper()
		calls := employeeMemoryTurn(t, f, dc, "我之前的颜色偏好是什么？", func(n int, source string, p openai.ChatCompletionNewParams) *openai.ChatCompletion {
			raw, _ := json.Marshal(p.Messages)
			if want != "" && !strings.Contains(string(raw), want) {
				t.Fatalf("memory not in next provider input: %s", raw)
			}
			if absent != "" && strings.Contains(string(raw), absent) {
				t.Fatalf("old memory still recalled: %s", raw)
			}
			return employeeMemoryAnswer(t)
		})
		if calls != 1 {
			t.Fatalf("recall calls=%d", calls)
		}
	}
	capture("MEM_BLUE_9")
	first := employeeMemoryRecord(t, f, "color")
	if first.Trusted || first.Source != employeememory.LearningSourceObserved || first.Confidence != 4 || first.CreatedBy != "dingtalk:456:open_id:requester-open-id" {
		t.Fatalf("Host attribution/trust incorrect: %+v", first)
	}
	recall("MEM_BLUE_9", "")
	capture("MEM_GREEN_7")
	second := employeeMemoryRecord(t, f, "color")
	if second.Supersedes != first.ID {
		t.Fatal("correction lost supersedes")
	}
	recall("MEM_GREEN_7", "MEM_BLUE_9")
	calls := employeeMemoryTurn(t, f, dc, "忘记颜色偏好", func(n int, source string, p openai.ChatCompletionNewParams) *openai.ChatCompletion {
		switch n {
		case 1:
			return employeeReplyCompletion(t, employeeReplyCall(t, "lookup", "memory_lookup", map[string]any{"source_ref": source, "query": "color"}))
		case 2:
			raw, _ := json.Marshal(p.Messages)
			if !strings.Contains(string(raw), second.ID) {
				t.Fatal("lookup lacks record identity")
			}
			return employeeReplyCompletion(t, employeeReplyCall(t, "forget", "memory_forget", map[string]any{"source_ref": source, "record_id": second.ID}))
		default:
			raw, _ := json.Marshal(p.Messages)
			if !strings.Contains(string(raw), "forgotten") {
				t.Fatal("forget lacks committed status")
			}
			return employeeMemoryAnswer(t)
		}
	})
	if calls != 3 {
		t.Fatalf("lookup/forget/reply calls=%d", calls)
	}
	recall("", "MEM_GREEN_7")
	assertEmployeeReplyNoTasks(t, f)
}

func employeeMemoryHost(t *testing.T, f *dingTalkResponseFixture, dc agentDispatchContext, messages []DispatchMessage) (*employeeSceneHost, employeeloop.Identity, employeeSourceMessage) {
	t.Helper()
	f.command.Event.Data.Messages = messages
	if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != http.StatusAccepted {
		t.Fatal(r.Body.String())
	}
	job, err := f.h.EmployeeSceneWorker.store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	envs := make([]employeeDispatchEnvelope, len(job.Items))
	for i, item := range job.Items {
		if err = json.Unmarshal(item.Payload, &envs[i]); err != nil {
			t.Fatal(err)
		}
	}
	host := &employeeSceneHost{worker: f.h.EmployeeSceneWorker, job: job, envelopes: envs}
	identity := employeeloop.Identity{WorkspaceID: job.Scope.WorkspaceID, AgentID: job.Scope.AgentID, TenantOrgID: job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: job.Scope.SceneID}, ReceiptID: job.Items[0].ReceiptID}
	return host, identity, employeeSourceMessages(job.Items[0], envs[0])[0]
}
func employeeMemoryScope(host *employeeSceneHost, source employeeSourceMessage) employeememory.Scope {
	return employeememory.Scope{WorkspaceID: parseUUID(host.job.Scope.WorkspaceID), AgentID: parseUUID(host.job.Scope.AgentID), TenantOrgID: host.job.Scope.TenantOrgID, Scene: scene.Ref{SceneID: host.job.Scope.SceneID}, Kind: employeememory.ScopePrivate, PrincipalID: source.RequesterRef}
}
func employeeCaptureCall(source employeeSourceMessage, key, quote string) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "memory_capture", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "key": key, "type": "preference", "quote": quote}}
}
func employeeMemoryEntry(t *testing.T, result employeeloop.ToolResult) employeememory.PrivateEntry {
	t.Helper()
	var entry employeememory.PrivateEntry
	if err := json.Unmarshal([]byte(result.Content), &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestEmployeeMemoryHostQuoteAndAuthorityBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, quote string
		reaction    bool
		extra       map[string]any
		allowed     bool
	}{
		{name: "outer_correction_with_quote", quote: "outer-value", allowed: true},
		{name: "quoted_background_is_not_outer", quote: "background-value"},
		{name: "fabricated_quote", quote: "invented-value"},
		{name: "reaction_is_not_statement", quote: "outer-value", reaction: true},
		{name: "model_scope", quote: "outer-value", extra: map[string]any{"scope": "scene"}},
		{name: "model_actor", quote: "outer-value", extra: map[string]any{"actor": "someone-else"}},
		{name: "model_trust", quote: "outer-value", extra: map[string]any{"trusted": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, dc := employeeMemoryFixture(t)
			message := DispatchMessage{OpenMsgID: "source-1", Text: "纠正为 outer-value", SenderOpenDingTalkID: "requester-open-id", ReferencedMessage: &DispatchReferencedMessage{Text: "background-value", SenderUID: "another-person"}}
			if tc.reaction {
				message.Reaction = &DispatchMessageReaction{Action: "add", EmotionName: "LIKE"}
			}
			host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{message})
			call := employeeCaptureCall(source, "preference", tc.quote)
			for key, value := range tc.extra {
				call.Arguments[key] = value
			}
			result, err := host.Execute(context.Background(), id, call)
			if tc.allowed {
				if err != nil || result.Receipt == "" {
					t.Fatalf("outer correction rejected: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid source/authority accepted")
				}
				var count int
				_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_learning WHERE agent_id=$1`, f.agentID).Scan(&count)
				if count != 0 {
					t.Fatal("rejected call wrote learning")
				}
			}
		})
	}
}
func TestEmployeeMemoryReplayReportsCurrentStateAndKeepsEvidenceIdentity(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "one-evidence", Text: "原值 BLUE，另有 GREEN", SenderOpenDingTalkID: "requester-open-id"}})
	ctx := context.Background()
	first, err := host.Execute(ctx, id, employeeCaptureCall(source, "color", "BLUE"))
	if err != nil {
		t.Fatal(err)
	}
	saved := employeeMemoryEntry(t, first)
	changed, err := host.Execute(ctx, id, employeeCaptureCall(source, "other-key", "GREEN"))
	if err != nil {
		t.Fatal(err)
	}
	same := employeeMemoryEntry(t, changed)
	if same.Record.ID != saved.Record.ID || same.Record.Key != "color" || same.Record.Insight != "BLUE" {
		t.Fatalf("same evidence changed identity: %+v", same)
	}
	scope := employeeMemoryScope(host, source)
	newer, err := f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "color", Insight: "NEW_VALUE", Source: employeememory.LearningSourceObserved, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "new-message", EvidenceID: "new-evidence", ActorID: source.RequesterRef})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := host.Execute(ctx, id, employeeCaptureCall(source, "another-key", "GREEN"))
	if err != nil {
		t.Fatal(err)
	}
	if entry := employeeMemoryEntry(t, replay); entry.State != "superseded" || entry.Record.ID != saved.Record.ID {
		t.Fatalf("superseded capture replay claims active: %+v", entry)
	}
	forget := employeeloop.ToolCall{Name: "memory_forget", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "record_id": saved.Record.ID}}
	if _, err = host.Execute(ctx, id, forget); err != nil {
		t.Fatal(err)
	}
	replay, err = host.Execute(ctx, id, employeeCaptureCall(source, "yet-another-key", "GREEN"))
	if err != nil {
		t.Fatal(err)
	}
	if entry := employeeMemoryEntry(t, replay); entry.State != "forgotten" {
		t.Fatalf("forgotten capture replay claims active: %+v", entry)
	}
	results, err := f.h.EmployeeMemory.Search(ctx, scope, "", 8)
	if err != nil || len(results) != 1 || results[0].ID != newer.ID {
		t.Fatalf("forget removed replacement: %+v %v", results, err)
	}
	var revisionBefore, revisionAfter int64
	_ = testPool.QueryRow(ctx, `SELECT revision FROM employee_memory_state WHERE agent_id=$1 AND principal_id=$2`, f.agentID, source.RequesterRef).Scan(&revisionBefore)
	forget.NativeToolCallID = uuid.NewString()
	result, err := host.Execute(ctx, id, forget)
	if err != nil {
		t.Fatal(err)
	}
	if employeeMemoryEntry(t, result).Changed {
		t.Fatal("already forgotten reports new mutation")
	}
	_ = testPool.QueryRow(ctx, `SELECT revision FROM employee_memory_state WHERE agent_id=$1 AND principal_id=$2`, f.agentID, source.RequesterRef).Scan(&revisionAfter)
	if revisionAfter != revisionBefore {
		t.Fatal("no-op forget advanced revision")
	}
	if err = f.h.EmployeeMemory.Reset(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Execute(ctx, id, employeeCaptureCall(source, "reset-bypass", "GREEN")); err == nil {
		t.Fatal("pre-reset source revived memory")
	}
	results, err = f.h.EmployeeMemory.Search(ctx, scope, "", 8)
	if err != nil || len(results) != 0 {
		t.Fatal("reset tombstone bypassed")
	}
}
func TestEmployeeMemoryPrivateOperationsRejectMixedOrUnknownWindow(t *testing.T) {
	for _, other := range []string{"another-requester", ""} {
		t.Run("other_"+other, func(t *testing.T) {
			f, dc := employeeMemoryFixture(t)
			host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "one", Text: "remember BLUE", SenderOpenDingTalkID: "requester-open-id"}, {OpenMsgID: "two", Text: "hello", SenderOpenDingTalkID: other}})
			for _, call := range []employeeloop.ToolCall{employeeCaptureCall(source, "color", "BLUE"), {Name: "memory_lookup", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "query": "color"}}, {Name: "memory_forget", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "record_id": uuid.NewString()}}} {
				if result, err := host.Execute(context.Background(), id, call); err == nil || result.Content != "" {
					t.Fatalf("mixed window private operation accepted: %+v %v", result, err)
				}
			}
		})
	}
}

func TestEmployeeMemoryToolsSingleConnectionAndReceiptTime(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "memory-single", Text: "remember BLUE", SenderOpenDingTalkID: "requester-open-id"}})
	cfg := testPool.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	view := *f.h
	view.DB, view.TxStarter, view.Queries = pool, pool, db.New(pool)
	view.EmployeeMemory = employeememory.NewStore(pool)
	host.worker = NewEmployeeSceneWorker(&view, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := host.Execute(ctx, id, employeeCaptureCall(source, "color", "BLUE"))
	if err != nil {
		t.Fatalf("single-connection capture: %v", err)
	}
	entry := employeeMemoryEntry(t, result)
	var receiptTime time.Time
	if err = testPool.QueryRow(ctx, `SELECT created_at FROM scene_event_receipt WHERE id=$1`, source.ReceiptID).Scan(&receiptTime); err != nil {
		t.Fatal(err)
	}
	if !entry.Record.EvidenceOccurredAt.Equal(receiptTime) {
		t.Fatalf("evidence time is not the original receipt time: %v %v", entry.Record.EvidenceOccurredAt, receiptTime)
	}
	if _, err = host.Execute(ctx, id, employeeloop.ToolCall{Name: "memory_lookup", NativeToolCallID: "lookup-single", Arguments: map[string]any{"source_ref": source.SourceRef, "query": "color"}}); err != nil {
		t.Fatalf("lookup borrowed own pool connection: %v", err)
	}
	if _, err = host.Execute(ctx, id, employeeloop.ToolCall{Name: "memory_forget", NativeToolCallID: "forget-single", Arguments: map[string]any{"source_ref": source.SourceRef, "record_id": entry.Record.ID}}); err != nil {
		t.Fatalf("single-connection forget: %v", err)
	}
}

func TestEmployeeMemoryJournalFailureRollsBackCapture(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "rollback-source", Text: "remember BLUE", SenderOpenDingTalkID: "requester-open-id"}})
	ctx := context.Background()
	client, exporter := employeeTraceClient(t)
	trace := employeeTraceStart(ctx, client, host.job)
	ctx = langfuse.ContextWithTrace(ctx, trace)
	defer trace.End(langfuse.EndOptions{})
	name := "memory_journal_failure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected journal checkpoint failure'; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DROP TRIGGER IF EXISTS `+name+` ON employee_scene_job`)
		_, _ = testPool.Exec(ctx, `DROP FUNCTION IF EXISTS `+name+`()`)
	})
	if _, err := testPool.Exec(ctx, `CREATE TRIGGER `+name+` BEFORE UPDATE OF tool_journal ON employee_scene_job FOR EACH ROW WHEN (NEW.id='`+host.job.ID+`'::uuid) EXECUTE FUNCTION `+name+`() `); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Execute(ctx, id, employeeCaptureCall(source, "color", "BLUE")); err == nil {
		t.Fatal("injected journal failure ignored")
	}
	spans := employeeTraceKind(exporter, "tool")
	if len(spans) != 1 || employeeTraceAttr(spans[0], "langfuse.observation.metadata.journal_committed") != "false" || employeeTraceAttr(spans[0], "langfuse.observation.level") != "ERROR" {
		t.Fatal("rolled-back memory trace claims committed effect")
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_learning WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("capture escaped journal rollback: %d %v", count, err)
	}
}

func TestEmployeeMemoryLateJobCannotReplaceNewerCorrection(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	oldHost, oldID, oldSource := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "old", Text: "remember OLD_VALUE", SenderOpenDingTalkID: "requester-open-id"}})
	if err := oldHost.worker.store.Retry(ctx, oldHost.job, "deferred"); err != nil {
		t.Fatal(err)
	}
	newHost, newID, newSource := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "new", Text: "correct NEW_VALUE", SenderOpenDingTalkID: "requester-open-id"}})
	result, err := newHost.Execute(ctx, newID, employeeCaptureCall(newSource, "color", "NEW_VALUE"))
	if err != nil {
		t.Fatal(err)
	}
	current := employeeMemoryEntry(t, result)
	if err = newHost.worker.store.SaveOutcome(ctx, newHost.job, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err = newHost.worker.store.Complete(ctx, newHost.job, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, oldHost.job.ID); err != nil {
		t.Fatal(err)
	}
	oldHost.job, err = oldHost.worker.store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err = oldHost.Execute(ctx, oldID, employeeCaptureCall(oldSource, "color", "OLD_VALUE"))
	if err != nil {
		t.Fatal(err)
	}
	late := employeeMemoryEntry(t, result)
	if late.State != "superseded" || late.Record.ID == current.Record.ID {
		t.Fatalf("old source replaced correction: %+v", late)
	}
	changedType := employeeCaptureCall(oldSource, "bypass-key", "OLD_VALUE")
	changedType.Arguments["type"] = "operational"
	result, err = oldHost.Execute(ctx, oldID, changedType)
	if err != nil {
		t.Fatal(err)
	}
	if replay := employeeMemoryEntry(t, result); replay.State != "superseded" || replay.Record.ID != late.Record.ID || replay.Record.Key != "color" {
		t.Fatal("late evidence changed type/key to bypass tombstone")
	}
	records, err := f.h.EmployeeMemory.Search(ctx, employeeMemoryScope(oldHost, oldSource), "", 8)
	if err != nil || len(records) != 1 || records[0].ID != current.Record.ID {
		t.Fatalf("newer correction not current: %+v %v", records, err)
	}
}

func TestEmployeeMemoryScopeAndTrustedRecordProtection(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "untrusted", Text: "remember UNTRUSTED_VALUE", SenderOpenDingTalkID: "requester-open-id"}})
	scope := employeeMemoryScope(host, source)
	trusted, err := f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "color", Insight: "TRUSTED_VALUE", Confidence: 8}, employeememory.TrustedEvidence{SourceID: "verified-human", EvidenceID: "statement", ActorID: source.RequesterRef, HumanStated: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Execute(ctx, id, employeeCaptureCall(source, "color", "UNTRUSTED_VALUE")); err == nil {
		t.Fatal("untrusted source replaced trusted memory")
	}
	other := scope
	other.PrincipalID = "other-requester"
	foreign, err := f.h.EmployeeMemory.Record(ctx, other, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "color", Insight: "OTHER_VALUE", Confidence: 4}, employeememory.TrustedEvidence{SourceID: "other", EvidenceID: "statement", ActorID: "other-requester"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Execute(ctx, id, employeeloop.ToolCall{Name: "memory_forget", NativeToolCallID: "foreign", Arguments: map[string]any{"source_ref": source.SourceRef, "record_id": foreign.ID}}); err == nil {
		t.Fatal("forgot other requester's record")
	}
	lookup, err := host.Execute(ctx, id, employeeloop.ToolCall{Name: "memory_lookup", NativeToolCallID: "lookup", Arguments: map[string]any{"source_ref": source.SourceRef, "query": "color"}})
	if err != nil || strings.Contains(lookup.Content, "OTHER_VALUE") || !strings.Contains(lookup.Content, trusted.ID) {
		t.Fatalf("private lookup widened: %s %v", lookup.Content, err)
	}
	forged := employeeCaptureCall(source, "unknown", "UNTRUSTED_VALUE")
	forged.Arguments["source_ref"] = "unbound/source"
	if _, err = host.Execute(ctx, id, forged); err == nil {
		t.Fatal("unbound source accepted")
	}
	for _, dimension := range []string{"tenant", "scene"} {
		wrong := id
		if dimension == "tenant" {
			wrong.TenantOrgID = "other-org"
		} else {
			wrong.Scene.SceneID = uuid.NewString()
		}
		if _, err = host.Execute(ctx, wrong, employeeCaptureCall(source, "scope", "UNTRUSTED_VALUE")); err == nil {
			t.Fatalf("forged %s accepted", dimension)
		}
	}
}

func TestEmployeeMemorySameNativeCallReplayRefreshesState(t *testing.T) {
	for _, change := range []string{"forget", "reset", "correct"} {
		t.Run(change, func(t *testing.T) {
			f, dc := employeeMemoryFixture(t)
			host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "same-call", Text: "remember BLUE", SenderOpenDingTalkID: "requester-open-id"}})
			ctx := context.Background()
			call := employeeCaptureCall(source, "color", "BLUE")
			result, err := host.Execute(ctx, id, call)
			if err != nil {
				t.Fatal(err)
			}
			original := employeeMemoryEntry(t, result)
			scope := employeeMemoryScope(host, source)
			want := "forgotten"
			lookup := employeeloop.ToolCall{Name: "memory_lookup", NativeToolCallID: "same-lookup", Arguments: map[string]any{"source_ref": source.SourceRef, "query": "BLUE"}}
			if _, err = host.Execute(ctx, id, lookup); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "forget":
				tx, _ := testPool.Begin(ctx)
				_, err = f.h.EmployeeMemory.ForgetPrivateTx(ctx, tx, scope, original.Record.ID)
				if err == nil {
					err = tx.Commit(ctx)
				} else {
					_ = tx.Rollback(ctx)
				}
			case "reset":
				err = f.h.EmployeeMemory.Reset(ctx, scope)
			case "correct":
				want = "superseded"
				_, err = f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "color", Insight: "GREEN", Confidence: 4}, employeememory.TrustedEvidence{SourceID: "correction", EvidenceID: "correction", ActorID: source.RequesterRef})
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err = host.Execute(ctx, id, call)
			if err != nil {
				t.Fatal(err)
			}
			entry := employeeMemoryEntry(t, result)
			if entry.State != want || entry.Changed {
				t.Fatalf("same native call reused stale state: %+v", entry)
			}
			result, err = host.Execute(ctx, id, lookup)
			if err != nil || strings.Contains(result.Content, "BLUE") {
				t.Fatalf("cached lookup disclosed inactive memory: %s %v", result.Content, err)
			}
		})
	}
}
func TestEmployeeMemoryAmbiguousSourceIsRejected(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "duplicate", Text: "FIRST", SenderOpenDingTalkID: "requester-open-id"}, {OpenMsgID: "duplicate", Text: "LAST", SenderOpenDingTalkID: "requester-open-id"}})
	if _, err := host.Execute(context.Background(), id, employeeCaptureCall(source, "ambiguous", "LAST")); err == nil {
		t.Fatal("duplicate source_ref selected a different message")
	}
}

func TestEmployeeMemoryChangedReplayTerminatesExistingModelJournal(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	host, identity, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "crash-source", Text: "remember BLUE", SenderOpenDingTalkID: "requester-open-id"}})
	worker := host.worker
	input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	if _, err = worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	calls := 0
	original := employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		if calls == 1 {
			return employeeReplyCompletion(t, employeeReplyCall(t, "capture-before-crash", "memory_capture", map[string]any{"source_ref": source.SourceRef, "key": "color", "type": "preference", "quote": "BLUE"})), nil
		}
		return employeeMemoryAnswer(t), nil
	})
	if _, err = employeeloop.New(input.Config, &employeeJournalModel{store: worker.store, job: host.job, delegate: original}, host).Run(ctx, input.Input); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("fixture did not freeze following model response")
	}
	saved := employeeMemoryRecord(t, f, "color")
	tx, _ := testPool.Begin(ctx)
	if _, err = f.h.EmployeeMemory.ForgetPrivateTx(ctx, tx, employeeMemoryScope(host, source), saved.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = worker.store.Retry(ctx, host.job, "restart before outcome checkpoint"); err != nil {
		t.Fatal(err)
	}
	_, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	worker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		providerCalls++
		return nil, errors.New("must not request model after replay conflict")
	})
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("replay did not terminate cleanly: %v %v", worked, err)
	}
	var state string
	var outcome []byte
	var attempts int
	if err = testPool.QueryRow(ctx, `SELECT state,outcome,model_attempts FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&state, &outcome, &attempts); err != nil {
		t.Fatal(err)
	}
	var terminal employeeSavedOutcome
	if err = json.Unmarshal(outcome, &terminal); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || terminal.Failure == "" || providerCalls != 0 || attempts != 2 || strings.Contains(terminal.Outcome.Reply, "BLUE") {
		t.Fatalf("stale reply escaped or replay kept running: state=%s failure=%s provider=%d attempts=%d", state, terminal.Failure, providerCalls, attempts)
	}
	if worked, err := worker.ProcessNext(ctx); err != nil || worked {
		t.Fatalf("terminal replay job was reclaimed: %v %v", worked, err)
	}
	_ = identity
}

func TestEmployeeMemoryUpgradePreservesFrozenOldToolSchema(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	host, _, _ := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "old-schema", Text: "hello", SenderOpenDingTalkID: "requester-open-id"}})
	worker := host.worker
	input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	var oldTools []employeeloop.Tool
	for _, tool := range input.Config.Tools {
		if !isEmployeeMemoryTool(tool.Name) {
			oldTools = append(oldTools, tool)
		}
	}
	input.Config.Tools = oldTools
	raw, _ := json.Marshal(input)
	if _, err = worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	calls := 0
	oldModel := employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(p.Tools)
		if strings.Contains(string(raw), "memory_capture") {
			t.Fatal("old schema expanded")
		}
		return employeeMemoryAnswer(t), nil
	})
	if _, err = employeeloop.New(input.Config, &employeeJournalModel{store: worker.store, job: host.job, delegate: oldModel}, host).Run(ctx, input.Input); err != nil {
		t.Fatal(err)
	}
	if err = worker.store.Retry(ctx, host.job, "restart"); err != nil {
		t.Fatal(err)
	}
	_, _ = testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID)
	worker.model = oldModel
	if _, err = worker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("old frozen schema replay requested model %d times", calls)
	}
	var failure string
	if err = testPool.QueryRow(ctx, `SELECT COALESCE(outcome->>'failure','') FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&failure); err != nil || failure != "" {
		t.Fatalf("old journal conflicted: %s %v", failure, err)
	}
}

func TestEmployeeMemoryUnchangedLookupAndForgetRecoverFrozenReply(t *testing.T) {
	for _, name := range []string{"memory_lookup", "memory_forget"} {
		t.Run(name, func(t *testing.T) {
			f, dc := employeeMemoryFixture(t)
			ctx := context.Background()
			host, _, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "stable-replay", Text: "查看或忘记颜色", SenderOpenDingTalkID: "requester-open-id"}})
			worker := host.worker
			stored, err := f.h.EmployeeMemory.Record(ctx, employeeMemoryScope(host, source), employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "color", Insight: "BLUE", Confidence: 4}, employeememory.TrustedEvidence{SourceID: "seed", EvidenceID: "seed", ActorID: source.RequesterRef})
			if err != nil {
				t.Fatal(err)
			}
			input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(input)
			if _, err = worker.store.SaveInput(ctx, host.job, raw); err != nil {
				t.Fatal(err)
			}
			calls := 0
			model := employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				if calls == 1 {
					args := map[string]any{"source_ref": source.SourceRef}
					if name == "memory_lookup" {
						args["query"] = "color"
					} else {
						args["record_id"] = stored.ID
					}
					return employeeReplyCompletion(t, employeeReplyCall(t, "stable-memory-call", name, args)), nil
				}
				return employeeMemoryAnswer(t), nil
			})
			if _, err = employeeloop.New(input.Config, &employeeJournalModel{store: worker.store, job: host.job, delegate: model}, host).Run(ctx, input.Input); err != nil {
				t.Fatal(err)
			}
			if err = worker.store.Retry(ctx, host.job, "restart before checkpoint"); err != nil {
				t.Fatal(err)
			}
			_, _ = testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID)
			worker.model = model
			if _, err = worker.ProcessNext(ctx); err != nil {
				t.Fatal(err)
			}
			var failure, state string
			if err = testPool.QueryRow(ctx, `SELECT COALESCE(outcome->>'failure',''),state FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&failure, &state); err != nil || failure != "" || state != "completed" || calls != 2 {
				t.Fatalf("unchanged replay conflicted or called provider: failure=%s state=%s calls=%d err=%v", failure, state, calls, err)
			}
		})
	}
}

func TestEmployeeMemoryFailureTraceDistinguishesCommittedJournal(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "trace-error", Text: "real text", SenderOpenDingTalkID: "requester-open-id"}})
	ctx := context.Background()
	client, exporter := employeeTraceClient(t)
	trace := employeeTraceStart(ctx, client, host.job)
	ctx = langfuse.ContextWithTrace(ctx, trace)
	defer trace.End(langfuse.EndOptions{})
	call := employeeCaptureCall(source, "color", "invented text")
	if _, err := host.Execute(ctx, id, call); err == nil {
		t.Fatal("invalid quote accepted")
	}
	spans := employeeTraceKind(exporter, "tool")
	if len(spans) != 1 || employeeTraceAttr(spans[0], "langfuse.observation.metadata.journal_committed") != "true" || employeeTraceAttr(spans[0], "langfuse.observation.level") != "ERROR" {
		t.Fatal("durable business failure confused with transaction rollback")
	}
	var exists bool
	if err := testPool.QueryRow(ctx, `SELECT tool_journal ? $2 FROM employee_scene_job WHERE id=$1`, host.job.ID, call.NativeToolCallID).Scan(&exists); err != nil || !exists {
		t.Fatal("failure journal not committed")
	}
}
