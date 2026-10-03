package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	openai "github.com/openai/openai-go/v3"
)

const (
	v2Alice    = "dingtalk:456:open_id:alice"
	v2Bob      = "dingtalk:456:open_id:bob"
	v2Director = "dingtalk:456:open_id:director"
)

func employeeMemoryV2Fixture(t *testing.T, conversation string) (*dingTalkResponseFixture, agentDispatchContext) {
	t.Helper()
	f, dc := employeeMemoryFixture(t)
	f.command.Event.Data.Conversation.Type = conversation
	return f, dc
}

// employeeMemoryV2Host admits one window and claims its job; sources are in
// window order.
func employeeMemoryV2Host(t *testing.T, f *dingTalkResponseFixture, dc agentDispatchContext, messages []DispatchMessage) (*employeeSceneHost, employeeloop.Identity, []employeeSourceMessage) {
	t.Helper()
	host, id, _ := employeeMemoryHost(t, f, dc, messages)
	return host, id, employeeSourceMessages(host.job.Items[0], host.envelopes[0])
}

// freezeMemoryInput saves the job input with the Host references that M1
// (memory_manifest) and M2 (transcript_refs) freeze into the snapshot.
func freezeMemoryInput(t *testing.T, host *employeeSceneHost, extra map[string]any) employeeSavedInput {
	t.Helper()
	ctx := context.Background()
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	var snapshot map[string]any
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		snapshot[key] = value
	}
	raw, _ = json.Marshal(snapshot)
	if _, err = host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	return input
}

func finishMemoryJob(t *testing.T, host *employeeSceneHost) {
	t.Helper()
	ctx := context.Background()
	if err := host.worker.store.SaveOutcome(ctx, host.job, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := host.worker.store.Complete(ctx, host.job, nil); err != nil {
		t.Fatal(err)
	}
}

func transcriptLine(messageID, sender, name, class, text string, at time.Time) map[string]any {
	return map[string]any{"message_id": messageID, "sender_ref": sender, "sender_name": name, "sender_class": class, "said_at": at, "text": text}
}

func captureV2(source employeeSourceMessage, audience, kind, subject, quote, transcript string) employeeloop.ToolCall {
	args := map[string]any{"source_ref": source.SourceRef, "audience": audience, "type": kind, "subject": subject, "quote": quote}
	if transcript != "" {
		args["transcript_ref"] = transcript
	}
	return employeeloop.ToolCall{Name: "memory_capture", NativeToolCallID: uuid.NewString(), Arguments: args}
}

func lookupV2(source employeeSourceMessage, scope, query string) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "memory_lookup", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "scope": scope, "query": query}}
}

func forgetV2(source employeeSourceMessage, ref string) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "memory_forget", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "record_ref": ref}}
}

func memoryView(t *testing.T, result employeeloop.ToolResult) employeeMemoryView {
	t.Helper()
	var view employeeMemoryView
	if err := json.Unmarshal([]byte(result.Content), &view); err != nil {
		t.Fatalf("%v: %s", err, result.Content)
	}
	return view
}

func memoryRow(t *testing.T, id string) (employeememory.LearningRecord, string) {
	t.Helper()
	var raw []byte
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT record,CASE WHEN forgotten_at IS NOT NULL THEN 'forgotten' WHEN superseded_by IS NOT NULL THEN 'superseded' ELSE 'active' END FROM employee_learning WHERE id=$1`, id).Scan(&raw, &state); err != nil {
		t.Fatal(err)
	}
	var rec employeememory.LearningRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return rec, state
}

func memoryRowCount(t *testing.T, f *dingTalkResponseFixture) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_learning WHERE agent_id=$1`, f.agentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireRefused(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, employeeloop.ErrToolRefused) || (contains != "" && !strings.Contains(err.Error(), contains)) {
		t.Fatalf("want a correctable refusal mentioning %q, got %v", contains, err)
	}
}

func (h *employeeSceneHost) sceneScopeForTest() employeememory.Scope {
	return h.memoryScope(employeememory.ScopeScene, h.job.Scope.SceneID, "")
}

