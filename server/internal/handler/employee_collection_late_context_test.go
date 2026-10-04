package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

func closedQuestionFixture(t *testing.T) (*collectionHarness, taskinput.Collection, taskinput.Invitation) {
	t.Helper()
	c := newCollectionHarness(t)
	c.seed()
	col := c.origin("问Carol本周签了几单", collectionParticipants[0])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm"})
	inv := c.invitations(col.ID)["Carol"]
	c.nativeCancel("取消这个收集，不用再汇总。")
	return c, col, inv
}

func TestCollectionClosedOwnFactsKeepAcceptToolUnavailable(t *testing.T) {
	c, col, inv := closedQuestionFixture(t)
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "late-cancelled-fact", text: "关于刚才问的签单：7单", quoted: "pm-Carol"})
	before := c.model.count()
	c.model.set(collectionQuiet)
	c.process()
	request := c.model.requests[before]
	if !strings.Contains(request, "closed_questions") || !strings.Contains(request, `\"state\":\"cancelled\"`) || !strings.Contains(request, "Do not collect, remind, forward, promise a summary") {
		t.Fatalf("cancelled own invitation not projected: %s", request)
	}
	if strings.Contains(request, "If the message actually answers") || strings.Contains(request, "short acknowledgement only after") || !strings.Contains(request, "closed_only") || !strings.Contains(request, "receiving a chat message alone is not a recording or memory-write effect") {
		t.Fatal("closed-only input mixed in active answer/acknowledgement instructions")
	}
	if strings.Contains(request, `"name":"accept_collection_input"`) || strings.Contains(request, "invitation_context") || strings.Contains(request, "SENTINEL-ORIGIN-NOTE-55") || strings.Contains(request, col.ID) || strings.Contains(request, inv.ID) {
		t.Fatal("closed facts granted an accept binding or leaked origin/object data")
	}
	if n := c.count(`SELECT count(*) FROM employee_task_input WHERE collection_id=$1::uuid`, col.ID); n != 0 {
		t.Fatal("late answer was consumed", n)
	}
	if n := c.count(`SELECT count(*) FROM employee_task_ready_intent WHERE collection_id=$1::uuid AND state IN ('pending','admitted')`, col.ID); n != 0 {
		t.Fatal("closed invitation revived ready work", n)
	}
}

// Clone domain-shaped local rows to put private decoys behind every scope edge.
// These fixtures never go through a provider or use a real actor/account.
func cloneClosedQuestion(t *testing.T, c *collectionHarness, col taskinput.Collection, inv taskinput.Invitation, scope employeeentry.Scope, participant, question string, at time.Time, delivered bool) string {
	t.Helper()
	ctx := context.Background()
	taskID, colID, invID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	common := map[string]any{"workspace_id": scope.WorkspaceID, "agent_id": scope.AgentID, "tenant_org_id": scope.TenantOrgID, "updated_at": at}
	insert := func(table, source string, changes map[string]any) {
		patch := map[string]any{}
		for k, v := range common {
			patch[k] = v
		}
		for k, v := range changes {
			patch[k] = v
		}
		raw, err := json.Marshal(patch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO `+table+` SELECT (jsonb_populate_record(NULL::`+table+`,to_jsonb(old)||$2::jsonb)).* FROM `+table+` old WHERE id=$1::uuid`, source, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	insert("employee_task", col.TaskID, map[string]any{"id": taskID, "source_key": "closed-decoy-" + taskID})
	insert("employee_task_collection", col.ID, map[string]any{"id": colID, "task_id": taskID, "source_key": uuid.NewString(), "closed_at": at, "completed_at": nil})
	invPatch := map[string]any{"id": invID, "task_id": taskID, "collection_id": colID, "target_scene_id": scope.SceneID, "participant_ref": participant, "question": question, "delivery_action_id": "decoy-" + invID}
	if !delivered {
		invPatch["delivery_outcome"] = "pending"
		invPatch["delivered_at"] = nil
	}
	insert("employee_task_invitation", inv.ID, invPatch)
	t.Cleanup(func() {
		for _, row := range []struct{ table, id string }{{"employee_task_invitation", invID}, {"employee_task_collection", colID}, {"employee_task", taskID}} {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM `+row.table+` WHERE id=$1::uuid`, row.id); err != nil {
				t.Error(err)
			}
		}
	})
	return taskID
}

