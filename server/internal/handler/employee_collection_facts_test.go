package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

func TestEmployeeCollectionFactsDeliveryStates(t *testing.T) {
	before := time.Now().UTC()
	past := before.Add(-time.Minute)
	future := before.Add(time.Minute)
	for _, tc := range []struct {
		name, state, action, message, want string
		at                                 *time.Time
	}{
		{"delivered", "enqueued", "delivered", "pm", "delivered", &past},
		{"accepted is not receipt", "enqueued", "provider_accepted", "", "provider_accepted", &past},
		{"missing receipt", "enqueued", "delivered", "", "unknown", &past},
		{"future receipt", "enqueued", "delivered", "pm", "unknown", &future},
		{"pending", "enqueued", "pending", "", "enqueued", &past},
		{"unknown", "enqueued", "unknown", "", "unknown", &past},
		{"held", "held", "", "", "held", nil},
		{"suppressed", "suppressed", "", "", "suppressed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, at := employeeCollectionReminderOutcome(tc.state, &tc.action, tc.at, tc.message, before)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			if (got == "delivered" || got == "provider_accepted") && at == nil {
				t.Fatal("confirmed state lost confirmation time")
			}
		})
	}
}

func TestEmployeeCollectionFactsScopedFrozenSnapshot(t *testing.T) {
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	scope := taskinput.Scope{WorkspaceID: uuid.NewString(), AgentID: uuid.NewString(), TenantOrgID: "facts-org"}
	task, col, inv, sc := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	before := time.Now().UTC().Truncate(time.Microsecond)
	past := before.Add(-time.Minute)
	answer := before.Add(-time.Second)
	_, err = tx.Exec(ctx, `INSERT INTO employee_task_invitation(id,workspace_id,agent_id,tenant_org_id,task_id,collection_id,ordinal,collection_revision,target_scene_id,target_scene_kind,participant_ref,question,delivery_action_id,delivery_outcome,delivered_at,created_at) VALUES($1,$2,$3,$4,$5,$6,1,1,$7,'dm','person:a','number','invite-action','sent',$8,$8)`, inv, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, task, col, sc, past)
	if err != nil {
		t.Fatal(err)
	}
	insertReminder := func(org string, ordinal int, action, state string, created, updated time.Time) {
		t.Helper()
		_, err := tx.Exec(ctx, `INSERT INTO employee_task_invitation_reminder(workspace_id,agent_id,tenant_org_id,task_id,collection_id,invitation_id,target_scene_id,ordinal,state,action_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, scope.WorkspaceID, scope.AgentID, org, task, col, inv, sc, ordinal, state, action, created, updated)
		if err != nil {
			t.Fatal(err)
		}
	}
	insertReminder(scope.TenantOrgID, 1, "reminder-delivered", "enqueued", past, past)
	insertReminder(scope.TenantOrgID, 2, "reminder-held", "held", past, past)
	insertReminder(scope.TenantOrgID, 3, "reminder-later", "suppressed", past, before.Add(time.Minute))
	// Identical foreign invitation id still cannot import another tenant's fact.

	_, err = tx.Exec(ctx, `INSERT INTO response_action(id,workspace_id,agent_id,request_id,kind,input,state,provider_message_id,created_at,updated_at) VALUES('reminder-delivered',$1,$2,'r','message.send','{}','delivered','pm',$3,$3)`, scope.WorkspaceID, scope.AgentID, past)
	if err != nil {
		t.Fatal(err)
	}
	view := taskinput.OriginView{CollectionID: col, TaskID: task, Slots: []taskinput.OriginSlot{{InvitationID: inv, ParticipantLabel: "Alice", Answer: &taskinput.Answer{Body: "10", OccurredAt: answer}}}}
	facts, err := employeeCollectionFacts(ctx, tx, scope, view, before)
	if err != nil {
		t.Fatal(err)
	}
	got := facts.Participants[0]
	if got.InvitationDelivery != "delivered" || got.AnswerOccurredAt == nil || !got.AnswerOccurredAt.Equal(answer) || len(got.Reminders) != 3 || got.Reminders[0].State != "delivered" || got.Reminders[1].State != "held" || got.Reminders[2].State != "unknown" {
		t.Fatalf("facts %+v", got)
	}
	frozen, err := json.Marshal(employeeCollectionWakeView{ProcessFacts: facts})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frozen), `"process_facts"`) || strings.Contains(string(frozen), "foreign") || strings.Contains(string(frozen), "reminder-delivered") {
		t.Fatal("missing facts or leaked internal ids", string(frozen))
	}
	_, err = tx.Exec(ctx, `UPDATE response_action SET state='unknown',provider_message_id='' WHERE id='reminder-delivered'`)
	if err != nil {
		t.Fatal(err)
	}
	var saved employeeCollectionWakeView
	if err = json.Unmarshal(frozen, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ProcessFacts.Participants[0].Reminders[0].State != "delivered" {
		t.Fatal("saved snapshot changed after delivery record mutation")
	}
	foreign := scope
	foreign.TenantOrgID = "other"
	hidden, err := employeeCollectionFacts(ctx, tx, foreign, view, before)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden.Participants[0].Reminders) != 0 || hidden.Participants[0].InvitationDelivery != "unknown" {
		t.Fatal("cross-scope facts leaked", hidden)
	}
}

// This is a contract regression check, not an actual model-behavior test.
func TestEmployeeIndependentTaskContractPrecedesContinuation(t *testing.T) {
	policy := employeeTaskExecutionInheritancePolicy
	independent := strings.Index(policy, "explicit choice of an independent new Task")
	same := strings.Index(policy, "same Task's new step")
	if independent < 0 || same < independent {
		t.Fatal("independent request lacks priority")
	}
	for _, want := range []string{"do not continue_task", "set builds_on", "actually run Python", "do not repeat an old sleep"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("contract lost %q", want)
		}
	}
	for _, tool := range employeeSceneTools() {
		if tool.Name == "dispatch_task" && !strings.Contains(tool.Description, "independent new Task") {
			t.Fatal("dispatch contract lacks independent-task priority")
		}
		if tool.Name == "continue_task" && !strings.Contains(tool.Description, "dispatch_task takes precedence") {
			t.Fatal("continue contract lacks precedence")
		}
	}
}
