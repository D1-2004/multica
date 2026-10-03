package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
)

func crossWindowReplies(t *testing.T, messageOnly bool) (fixture, RecentConversationRequest, string) {
	t.Helper()
	f, before := recentHistoryDatabase(t)
	ctx, scope := context.Background(), f.admission.Scope
	principal, recordID := f.admission.Item.PrincipalID, uuid.NewString()
	author := "dingtalk:" + scope.TenantOrgID + ":open_id:source-open"
	record, _ := json.Marshal(map[string]any{"id": recordID, "created_by": author, "source_id": "dingtalk-message:" + scope.SceneID, "evidence_id": "old-original-fact", "insight": "周二 17 点"})
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,'scene','',$6,$7::jsonb)`, recordID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, strings.Repeat("c", 64), record); err != nil {
		t.Fatal(err)
	}
	_, oldJob := recentHistoryInput(t, f, scope, principal, "原始问题", "old-question", before.Add(-26*time.Hour))
	historyReplyJob(t, f, oldJob, map[string]any{"memory_manifest": []any{map[string]any{"id": recordID, "scene_id": scope.SceneID}}}, map[string]any{}, before.Add(-26*time.Hour))
	recentHistoryReply(t, f, scope, oldJob, "delivered", "旧 A：周二 17 点", "old-A", "cid-test", before.Add(-25*time.Hour))
	var oldAction string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM response_action WHERE provider_message_id='old-A'`).Scan(&oldAction); err != nil {
		t.Fatal(err)
	}
	_, recentJob := recentHistoryInput(t, f, scope, principal, "按之前说的什么时候？", "recent-question", before.Add(-5*time.Minute))
	ref := RecentConversationMessage{Role: "assistant", ActionID: oldAction, MessageID: "old-A", Text: "旧 A：周二 17 点"}
	if messageOnly {
		ref.ActionID = ""
	}
	history, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{ref}})
	historyReplyJob(t, f, recentJob, map[string]any{"input": map[string]any{"RecentConversation": string(history)}}, map[string]any{}, before.Add(-5*time.Minute))
	recentHistoryReply(t, f, scope, recentJob, "delivered", "B 又说：周二 17 点", "recent-B", "cid-test", before.Add(-4*time.Minute))
	_, independent := recentHistoryInput(t, f, scope, principal, "另一成员独立项目也在周二 17 点", "independent-question", before.Add(-3*time.Minute))
	historyReplyJob(t, f, independent, map[string]any{"input": map[string]any{"Memory": ""}}, map[string]any{}, before.Add(-3*time.Minute))
	recentHistoryReply(t, f, scope, independent, "delivered", "独立 C：周二 17 点", "independent-C", "cid-test", before.Add(-2*time.Minute))
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	memoryScope := employeememory.Scope{WorkspaceID: util.MustParseUUID(scope.WorkspaceID), AgentID: util.MustParseUUID(scope.AgentID), TenantOrgID: scope.TenantOrgID, Scene: scene.Ref{SceneID: scope.SceneID}, Kind: employeememory.ScopeScene}
	if _, err := employeememory.NewStore(f.pool).ForgetSceneTx(ctx, tx, memoryScope, recordID, author); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f, RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before}, oldAction
}

func TestRecentConversationClosesCrossWindowReplyAncestors(t *testing.T) {
	for _, messageOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "action-and-message", true: "message-only"}[messageOnly], func(t *testing.T) {
			f, request, _ := crossWindowReplies(t, messageOnly)
			got, err := f.store.RecentConversation(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			text := historyTexts(t, got)
			if strings.Contains(text, "recent-B") || !strings.Contains(text, "independent-C") || !got.WithdrawnMemoryEvidenceOmitted {
				t.Fatalf("cross-window A resurrected through B or independent C erased: %s", text)
			}
			var original string
			if err := f.pool.QueryRow(context.Background(), `SELECT input->>'text' FROM response_action WHERE provider_message_id='old-A'`).Scan(&original); err != nil || original != "旧 A：周二 17 点" {
				t.Fatalf("ancestor audit changed: %q %v", original, err)
			}
		})
	}
}

