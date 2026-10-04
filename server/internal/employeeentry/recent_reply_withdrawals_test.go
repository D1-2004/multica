package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
)

func historyReplyJob(t *testing.T, f fixture, job string, snapshot, journal any, at time.Time) {
	t.Helper()
	scope := f.admission.Scope
	input, _ := json.Marshal(snapshot)
	tools, _ := json.Marshal(journal)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO employee_scene_job(id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,message_count,state,input_snapshot,tool_journal,created_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,'[]',1,'completed',$7::jsonb,$8::jsonb,$9)`, job, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, f.admission.Item.PrincipalID, input, tools, at); err != nil {
		t.Fatal(err)
	}
}

// M5: a later answer reads an existing record, rather than capturing it.
// Both the actual delivered answer and a following answer grounded in that
// exact history action must disappear after withdrawal, without value matching.
func TestRecentConversationWithdrawsDeliveredMemoryReadReplies(t *testing.T) {
	for _, provenance := range []string{"manifest", "lookup"} {
		t.Run(provenance, func(t *testing.T) {
			f, before := recentHistoryDatabase(t)
			ctx := context.Background()
			scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
			recordID := uuid.NewString()
			author := "dingtalk:" + scope.TenantOrgID + ":open_id:source-open"
			record, _ := json.Marshal(map[string]any{"id": recordID, "created_by": author, "speaker_ref": author, "source_id": "dingtalk-message:" + scope.SceneID, "evidence_id": "original-fact", "insight": "周二 17 点"})
			if _, err := f.pool.Exec(ctx, `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,'scene','',$6,$7::jsonb)`, recordID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, strings.Repeat("a", 64), record); err != nil {
				t.Fatal(err)
			}
			_, job := recentHistoryInput(t, f, scope, principal, "周报什么时候交？", "later-question", before.Add(-10*time.Minute))
			snapshot, journal := map[string]any{}, map[string]any{}
			if provenance == "manifest" {
				snapshot["memory_manifest"] = []any{map[string]any{"id": recordID, "label": "m1", "scope": "scene", "scene_id": scope.SceneID}}
			} else {
				journal["lookup"] = map[string]any{"input": map[string]any{"name": "memory_lookup"}, "result": map[string]any{"result": map[string]any{"Content": `{"records":[{"record_ref":"` + recordID + `","text":"周二 17 点"}]}`}}}
			}
			historyReplyJob(t, f, job, snapshot, journal, before.Add(-10*time.Minute))
			recentHistoryReply(t, f, scope, job, "delivered", "周报周二 17 点交", "memory-answer", "cid-test", before.Add(-9*time.Minute))
			var actionID string
			if err := f.pool.QueryRow(ctx, `SELECT id FROM response_action WHERE provider_message_id='memory-answer'`).Scan(&actionID); err != nil {
				t.Fatal(err)
			}
			_, followJob := recentHistoryInput(t, f, scope, principal, "按你上面说的是什么时候？", "follow-question", before.Add(-8*time.Minute))
			history, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{{Role: "assistant", ActionID: actionID, MessageID: "memory-answer", Text: "周报周二 17 点交"}}})
			historyReplyJob(t, f, followJob, map[string]any{"input": map[string]any{"RecentConversation": string(history)}}, map[string]any{}, before.Add(-8*time.Minute))
			recentHistoryReply(t, f, scope, followJob, "delivered", "对，周二 17 点", "follow-answer", "cid-test", before.Add(-7*time.Minute))
			independentReceipt, independent := recentHistoryInput(t, f, scope, principal, "我自己的项目也是周二 17 点，这条要保留", "other-member-fact", before.Add(-6*time.Minute))
			if _, err := f.pool.Exec(ctx, `UPDATE employee_event_consumption SET payload=jsonb_set(payload,'{command,event,data,messages,0,senderOpenDingTalkId}','"different-member"'::jsonb) WHERE receipt_id=$1::uuid`, independentReceipt); err != nil {
				t.Fatal(err)
			}
			historyReplyJob(t, f, independent, map[string]any{"input": map[string]any{"Memory": ""}}, map[string]any{}, before.Add(-6*time.Minute))
			recentHistoryReply(t, f, scope, independent, "delivered", "另一项目周二 17 点", "independent-answer", "cid-test", before.Add(-5*time.Minute))
			request := RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before}
			active, err := f.store.RecentConversation(ctx, request)
			if err != nil || !strings.Contains(historyTexts(t, active), "memory-answer") || !strings.Contains(historyTexts(t, active), "follow-answer") {
				t.Fatalf("active precondition: %+v %v", active, err)
			}
			frozen, _ := json.Marshal(active)
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			memoryScope := employeememory.Scope{WorkspaceID: util.MustParseUUID(scope.WorkspaceID), AgentID: util.MustParseUUID(scope.AgentID), TenantOrgID: scope.TenantOrgID, Scene: scene.Ref{SceneID: scope.SceneID}, Kind: employeememory.ScopeScene}
			forgotten, err := employeememory.NewStore(f.pool).ForgetSceneTx(ctx, tx, memoryScope, recordID, author)
			if err != nil || forgotten.State != "forgotten" {
				_ = tx.Rollback(ctx)
				t.Fatalf("authorized author forget failed: %+v %v", forgotten, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.RecentConversation(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			texts := historyTexts(t, got)
			if strings.Contains(texts, "memory-answer") || strings.Contains(texts, "follow-answer") || !got.WithdrawnMemoryEvidenceOmitted {
				t.Fatalf("withdrawn record revived through delivered replies: %s", texts)
			}
			if !strings.Contains(texts, "other-member-fact") || !strings.Contains(texts, "independent-answer") || !strings.Contains(texts, "周报什么时候交") {
				t.Fatalf("independent same-value evidence or question erased: %s", texts)
			}
			bounds := transcriptBounds(before)
			bounds.Org = scope.TenantOrgID
			bounds.Evidence, err = f.store.SceneTranscriptEvidence(ctx, scope, before.Add(-TranscriptWindow), before)
			if err != nil {
				t.Fatal(err)
			}
			transcript := BuildSceneTranscript([]TranscriptSource{{ID: "new-independent-quote", SentAt: before.Add(-time.Minute), SenderOpenID: "different-member", Content: "这件事另外再聊", QuotedID: "memory-answer", QuotedContent: "周报周二 17 点交"}}, transcriptReader, bounds)
			if len(transcript.Lines) != 1 || transcript.Lines[0].Quoted != "" || !transcript.WithdrawnEvidenceOmitted {
				t.Fatalf("provider quote revived a withdrawn assistant reply: %+v", transcript)
			}
			var original, storedSnapshot string
			if err := f.pool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE provider_message_id='memory-answer'`).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if err := f.pool.QueryRow(ctx, `SELECT input_snapshot->'input'->>'RecentConversation' FROM employee_scene_job WHERE id=$1::uuid`, followJob).Scan(&storedSnapshot); err != nil {
				t.Fatal(err)
			}
			if original != "周报周二 17 点交" || !strings.Contains(storedSnapshot, "memory-answer") || !strings.Contains(string(frozen), "memory-answer") {
				t.Fatal("withdrawal modified audit or already-frozen input")
			}
		})
	}
}