func TestCollectionClosedFactsExcludeForeignScopesParticipantsAndUnsent(t *testing.T) {
	c, col, inv := closedQuestionFixture(t)
	scope := employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: c.f.agentID, TenantOrgID: collectionTestOrg, SceneID: inv.TargetSceneID}
	cases := []struct {
		name        string
		scope       employeeentry.Scope
		participant string
		delivered   bool
	}{
		{"other_scene", scope, inv.ParticipantRef, true},
		{"other_tenant", scope, inv.ParticipantRef, true},
		{"other_agent", scope, inv.ParticipantRef, true},
		{"other_workspace", scope, inv.ParticipantRef, true},
		{"other_participant", scope, "other-participant", true},
		{"never_delivered", scope, inv.ParticipantRef, false},
	}
	for i := range cases {
		x := &cases[i]
		switch x.name {
		case "other_scene":
			x.scope.SceneID = uuid.NewString()
		case "other_tenant":
			x.scope.TenantOrgID = "other-org"
		case "other_agent":
			x.scope.AgentID = uuid.NewString()
		case "other_workspace":
			x.scope.WorkspaceID = uuid.NewString()
		}
		cloneClosedQuestion(t, c, col, inv, x.scope, x.participant, "PRIVATE-DECOY-"+x.name, time.Now(), x.delivered)
	}
	facts, err := employeeClosedQuestions(context.Background(), testPool, scope, inv.ParticipantRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(facts)
	if len(facts) != 1 || facts[0].State != "cancelled" || strings.Contains(string(raw), "PRIVATE-DECOY") || strings.Contains(string(raw), col.ID) || strings.Contains(string(raw), inv.ID) {
		t.Fatalf("foreign closed fact leaked: %s", raw)
	}
}

func TestCollectionClosedFactsBoundRecencyAndRows(t *testing.T) {
	c, col, inv := closedQuestionFixture(t)
	scope := employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: c.f.agentID, TenantOrgID: collectionTestOrg, SceneID: inv.TargetSceneID}
	oldTask := cloneClosedQuestion(t, c, col, inv, scope, inv.ParticipantRef, "STALE-25H-QUESTION", time.Now().Add(-25*time.Hour), true)
	// A later task write cannot make an old closed question recent again.
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET updated_at=now() WHERE id=$1::uuid`, oldTask); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		cloneClosedQuestion(t, c, col, inv, scope, inv.ParticipantRef, "RECENT-QUESTION-"+string(rune('A'+i)), time.Now(), true)
	}
	facts, err := employeeClosedQuestions(context.Background(), testPool, scope, inv.ParticipantRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(facts)
	if len(facts) != 5 || strings.Contains(string(raw), "STALE-25H-QUESTION") {
		t.Fatalf("unbounded closed facts: %s", raw)
	}
	for _, f := range facts {
		if f.EndedAt.Before(time.Now().Add(-24 * time.Hour)) {
			t.Fatal("stale closed fact", f)
		}
	}
}

func TestCollectionMixedClosedAndOpenFactsKeepOnlyActiveBinding(t *testing.T) {
	c, _, _ := closedQuestionFixture(t)
	current := c.origin("重新单独问Carol本周签了几单", collectionParticipants[0])
	c.deliver(current.ID, map[string]string{"Carol": "cid-carol-dm"})
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "mixed-current-answer", text: "这次问的签单是7单"})
	before := c.model.count()
	c.model.set(collectionQuiet)
	c.process()
	request := c.model.requests[before]
	if !strings.Contains(request, "closed_questions") || !strings.Contains(request, "invitation_context") || !strings.Contains(request, `"name":"accept_collection_input"`) {
		t.Fatal("closed facts suppressed the current open binding")
	}
	if !strings.Contains(request, "mixed") || !strings.Contains(request, "Only invitation_context carries active answer bindings") || !strings.Contains(request, "receiving a chat message alone is not a recording or memory-write effect") {
		t.Fatal("mixed facts did not separate active authority from closed-question constraints")
	}
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT j.input_snapshot FROM employee_scene_job j JOIN employee_event_consumption e ON e.job_id=j.id WHERE e.agent_id=$1::uuid AND e.payload#>>'{command,event,data,messages,0,openMsgId}'='mixed-current-answer'`, c.f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedInput
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Invitations) != 1 || len(saved.Invitations[0].Candidates) != 1 || saved.Invitations[0].Candidates[0].CollectionID != current.ID {
		t.Fatal("closed context manufactured or replaced an active binding")
	}
}

func TestCollectionActiveOnlyFactsKeepAnswerContractWithoutClosedMode(t *testing.T) {
	c := newCollectionHarness(t)
	c.seed()
	col := c.origin("问Carol本周签了几单", collectionParticipants[0])
	c.deliver(col.ID, map[string]string{"Carol": "cid-carol-dm"})
	c.send(collectionMessage{conversation: "cid-carol-dm", kind: "single", name: "Carol", openID: "carol-open", messageID: "active-mode-answer", text: "7单"})
	before := c.model.count()
	c.model.set(collectionQuiet)
	c.process()
	request := c.model.requests[before]
	if !strings.Contains(request, "active_only") || !strings.Contains(request, `"name":"accept_collection_input"`) || !strings.Contains(request, "Only invitation_context carries active answer bindings") {
		t.Fatal("active-only input lost the existing answer authority")
	}
	if strings.Contains(request, "closed_questions") || strings.Contains(request, "receiving a chat message alone is not a recording or memory-write effect") {
		t.Fatal("active-only input received an unrelated closed-question mode")
	}
}