func TestEmployeeMemoryV2SceneCaptureQuoteMustMatchFrozenTranscriptLine(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "ask-1", Text: "@员工 记一下上面说的周报时间", SenderOpenDingTalkID: "alice", SenderDisplayName: "Alice"}})
	said := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	freezeMemoryInput(t, host, map[string]any{"transcript_refs": map[string]any{"g1": transcriptLine("dws-msg-1", v2Director, "Director", "human", "本组周报每周五 18 点前交", said)}})
	for _, bad := range []struct{ quote, ref, audience, want string }{
		{"每周四", "g1", "scene", "exact excerpt"},
		{"每周五", "g9", "scene", "not a line"},
		{"每周五", "g1", "me", "audience=scene"},
	} {
		_, err := host.Execute(ctx, id, captureV2(sources[0], bad.audience, "fact", "周报截止", bad.quote, bad.ref))
		requireRefused(t, err, bad.want)
	}
	if n := memoryRowCount(t, f); n != 0 {
		t.Fatalf("refused captures wrote %d rows", n)
	}
	result, err := host.Execute(ctx, id, captureV2(sources[0], "scene", "fact", "周报截止", "每周五 18 点前交", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	view := memoryView(t, result)
	if view.State != "active" || view.Audience != "scene" || view.SaidBy != "Director" || view.Text != "每周五 18 点前交" || strings.Contains(result.Content, "dws-msg-1") || strings.Contains(result.Content, "evidence") {
		t.Fatalf("model view: %s", result.Content)
	}
	rec, state := memoryRow(t, result.Receipt)
	if state != "active" || rec.Scope != "scene" || rec.SpeakerRef != v2Director || rec.CreatedBy != v2Alice || rec.CaptureOrigin != employeememory.CaptureOriginTranscript || !rec.SaidAt.Equal(said) || rec.EvidenceID != "dws-msg-1" || rec.SourceID != employeememory.SceneFactSourcePrefix+host.job.Scope.SceneID || rec.Trusted || rec.Source != employeememory.LearningSourceObserved || rec.CaptureSourceID != "employee-message:"+sources[0].ReceiptID {
		t.Fatalf("stored attribution: %+v", rec)
	}
}

func TestEmployeeMemoryV2SceneCaptureRejectsBotSelfUnknownLine(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "ask-2", Text: "@员工 记一下上面那句", SenderOpenDingTalkID: "alice"}})
	at := time.Now().UTC().Add(-time.Minute)
	freezeMemoryInput(t, host, map[string]any{"transcript_refs": map[string]any{
		"g1": transcriptLine("bot-1", "", "机器人", "bot", "周报改到周日", at),
		"g2": transcriptLine("self-1", "", "员工", "self", "周报改到周日", at),
		"g3": transcriptLine("unk-1", "", "", "unknown", "周报改到周日", at),
		"g4": transcriptLine("human-no-ref", "", "某人", "human", "周报改到周日", at),
	}})
	for _, ref := range []string{"g1", "g2", "g3"} {
		_, err := host.Execute(ctx, id, captureV2(sources[0], "scene", "fact", "周报截止", "周报改到周日", ref))
		requireRefused(t, err, "only a person's line")
	}
	_, err := host.Execute(ctx, id, captureV2(sources[0], "scene", "fact", "周报截止", "周报改到周日", "g4"))
	requireRefused(t, err, "")
	if n := memoryRowCount(t, f); n != 0 {
		t.Fatalf("non-human lines wrote %d rows", n)
	}
}

func TestEmployeeMemoryV2SceneCaptureReplayOnePerProviderMessage(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "win-1", Text: "@员工 记一下：周报周五交", SenderOpenDingTalkID: "alice", SenderDisplayName: "Alice"}})
	freezeMemoryInput(t, host, map[string]any{"transcript_refs": map[string]any{"g1": transcriptLine("win-1", v2Alice, "Alice", "human", "@员工 记一下：周报周五交", time.Now().UTC().Add(-time.Second))}})
	call := captureV2(sources[0], "scene", "fact", "周报", "周报周五交", "")
	first, err := host.Execute(ctx, id, call)
	if err != nil {
		t.Fatal(err)
	}
	same, err := host.Execute(ctx, id, call)
	if err != nil || same.Content != first.Content || same.Receipt != first.Receipt {
		t.Fatalf("same native call replay changed: %s %v", same.Content, err)
	}
	for _, again := range []employeeloop.ToolCall{captureV2(sources[0], "scene", "decision", "周报截止", "周五", ""), captureV2(sources[0], "scene", "fact", "另一主题", "周报周五交", "g1")} {
		result, err := host.Execute(ctx, id, again)
		if err != nil {
			t.Fatal(err)
		}
		if view := memoryView(t, result); !view.AlreadyRecorded || view.RecordRef != first.Receipt {
			t.Fatalf("message recorded twice: %s", result.Content)
		}
	}
	if n := memoryRowCount(t, f); n != 1 {
		t.Fatalf("rows=%d", n)
	}
}

