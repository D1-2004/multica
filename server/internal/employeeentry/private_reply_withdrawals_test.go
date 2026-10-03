package employeeentry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDMWithdrawsExactOwnersCrossOriginPrivateReply(t *testing.T) {
	for _, provenance := range []string{"manifest", "lookup", "deleted-origin"} {
		t.Run(provenance, func(t *testing.T) {
			f, before := recentHistoryDatabase(t)
			ctx := context.Background()
			origin := f.admission.Scope
			owner := "dingtalk:" + origin.TenantOrgID + ":open_id:source-open"
			dmDirectory, err := scene.Resolve(ctx, db.New(f.pool), scene.Owner{WorkspaceID: util.MustParseUUID(origin.WorkspaceID), AgentID: util.MustParseUUID(origin.AgentID)}, scene.DingTalkConversation(origin.TenantOrgID, scene.KindDM, "cid-dm-"+uuid.NewString()), scene.Observation{KindStated: true})
			if err != nil {
				t.Fatal(err)
			}
			dm := origin
			dm.SceneID = util.UUIDToString(dmDirectory.ID)
			f.admission.Scope = dm
			store := employeememory.NewStore(f.pool)
			private := employeememory.Scope{WorkspaceID: util.MustParseUUID(origin.WorkspaceID), AgentID: util.MustParseUUID(origin.AgentID), TenantOrgID: origin.TenantOrgID, Scene: scene.Ref{SceneID: origin.SceneID}, Kind: employeememory.ScopePrivate, PrincipalID: owner}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := store.RecordPrivateObservationTx(ctx, tx, private, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "p-private-drink", Subject: "饮品偏好", Insight: "本人偏好薄荷茶", Source: employeememory.LearningSourceObserved, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "employee-message:" + uuid.NewString(), EvidenceID: "foreign-evidence-matches-current-human", ActorID: owner, OccurredAt: before.Add(-20 * time.Minute), HumanStated: true})
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			tx, err = f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			other, err := store.RecordPrivateObservationTx(ctx, tx, private, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "p-independent-drink", Subject: "另一来源饮品", Insight: "本人偏好薄荷茶", Source: employeememory.LearningSourceObserved, Confidence: 4}, employeememory.TrustedEvidence{SourceID: "employee-message:" + uuid.NewString(), EvidenceID: "independent-group-record", ActorID: owner, OccurredAt: before.Add(-19 * time.Minute), HumanStated: true})
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			brief, err := store.ForegroundBrief(ctx, employeememory.ForegroundRequest{Scene: employeememory.Scope{WorkspaceID: private.WorkspaceID, AgentID: private.AgentID, TenantOrgID: private.TenantOrgID, Scene: scene.Ref{SceneID: dm.SceneID}, Kind: employeememory.ScopeScene}, SceneKind: scene.KindDM, Requester: owner, Query: "饮品偏好"})
			if err != nil || !brief.Stats.PersonView || !strings.Contains(brief.Text, "薄荷茶") {
				t.Fatalf("person-view precondition: %+v %v", brief, err)
			}
			principal := f.admission.Item.PrincipalID
			_, job := recentHistoryInput(t, f, dm, principal, "我偏好什么？", "private-question", before.Add(-10*time.Minute))
			snapshot, journal := map[string]any{}, map[string]any{}
			if provenance == "lookup" {
				snapshot["memory_manifest"] = []any{}
				journal["lookup"] = map[string]any{"input": map[string]any{"name": "memory_lookup"}, "result": map[string]any{"result": map[string]any{"Content": `{"records":[{"record_ref":"` + rec.ID + `"}]}`}}}
			} else {
				snapshot["memory_manifest"] = brief.Manifest
			}
			historyReplyJob(t, f, job, snapshot, journal, before.Add(-10*time.Minute))
			reply := func(job, text, id string, at time.Time) {
				recentHistoryReply(t, f, dm, job, "delivered", text, id, dmDirectory.ExternalSceneID, at)
				if _, err := f.pool.Exec(ctx, `UPDATE response_action SET input=jsonb_set(input,'{conversation_id}',to_jsonb($2::text)) WHERE provider_message_id=$1`, id, dmDirectory.ExternalSceneID); err != nil {
					t.Fatal(err)
				}
			}
			reply(job, "旧本人偏好薄荷茶", "cross-private-answer", before.Add(-9*time.Minute))
			var action string
			if err := f.pool.QueryRow(ctx, `SELECT id FROM response_action WHERE provider_message_id='cross-private-answer'`).Scan(&action); err != nil {
				t.Fatal(err)
			}
			_, child := recentHistoryInput(t, f, dm, principal, "按上面说的？", "derived-question", before.Add(-8*time.Minute))
			history, _ := json.Marshal(RecentConversation{Messages: []RecentConversationMessage{{Role: "assistant", ActionID: action, MessageID: "cross-private-answer"}}})
			historyReplyJob(t, f, child, map[string]any{"input": map[string]any{"RecentConversation": string(history)}}, map[string]any{}, before.Add(-8*time.Minute))
			reply(child, "派生答复薄荷茶", "derived-private-answer", before.Add(-7*time.Minute))
			_, independent := recentHistoryInput(t, f, dm, principal, "另一条独立说明也是薄荷茶", "foreign-evidence-matches-current-human", before.Add(-6*time.Minute))
			historyReplyJob(t, f, independent, map[string]any{"memory_manifest": []any{}}, map[string]any{}, before.Add(-6*time.Minute))
			reply(independent, "独立来源薄荷茶", "independent-answer", before.Add(-5*time.Minute))
			request := RecentConversationRequest{Scope: dm, PrincipalID: principal, MemoryPrincipal: owner, Before: before}
			active, err := f.store.RecentConversation(ctx, request)
			if err != nil || !strings.Contains(historyTexts(t, active), "cross-private-answer") {
				t.Fatalf("active history missing: %+v %v", active, err)
			}
			tx, err = f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			entry, err := store.ForgetPrivateTx(ctx, tx, private, rec.ID)
			if err != nil || entry.State != "forgotten" {
				_ = tx.Rollback(ctx)
				t.Fatalf("author forget: %+v %v", entry, err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			tx, err = f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := store.PrivateEntryTx(ctx, tx, private, other.ID)
			_ = tx.Rollback(ctx)
			if err != nil || retained.State != "active" || retained.Record.Insight != "本人偏好薄荷茶" {
				t.Fatalf("same-actor same-origin independent record changed: %+v %v", retained, err)
			}
			if provenance == "deleted-origin" {
				if _, err := f.pool.Exec(ctx, `DELETE FROM agent_scene WHERE id=$1::uuid`, origin.SceneID); err != nil {
					t.Fatal(err)
				}
			}
			got, err := f.store.RecentConversation(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			text := historyTexts(t, got)
			if strings.Contains(text, "cross-private-answer") || strings.Contains(text, "derived-private-answer") || !strings.Contains(text, "independent-answer") || !strings.Contains(text, "foreign-evidence-matches-current-human") {
				t.Fatalf("cross-source withdrawal wrong: %s", text)
			}
			var original string
			if err := f.pool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE provider_message_id='cross-private-answer'`).Scan(&original); err != nil || original != "旧本人偏好薄荷茶" {
				t.Fatalf("audit changed: %s %v", original, err)
			}
			if err := f.pool.QueryRow(ctx, `SELECT input_snapshot->'input'->>'RecentConversation' FROM employee_scene_job WHERE id=$1::uuid`, child).Scan(&original); err != nil || !strings.Contains(original, "cross-private-answer") {
				t.Fatal("frozen child changed", err)
			}
		})
	}
}

func TestDMCrossOriginWithdrawalNeverWidensOwnerOrPublicScope(t *testing.T) {
	for _, kind := range []string{"other-owner", "no-owner", "staff-only", "external-public", "other-tenant", "other-agent", "other-workspace", "group-view", "wrong-manifest-origin"} {
		t.Run(kind, func(t *testing.T) {
			f, before := recentHistoryDatabase(t)
			ctx := context.Background()
			origin := f.admission.Scope
			owner := "dingtalk:" + origin.TenantOrgID + ":open_id:source-open"
			dmRow, err := scene.Resolve(ctx, db.New(f.pool), scene.Owner{WorkspaceID: util.MustParseUUID(origin.WorkspaceID), AgentID: util.MustParseUUID(origin.AgentID)}, scene.DingTalkConversation(origin.TenantOrgID, scene.KindDM, "cid-dm-neg-"+uuid.NewString()), scene.Observation{KindStated: true})
			if err != nil {
				t.Fatal(err)
			}
			dm := origin
			dm.SceneID = util.UUIDToString(dmRow.ID)
			f.admission.Scope = dm
			record := uuid.NewString()
			ws, agent, tenant, recordOwner, recordKind := origin.WorkspaceID, origin.AgentID, origin.TenantOrgID, owner, "private"
			principalForView := owner
			switch kind {
			case "other-owner":
				recordOwner = "dingtalk:" + tenant + ":open_id:another"
			case "no-owner":
				principalForView = ""
			case "staff-only":
				principalForView = "dingtalk:" + tenant + ":staff_id:source-open"
			case "external-public":
				recordKind = "scene"
				recordOwner = ""
			case "other-tenant":
				tenant = "different-org"
			case "other-agent":
				agent = uuid.NewString()
			case "other-workspace":
				ws = uuid.NewString()
			case "group-view":
				if _, err := f.pool.Exec(ctx, `UPDATE agent_scene SET scene_kind='group' WHERE id=$1`, dmRow.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record,forgotten_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7,$8,'{}'::jsonb,now())`, record, ws, agent, tenant, origin.SceneID, recordKind, recordOwner, strings.Repeat("f", 64)); err != nil {
				t.Fatal(err)
			}
			principal := f.admission.Item.PrincipalID
			_, job := recentHistoryInput(t, f, dm, principal, "只按我这里的来源回答", "independent-source", before.Add(-2*time.Minute))
			manifestScene := origin.SceneID
			if kind == "wrong-manifest-origin" {
				manifestScene = dm.SceneID
			}
			historyReplyJob(t, f, job, map[string]any{"memory_manifest": []any{map[string]any{"id": record, "scene_id": manifestScene}}}, map[string]any{}, before.Add(-2*time.Minute))
			recentHistoryReply(t, f, dm, job, "delivered", "KEEP independent-source", "independent-result", dmRow.ExternalSceneID, before.Add(-time.Minute))
			if _, err := f.pool.Exec(ctx, `UPDATE response_action SET input=jsonb_set(input,'{conversation_id}',to_jsonb($1::text)) WHERE provider_message_id='independent-result'`, dmRow.ExternalSceneID); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.RecentConversation(ctx, RecentConversationRequest{Scope: dm, PrincipalID: principal, MemoryPrincipal: principalForView, Before: before})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(historyTexts(t, got), "independent-result") {
				t.Fatalf("cross-source query widened %s: %+v", kind, got)
			}
		})
	}
}