func TestRecentConversationMemoryReplyProvenanceRejectsFalseAssociations(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	ctx := context.Background()
	scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
	retired, foreign := uuid.NewString(), uuid.NewString()
	for id, org := range map[string]string{retired: scope.TenantOrgID, foreign: "another-org"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record,forgotten_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,'scene','',$6,'{}',now())`, id, scope.WorkspaceID, scope.AgentID, org, scope.SceneID, strings.Repeat(id[:1], 64)); err != nil {
			t.Fatal(err)
		}
	}
	for index, kind := range []string{"foreign-record", "refused-lookup", "text-is-not-provenance"} {
		_, job := recentHistoryInput(t, f, scope, principal, "普通问题", kind, before.Add(-time.Duration(10-index)*time.Minute))
		snapshot, journal := map[string]any{"input": map[string]any{"Memory": ""}}, map[string]any{}
		switch kind {
		case "foreign-record":
			snapshot["memory_manifest"] = []any{map[string]any{"id": foreign, "scope": "scene", "scene_id": scope.SceneID}}
		case "refused-lookup":
			journal["lookup"] = map[string]any{"input": map[string]any{"name": "memory_lookup"}, "result": map[string]any{"refused": true, "failure": "not allowed", "result": map[string]any{"Content": `{"records":[{"record_ref":"` + retired + `"}]}`}}}
		case "text-is-not-provenance":
			journal["lookup"] = map[string]any{"input": map[string]any{"name": "memory_lookup"}, "result": map[string]any{"result": map[string]any{"Content": `{"text":"` + retired + `"}`}}}
		}
		historyReplyJob(t, f, job, snapshot, journal, before.Add(-time.Duration(10-index)*time.Minute))
		recentHistoryReply(t, f, scope, job, "delivered", "KEEP_"+kind, "reply-"+kind, "cid-test", before.Add(-time.Duration(9-index)*time.Minute))
	}
	got, err := f.store.RecentConversation(ctx, RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KEEP_foreign-record", "KEEP_refused-lookup", "KEEP_text-is-not-provenance"} {
		if !strings.Contains(historyTexts(t, got), want) {
			t.Fatalf("false record association erased %s: %+v", want, got)
		}
	}
	if got.WithdrawnMemoryEvidenceOmitted {
		t.Fatal("unrelated tombstone claimed a withdrawal")
	}
}

// A truncated tombstone set cannot establish that a reply is safe to expose.
func TestRecentConversationWithdrawalBoundIsUnavailable(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	scope := f.admission.Scope
	_, err := f.pool.Exec(context.Background(), `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record,forgotten_at)
 SELECT gen_random_uuid(),$1::uuid,$2::uuid,$3,$4::uuid,'scene','',repeat('b',64),'{}'::jsonb,now() FROM generate_series(1,$5)`,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, transcriptEvidenceCap+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: f.admission.Item.PrincipalID, Before: before}); !errors.Is(err, errTranscriptEvidenceBound) {
		t.Fatalf("partial withdrawal set became available history: %v", err)
	}
}