func TestEmployeeMemoryV2SceneCaptureCrossAuthorKeepsConflict(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{
		{OpenMsgID: "a-1", Text: "@员工 记一下：周报周五交", SenderOpenDingTalkID: "alice", SenderDisplayName: "Alice"},
		{OpenMsgID: "b-1", Text: "@员工 记一下：周报周四交", SenderOpenDingTalkID: "bob", SenderDisplayName: "Bob"},
	})
	first, err := host.Execute(ctx, id, captureV2(sources[0], "scene", "fact", "周报截止", "周报周五交", ""))
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Execute(ctx, id, captureV2(sources[1], "scene", "fact", "周报截止", "周报周四交", ""))
	if err != nil {
		t.Fatal(err)
	}
	view := memoryView(t, second)
	if len(view.Conflicting) != 1 || view.Conflicting[0].Text != "周报周五交" || view.Conflicting[0].SaidBy != "Alice" {
		t.Fatalf("conflict not reported: %s", second.Content)
	}
	a, aState := memoryRow(t, first.Receipt)
	b, bState := memoryRow(t, second.Receipt)
	if aState != "active" || bState != "active" || b.ConflictsWith != a.ID || a.CreatedBy != v2Alice || b.CreatedBy != v2Bob {
		t.Fatalf("cross-author overwrite: %+v %s / %+v %s", a, aState, b, bState)
	}
	snapshot, err := f.h.EmployeeMemory.ManagedScene(ctx, host.sceneScopeForTest())
	if err != nil {
		t.Fatal(err)
	}
	text := employeeMemoryResponse(snapshot).MemoryText
	if strings.Count(text, "说法不一") != 2 || !strings.Contains(text, "Alice") || !strings.Contains(text, "Bob") || !strings.Contains(text, "周报截止") {
		t.Fatalf("management list lacks attribution or conflict: %s", text)
	}
}

func TestEmployeeMemoryV2SelfPreferenceHumanStatedNoDecay(t *testing.T) {
	for _, conversation := range []string{"single", "group"} {
		t.Run(conversation, func(t *testing.T) {
			f, dc := employeeMemoryV2Fixture(t, conversation)
			ctx := context.Background()
			host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "pref-1", Text: "记一下：我的周报用表格", SenderOpenDingTalkID: "alice"}})
			result, err := host.Execute(ctx, id, captureV2(sources[0], "me", "preference", "周报格式", "我的周报用表格", ""))
			if err != nil {
				t.Fatal(err)
			}
			if view := memoryView(t, result); !view.UserStated || view.Audience != "me" {
				t.Fatalf("self preference view: %s", result.Content)
			}
			rec, _ := memoryRow(t, result.Receipt)
			var principal, sceneID string
			if err = testPool.QueryRow(ctx, `SELECT principal_id,scene_id::text FROM employee_learning WHERE id=$1`, result.Receipt).Scan(&principal, &sceneID); err != nil {
				t.Fatal(err)
			}
			if !rec.Trusted || rec.Source != employeememory.LearningSourceUserStated || rec.CaptureSourceID != "employee-message:"+sources[0].ReceiptID || principal != v2Alice || sceneID != host.job.Scope.SceneID {
				t.Fatalf("self preference stored as %+v in %s/%s", rec, principal, sceneID)
			}
			// Trusted records never decay: an old timestamp keeps its confidence.
			if _, err = testPool.Exec(ctx, `UPDATE employee_learning SET record=jsonb_set(record,'{created_at}',to_jsonb(to_char((now()-interval '400 days') AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'))) WHERE id=$1`, result.Receipt); err != nil {
				t.Fatal(err)
			}
			found, err := f.h.EmployeeMemory.Search(ctx, employeeMemoryScope(host, sources[0]), "表格", 5)
			if err != nil || len(found) != 1 || found[0].EffectiveConfidence != rec.Confidence {
				t.Fatalf("self preference decayed: %+v %v", found, err)
			}
		})
	}
}