func TestRecentConversationUnclosedReplyAncestorIsUnavailable(t *testing.T) {
	for _, kind := range []string{"missing", "foreign", "not-delivered", "unknown-job", "unknown-snapshot", "mismatched-pair"} {
		t.Run(kind, func(t *testing.T) {
			f, request, action := crossWindowReplies(t, false)
			ctx := context.Background()
			var err error
			switch kind {
			case "missing":
				_, err = f.pool.Exec(ctx, `DELETE FROM response_action WHERE id=$1`, action)
			case "foreign":
				_, err = f.pool.Exec(ctx, `UPDATE response_action SET provider_conversation_id='another-cid' WHERE id=$1`, action)
			case "not-delivered":
				_, err = f.pool.Exec(ctx, `UPDATE response_action SET state='provider_accepted' WHERE id=$1`, action)
			case "unknown-job":
				_, err = f.pool.Exec(ctx, `UPDATE response_action SET input=jsonb_set(input,'{scene_notice_id}',to_jsonb($2::text)) WHERE id=$1`, action, uuid.NewString())
			case "unknown-snapshot":
				_, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=NULL WHERE id::text=(SELECT input->>'scene_notice_id' FROM response_action WHERE id=$1)`, action)
			case "mismatched-pair":
				_, err = f.pool.Exec(ctx, `UPDATE response_action SET provider_message_id='different-message' WHERE id=$1`, action)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.store.RecentConversation(ctx, request)
			if err != nil || strings.Contains(historyTexts(t, got), "recent-B") || !strings.Contains(historyTexts(t, got), "independent-C") {
				t.Fatalf("unclosed assistant leaked or erased independent history: %+v %v", got, err)
			}
		})
	}
}

func TestRecentConversationReplyAncestorDepthBound(t *testing.T) {
	f, request, _ := crossWindowReplies(t, false)
	ctx, scope := context.Background(), request.Scope
	var lastAction, lastMessage string
	for index := 0; index <= replyAncestorDepthLimit; index++ {
		id := "deep-" + strconv.Itoa(index)
		at := request.Before.Add(-27 * time.Hour)
		_, job := recentHistoryInput(t, f, scope, request.PrincipalID, "旧问题", id+"-question", at)
		snapshot := map[string]any{"memory_manifest": []any{}}
		if lastAction != "" {
			history, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{{Role: "assistant", ActionID: lastAction, MessageID: lastMessage}}})
			snapshot["input"] = map[string]any{"RecentConversation": string(history)}
		}
		historyReplyJob(t, f, job, snapshot, map[string]any{}, at)
		recentHistoryReply(t, f, scope, job, "delivered", "独立旧回复", id, "cid-test", at.Add(time.Minute))
		if err := f.pool.QueryRow(ctx, `SELECT id FROM response_action WHERE provider_message_id=$1`, id).Scan(&lastAction); err != nil {
			t.Fatal(err)
		}
		lastMessage = id
	}
	// Redirect B's exact dependency to a chain too deep to close safely.
	var recentJob string
	if err := f.pool.QueryRow(ctx, `SELECT input->>'scene_notice_id' FROM response_action WHERE provider_message_id='recent-B'`).Scan(&recentJob); err != nil {
		t.Fatal(err)
	}
	history, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{{Role: "assistant", ActionID: lastAction, MessageID: lastMessage}}})
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=jsonb_build_object('input',jsonb_build_object('RecentConversation',$2::text)) WHERE id=$1::uuid`, recentJob, string(history)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RecentConversation(ctx, request); !errors.Is(err, errTranscriptEvidenceBound) {
		t.Fatalf("partial deep ancestor graph became available: %v", err)
	}
}

func TestRecentConversationAncestorSnapshotNeedsExplicitStructure(t *testing.T) {
	emptyHistory, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{}})
	for _, tc := range []struct {
		name     string
		snapshot any
		known    bool
	}{
		{"unknown-nonempty-input", map[string]any{"input": map[string]any{"unrecognized": "old context"}}, false},
		{"legacy-plaintext-memory", map[string]any{"input": map[string]any{"Memory": "周二 17 点"}}, false},
		{"null-memory-is-not-empty", map[string]any{"input": map[string]any{"Memory": nil}}, false},
		{"null-counts-are-not-zero", map[string]any{"input": map[string]any{"Memory": "周二 17 点"}, "memory_stats": map[string]any{"pinned": nil, "retrieved": nil, "verified": nil}}, false},
		{"explicit-empty-memory", map[string]any{"input": map[string]any{"Memory": ""}}, true},
		{"structured-history-only", map[string]any{"input": map[string]any{"RecentConversation": string(emptyHistory)}}, true},
		{"current-zero-injection", map[string]any{"input": map[string]any{"Memory": "Host status; no matching records."}, "memory_stats": map[string]any{"pinned": 0, "retrieved": 0, "verified": 0}}, true},
		{"incomplete-stats", map[string]any{"input": map[string]any{"Memory": "周二 17 点"}, "memory_stats": map[string]any{"pinned": 0}}, false},
		{"unmanifested-records", map[string]any{"input": map[string]any{"Memory": "周二 17 点"}, "memory_stats": map[string]any{"pinned": 0, "retrieved": 1, "verified": 0}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, request, action := crossWindowReplies(t, false)
			raw, _ := json.Marshal(tc.snapshot)
			if _, err := f.pool.Exec(context.Background(), `UPDATE employee_scene_job SET input_snapshot=$2::jsonb WHERE id::text=(SELECT input->>'scene_notice_id' FROM response_action WHERE id=$1)`, action, raw); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.RecentConversation(context.Background(), request)
			if !tc.known {
				if err != nil || strings.Contains(historyTexts(t, got), "recent-B") || !strings.Contains(historyTexts(t, got), "independent-C") {
					t.Fatalf("unknown assistant leaked or erased independent history: %+v %v", got, err)
				}
				return
			}
			if err != nil || !strings.Contains(historyTexts(t, got), "recent-B") || !strings.Contains(historyTexts(t, got), "independent-C") {
				t.Fatalf("explicit supported structure became unavailable or was value-matched: %+v %v", got, err)
			}
		})
	}
}
