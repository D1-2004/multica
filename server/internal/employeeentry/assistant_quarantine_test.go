package employeeentry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Production /reset-memory can complete with zero model attempts and no
// input_snapshot. An old acknowledgement must not hide new human material.
func TestRecentConversationQuarantinesUnknownResetAssistantOnly(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	ctx, scope := context.Background(), f.admission.Scope
	principal := f.admission.Item.PrincipalID
	receipt, resetJob := recentHistoryInput(t, f, scope, principal, "/reset-memory", "reset-command", before.Add(-20*time.Minute))
	historyReplyJob(t, f, resetJob, nil, map[string]any{"host:reset-memory:fixture": map[string]any{"input": map[string]any{}, "result": map[string]any{"reply": "已清理"}}}, before.Add(-20*time.Minute))
	recentHistoryMemoryEvidence(t, f, scope, "dingtalk:org-a:open_id:source-open", receipt, "older-retired-source", "forgotten")
	recentHistoryReply(t, f, scope, resetJob, "delivered", "不可信旧助理值", "reset-ack", "cid-test", before.Add(-19*time.Minute))
	var action string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM response_action WHERE provider_message_id='reset-ack'`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	_, child := recentHistoryInput(t, f, scope, principal, "另一个问题", "child-question", before.Add(-10*time.Minute))
	prior, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{{Role: "assistant", ActionID: action, MessageID: "reset-ack"}}})
	historyReplyJob(t, f, child, map[string]any{"input": map[string]any{"RecentConversation": string(prior)}}, map[string]any{}, before.Add(-10*time.Minute))
	recentHistoryReply(t, f, scope, child, "delivered", "旧助理的依赖后继", "dependent-reply", "cid-test", before.Add(-9*time.Minute))
	recentHistoryInput(t, f, scope, principal, "新的真人约定：周二 17 点", "fresh-human", before.Add(-time.Minute))
	got, err := f.store.RecentConversation(ctx, RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
	if err != nil {
		t.Fatalf("unknown assistant removed all trusted human history: %v", err)
	}
	raw := historyTexts(t, got)
	if strings.Contains(raw, "reset-ack") || strings.Contains(raw, "dependent-reply") || !strings.Contains(raw, "fresh-human") {
		t.Fatalf("unsafe assistant or lost fresh human: %s", raw)
	}
	evidence, err := f.store.SceneTranscriptEvidence(ctx, scope, before.Add(-TranscriptWindow), before)
	if err != nil {
		t.Fatalf("unknown assistant disabled trusted DWS transcript evidence: %v", err)
	}
	bounds := transcriptBounds(before)
	bounds.Org, bounds.Evidence = scope.TenantOrgID, evidence
	transcript := BuildSceneTranscript([]TranscriptSource{{ID: "new-plain-human", SentAt: before.Add(-30 * time.Second), SenderOpenID: "director", Content: "同值的新真人约定：周二 17 点"}}, transcriptReader, bounds)
	if len(transcript.Lines) != 1 || transcript.Lines[0].MessageID != "new-plain-human" {
		t.Fatalf("independent current provider human disappeared: %+v", transcript)
	}
}

func TestAssistantQuarantineCannotWithdrawIndependentHumanReferenceID(t *testing.T) {
	f, request, action := crossWindowReplies(t, false)
	ctx := context.Background()
	var recentJob string
	if err := f.pool.QueryRow(ctx, `SELECT input->>'scene_notice_id' FROM response_action WHERE provider_message_id='recent-B'`).Scan(&recentJob); err != nil {
		t.Fatal(err)
	}
	prior, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{{Role: "assistant", ActionID: action, MessageID: "independent-human-provider-id"}}})
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=jsonb_build_object('input',jsonb_build_object('RecentConversation',$2::text)) WHERE id=$1::uuid`, recentJob, string(prior)); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.RecentConversation(ctx, request)
	if err != nil || strings.Contains(historyTexts(t, got), "recent-B") || !strings.Contains(historyTexts(t, got), "independent-C") {
		t.Fatalf("bad assistant pair was not isolated: %+v %v", got, err)
	}
	evidence, err := f.store.SceneTranscriptEvidence(ctx, request.Scope, request.Before.Add(-TranscriptWindow), request.Before)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.WithdrawnEvidenceIDs["independent-human-provider-id"] {
		t.Fatal("unverified assistant reference granted a human tombstone")
	}
	bounds := transcriptBounds(request.Before)
	bounds.Org, bounds.Evidence = request.Scope.TenantOrgID, evidence
	transcript := BuildSceneTranscript([]TranscriptSource{{ID: "independent-human-provider-id", SentAt: request.Before.Add(-time.Minute), SenderOpenID: "different-member", Content: "独立真人仍在"}}, transcriptReader, bounds)
	if len(transcript.Lines) != 1 {
		t.Fatalf("independent human was erased by another node's invalid pair: %+v", transcript)
	}
}

func TestUnknownOldJobWithWindowDeliveryIsReboundAndOmitted(t *testing.T) {
	f, request, action := crossWindowReplies(t, false)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=NULL WHERE id::text=(SELECT input->>'scene_notice_id' FROM response_action WHERE id=$1)`, action); err != nil {
		t.Fatal(err)
	}
	// Outside both the 24-hour dialogue and 72-hour transcript job window.
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET created_at=$2 WHERE id::text=(SELECT input->>'scene_notice_id' FROM response_action WHERE id=$1)`, action, request.Before.Add(-76*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var oldJob string
	if err := f.pool.QueryRow(ctx, `SELECT input->>'scene_notice_id' FROM response_action WHERE id=$1`, action).Scan(&oldJob); err != nil {
		t.Fatal(err)
	}
	if err := RecordHostNotice(ctx, f.pool, HostNotice{ActionID: action, Scope: request.Scope, PrincipalID: request.PrincipalID, SourceKind: HostNoticeTaskWake, SourceID: oldJob}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE response_action SET updated_at=$2 WHERE id=$1`, action, request.Before.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.RecentConversation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	raw := historyTexts(t, got)
	if strings.Contains(raw, "old-A") || strings.Contains(raw, "recent-B") || !strings.Contains(raw, "independent-C") {
		t.Fatalf("window action retained an unbound unsafe old source: %s", raw)
	}
	evidence, err := f.store.SceneTranscriptEvidence(ctx, request.Scope, request.Before.Add(-TranscriptWindow), request.Before)
	if err != nil {
		t.Fatal(err)
	}
	bounds := transcriptBounds(request.Before)
	bounds.Org, bounds.Evidence = request.Scope.TenantOrgID, evidence
	transcript := BuildSceneTranscript([]TranscriptSource{{ID: "current-human-quote", SentAt: request.Before.Add(-30 * time.Second), SenderOpenID: "other-member", Content: "独立的真人材料", QuotedID: "old-A", QuotedContent: "旧值不该复活"}}, transcriptReader, bounds)
	if len(transcript.Lines) != 1 || transcript.Lines[0].Quoted != "" {
		t.Fatalf("unsafe old action reentered through DWS quote: %+v", transcript)
	}
}