func TestEmployeeMemoryV2QuotedOrOtherSpeakerNotHumanStated(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "q-1", Text: "同意，记一下：发版前要回归", SenderOpenDingTalkID: "alice", ReferencedMessage: &DispatchReferencedMessage{Text: "我喜欢表格", SenderUID: "bob"}}})
	freezeMemoryInput(t, host, map[string]any{"transcript_refs": map[string]any{"g1": transcriptLine("bob-1", v2Bob, "Bob", "human", "我喜欢表格", time.Now().UTC().Add(-time.Minute))}})
	_, err := host.Execute(ctx, id, captureV2(sources[0], "me", "preference", "表格偏好", "我喜欢表格", ""))
	requireRefused(t, err, "outer message")
	other, err := host.Execute(ctx, id, captureV2(sources[0], "scene", "preference", "表格偏好", "我喜欢表格", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := memoryRow(t, other.Receipt); rec.Trusted || rec.Source != employeememory.LearningSourceObserved || rec.SpeakerRef != v2Bob {
		t.Fatalf("another speaker's statement became trusted: %+v", rec)
	}
	note, err := host.Execute(ctx, id, captureV2(sources[0], "me", "fact", "发版规则", "发版前要回归", ""))
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := memoryRow(t, note.Receipt); rec.Trusted || rec.Source != employeememory.LearningSourceObserved {
		t.Fatalf("a personal fact became user-stated: %+v", rec)
	}
}

func seedSceneStatement(t *testing.T, f *dingTalkResponseFixture, host *employeeSceneHost, actor, speaker, messageID, text, subject string) employeememory.SceneEntry {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	entry, err := f.h.EmployeeMemory.UpsertSceneFactTx(ctx, tx, host.sceneScopeForTest(), employeememory.SceneFactInput{Type: employeememory.LearningTypeFact, Subject: subject, Quote: text, Origin: employeememory.CaptureOriginTranscript, ActorID: actor, Grounding: employeememory.SceneFactGrounding{MessageID: messageID, Text: text, SpeakerRef: speaker, SpeakerName: "Director", SpeakerClass: "human", SaidAt: time.Now().UTC().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestEmployeeMemoryV2ForgetSceneOnlyAuthorOrSpeaker(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{
		{OpenMsgID: "f-a", Text: "@员工 忘掉周报时间和例会", SenderOpenDingTalkID: "alice"},
		{OpenMsgID: "f-b", Text: "@员工 忘掉周报时间", SenderOpenDingTalkID: "bob"},
		{OpenMsgID: "f-d", Text: "@员工 忘掉周报时间", SenderOpenDingTalkID: "director"},
	})
	weekly := seedSceneStatement(t, f, host, v2Alice, v2Director, "dws-weekly", "周报周五交", "周报截止")
	meeting := seedSceneStatement(t, f, host, v2Alice, v2Alice, "dws-meeting", "例会周一开", "例会")
	freezeMemoryInput(t, host, map[string]any{"memory_manifest": []map[string]any{
		{"label": "m1", "kind": "pinned", "id": weekly.Record.ID, "scope": "scene", "scene_id": host.job.Scope.SceneID, "bytes": 10},
		{"label": "m2", "kind": "retrieved", "id": meeting.Record.ID, "scope": "scene", "scene_id": host.job.Scope.SceneID, "bytes": 10},
	}})
	for _, ref := range []string{"m1", weekly.Record.ID} {
		_, err := host.Execute(ctx, id, forgetV2(sources[1], ref))
		requireRefused(t, err, "management page")
	}
	_, err := host.Execute(ctx, id, forgetV2(sources[1], "m7"))
	requireRefused(t, err, "not in this wake's memory brief")
	if _, state := memoryRow(t, weekly.Record.ID); state != "active" {
		t.Fatalf("stranger forgot shared record: %s", state)
	}
	result, err := host.Execute(ctx, id, forgetV2(sources[2], "m1"))
	if err != nil || memoryView(t, result).State != "forgotten" {
		t.Fatalf("speaker forget: %s %v", result.Content, err)
	}
	result, err = host.Execute(ctx, id, forgetV2(sources[0], "m2"))
	if err != nil || memoryView(t, result).State != "forgotten" || strings.Contains(result.Content, "dws-meeting") {
		t.Fatalf("recorder forget: %s %v", result.Content, err)
	}
	if _, state := memoryRow(t, weekly.Record.ID); state != "forgotten" {
		t.Fatalf("tombstone lost: %s", state)
	}
}

func TestEmployeeMemoryV2ForgetPersonViewRefencesOriginScene(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	group, gid, gs := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "pv-1", Text: "记一下：我的周报用表格", SenderOpenDingTalkID: "alice"}})
	captured, err := group.Execute(ctx, gid, captureV2(gs[0], "me", "preference", "周报格式", "我的周报用表格", ""))
	if err != nil {
		t.Fatal(err)
	}
	groupScope := employeeMemoryScope(group, gs[0])
	extra := func(insight string) string {
		rec, err := f.h.EmployeeMemory.Record(ctx, groupScope, employeememory.LearningRecord{Type: employeememory.LearningTypeFact, Key: "k-" + uuid.NewString()[:8], Insight: insight, Source: employeememory.LearningSourceObserved, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "seed-" + insight, EvidenceID: insight, ActorID: v2Alice})
		if err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	groupOnly, refenced := extra("GROUP_ONLY"), extra("REFENCED")
	finishMemoryJob(t, group)

	// A second group is not a person view: alice's private records of the
	// first group are not reachable there.
	f.command.Event.Data.Conversation.OpenConversationID = "cid-other-group-" + uuid.NewString()
	other, oid, os := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "pv-2", Text: "忘掉我的周报格式", SenderOpenDingTalkID: "alice"}})
	_, err = other.Execute(ctx, oid, forgetV2(os[0], groupOnly))
	requireRefused(t, err, "no memory record")
	finishMemoryJob(t, other)

	f.command.Event.Data.Conversation.Type = "single"
	f.command.Event.Data.Conversation.OpenConversationID = "cid-dm-" + uuid.NewString()
	dm, did, ds := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "pv-3", Text: "忘掉我的周报格式偏好", SenderOpenDingTalkID: "alice"}, {OpenMsgID: "pv-4", Text: "也忘掉 alice 的", SenderOpenDingTalkID: "bob"}})
	if dm.job.Scope.SceneID == group.job.Scope.SceneID {
		t.Fatal("fixture did not open a DM scene")
	}
	_, err = dm.Execute(ctx, did, forgetV2(ds[1], captured.Receipt))
	requireRefused(t, err, "no memory record")
	result, err := dm.Execute(ctx, did, forgetV2(ds[0], captured.Receipt))
	if err != nil || memoryView(t, result).State != "forgotten" {
		t.Fatalf("person-view forget: %s %v", result.Content, err)
	}
	if _, state := memoryRow(t, captured.Receipt); state != "forgotten" {
		t.Fatalf("origin record state %s", state)
	}
	// Re-fence: once the origin scene no longer belongs to this tenant, the
	// person view cannot write into it.
	if _, err = testPool.Exec(ctx, `UPDATE agent_scene SET tenant_org_id='999' WHERE id=$1`, group.job.Scope.SceneID); err != nil {
		t.Fatal(err)
	}
	if _, err = dm.Execute(ctx, did, forgetV2(ds[0], refenced)); err == nil {
		t.Fatal("forgot a record of a scene outside the tenant")
	}
	if _, state := memoryRow(t, refenced); state != "active" {
		t.Fatalf("re-fence bypassed: %s", state)
	}
}

func TestEmployeeMemoryV2LookupBoundToSelectedSourceSpeaker(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "l-a", Text: "我记过什么 secret", SenderOpenDingTalkID: "alice"}, {OpenMsgID: "l-b", Text: "我的 secret 呢", SenderOpenDingTalkID: "bob"}})
	seed := func(scope employeememory.Scope, insight string, source employeememory.LearningSource) {
		t.Helper()
		if _, err := f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypeFact, Key: strings.ToLower(insight), Insight: insight, Source: source, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "seed-" + insight, EvidenceID: insight, ActorID: "seed"}); err != nil {
			t.Fatal(err)
		}
	}
	seed(employeeMemoryScope(host, sources[0]), "ALICE_SECRET", employeememory.LearningSourceObserved)
	seed(employeeMemoryScope(host, sources[1]), "BOB_SECRET", employeememory.LearningSourceObserved)
	seed(host.sceneScopeForTest(), "SHARED_SECRET", employeememory.LearningSourceObserved)
	seed(host.sceneScopeForTest(), "INFERRED_SECRET", employeememory.LearningSourceInferred)
	for _, tc := range []struct {
		source       employeeSourceMessage
		scope        string
		want, absent []string
	}{
		{sources[0], "me", []string{"ALICE_SECRET"}, []string{"BOB_SECRET", "SHARED_SECRET"}},
		{sources[1], "me", []string{"BOB_SECRET"}, []string{"ALICE_SECRET", "SHARED_SECRET"}},
		{sources[1], "scene", []string{"SHARED_SECRET"}, []string{"ALICE_SECRET", "BOB_SECRET", "INFERRED_SECRET"}},
	} {
		result, err := host.Execute(ctx, id, lookupV2(tc.source, tc.scope, "secret"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(result.Content, want) {
				t.Fatalf("%s lookup lacks %s: %s", tc.scope, want, result.Content)
			}
		}
		for _, absent := range tc.absent {
			if strings.Contains(result.Content, absent) {
				t.Fatalf("%s lookup leaked %s: %s", tc.scope, absent, result.Content)
			}
		}
	}
}

func TestEmployeeMemoryV2GroupResetClearsOnlySendersItems(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	seedHost, _, seedSources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: "seed", Text: "hello", SenderOpenDingTalkID: "alice"}})
	aliceFact := seedSceneStatement(t, f, seedHost, v2Alice, v2Director, "reset-a", "周报周五交", "周报")
	bobFact := seedSceneStatement(t, f, seedHost, v2Bob, v2Director, "reset-b", "例会周一开", "例会")
	private := func(principal, insight string) string {
		scope := employeeMemoryScope(seedHost, seedSources[0])
		scope.PrincipalID = principal
		rec, err := f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "p", Insight: insight, Source: employeememory.LearningSourceObserved, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "seed-" + insight, EvidenceID: insight, ActorID: principal})
		if err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	alicePrivate, bobPrivate := private(v2Alice, "ALICE_PRIVATE"), private(v2Bob, "BOB_PRIVATE")
	finishMemoryJob(t, seedHost)
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "reset-cmd", Text: "/reset-memory", SenderOpenDingTalkID: "alice"}}
	if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != http.StatusAccepted {
		t.Fatal(r.Body.String())
	}
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		return nil, errors.New("reset must not call the model")
	})
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{aliceFact.Record.ID: "forgotten", alicePrivate: "forgotten", bobFact.Record.ID: "active", bobPrivate: "active"} {
		if _, state := memoryRow(t, id); state != want {
			t.Fatalf("record %s is %s, want %s", id, state, want)
		}
	}
	var outcome string
	if err := testPool.QueryRow(ctx, `SELECT outcome::text FROM employee_scene_job WHERE agent_id=$1 AND outcome::text LIKE '%已清理%'`, f.agentID).Scan(&outcome); err != nil || !strings.Contains(outcome, "管理页") {
		t.Fatalf("reset reply: %s %v", outcome, err)
	}
	var resetAt *time.Time
	if err := testPool.QueryRow(ctx, `SELECT reset_at FROM employee_memory_state WHERE agent_id=$1 AND scope_kind='scene'`, f.agentID).Scan(&resetAt); err != nil || resetAt != nil {
		t.Fatalf("group reset moved the shared reset fence: %v %v", resetAt, err)
	}
}

func TestEmployeeMemoryV2TwoConnectionConcurrentSceneCapture(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	ctx := context.Background()
	host, id, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{
		{OpenMsgID: "cc-1", Text: "@员工 记一下：周报周五交", SenderOpenDingTalkID: "alice", SenderDisplayName: "Alice"},
		{OpenMsgID: "cc-2", Text: "@员工 记一下：例会周一", SenderOpenDingTalkID: "alice", SenderDisplayName: "Alice"},
	})
	flush := func(messageID, text, subject string) employeememory.SceneFactInput {
		return employeememory.SceneFactInput{Type: employeememory.LearningTypeFact, Subject: subject, Quote: text, Origin: employeememory.CaptureOriginFlush, ActorID: "employee-flush:" + f.agentID, Grounding: employeememory.SceneFactGrounding{MessageID: messageID, Text: text, SpeakerRef: v2Director, SpeakerName: "Director", SpeakerClass: "human", SaidAt: time.Now().UTC().Add(-time.Second)}}
	}
	race := func(call employeeloop.ToolCall, in employeememory.SceneFactInput) (employeeloop.ToolResult, employeememory.SceneEntry) {
		t.Helper()
		var wg sync.WaitGroup
		var hostResult employeeloop.ToolResult
		var entry employeememory.SceneEntry
		var hostErr, flushErr error
		start := make(chan struct{})
		conn, err := testPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		wg.Add(2)
		go func() { defer wg.Done(); <-start; hostResult, hostErr = host.Execute(ctx, id, call) }()
		go func() {
			defer wg.Done()
			<-start
			tx, err := conn.Begin(ctx)
			if err != nil {
				flushErr = err
				return
			}
			defer tx.Rollback(ctx)
			if entry, flushErr = f.h.EmployeeMemory.UpsertSceneFactTx(ctx, tx, host.sceneScopeForTest(), in); flushErr == nil {
				flushErr = tx.Commit(ctx)
			}
		}()
		close(start)
		wg.Wait()
		if hostErr != nil || flushErr != nil {
			t.Fatalf("host=%v flush=%v", hostErr, flushErr)
		}
		return hostResult, entry
	}
	// Different messages, one subject: both statements stay, one conflicts.
	captured, flushed := race(captureV2(sources[0], "scene", "fact", "周报截止", "周报周五交", ""), flush("dws-other", "周报周四交", "周报截止"))
	hostRec, _ := memoryRow(t, captured.Receipt)
	flushRec, _ := memoryRow(t, flushed.Record.ID)
	if (hostRec.ConflictsWith == "") == (flushRec.ConflictsWith == "") {
		t.Fatalf("exactly one write must name the other: %q %q", hostRec.ConflictsWith, flushRec.ConflictsWith)
	}
	// The same provider message on two connections: one record.
	second, again := race(captureV2(sources[1], "scene", "fact", "例会", "例会周一", ""), flush("cc-2", "@员工 记一下：例会周一", "例会时间"))
	if second.Receipt != again.Record.ID {
		t.Fatalf("one message became two records: %s %s", second.Receipt, again.Record.ID)
	}
	if n := memoryRowCount(t, f); n != 3 {
		t.Fatalf("rows=%d", n)
	}
	_ = scene.KindGroup
}

func TestEmployeeMemoryLookupSkipsInferredRunCandidates(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	host, id, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "inferred-lookup", Text: "例行任务怎么样", SenderOpenDingTalkID: "requester-open-id"}})
	scope := employeeMemoryScope(host, source)
	for _, seed := range []struct {
		key, insight string
		source       employeememory.LearningSource
	}{{"run-8a29fc67", "Unverified execution candidate: 例行任务已创建好了", employeememory.LearningSourceInferred}, {"routine-note", "例行任务每小时汇报", employeememory.LearningSourceObserved}} {
		if _, err := f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypeOperational, Key: seed.key, Insight: seed.insight, Source: seed.source, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "seed-" + seed.key, EvidenceID: seed.key, ActorID: source.RequesterRef}); err != nil {
			t.Fatal(err)
		}
	}
	v1, err := host.Execute(ctx, id, employeeloop.ToolCall{Name: "memory_lookup", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "query": "例行任务"}})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := host.Execute(ctx, id, lookupV2(source, "me", "例行任务"))
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{v1.Content, v2.Content} {
		if strings.Contains(content, "Unverified execution candidate") || strings.Contains(content, "run-8a29fc67") || !strings.Contains(content, "例行任务每小时汇报") {
			t.Fatalf("lookup returned run candidates or lost stated memory: %s", content)
		}
	}
}
