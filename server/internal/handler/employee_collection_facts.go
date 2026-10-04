package handler

import (
	"context"
	"errors"
	"time"

	"github.com/multica-ai/multica/server/internal/taskinput"
)

// These are bounded Host-observed facts, not a model summary or permissions.
// Confirmation timestamps deliberately do not assert provider receipt time.
type employeeCollectionProcessFacts struct {
	AsOf         time.Time                            `json:"as_of"`
	Participants []employeeCollectionParticipantFacts `json:"participants"`
}
type employeeCollectionParticipantFacts struct {
	Participant           string                           `json:"participant"`
	InvitationDelivery    string                           `json:"invitation_delivery"`
	InvitationDeliveredAt *time.Time                       `json:"invitation_delivered_at,omitempty"`
	AnswerTime            string                           `json:"answer_time"`
	AnswerOccurredAt      *time.Time                       `json:"answer_occurred_at,omitempty"`
	ReminderPolicy        string                           `json:"reminder_policy"`
	Reminders             []employeeCollectionReminderFact `json:"reminders"`
}
type employeeCollectionReminderFact struct {
	Ordinal     int        `json:"ordinal"`
	State       string     `json:"state"`
	RecordedAt  time.Time  `json:"recorded_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

func employeeCollectionFacts(ctx context.Context, database taskinput.DB, scope taskinput.Scope, view taskinput.OriginView, before time.Time) (*employeeCollectionProcessFacts, error) {
	if len(view.Slots) > 32 || before.IsZero() {
		return nil, errors.New("invalid collection facts boundary")
	}
	out := &employeeCollectionProcessFacts{AsOf: before, Participants: make([]employeeCollectionParticipantFacts, len(view.Slots))}
	indices := make(map[string]int, len(view.Slots))
	ids := make([]string, 0, len(view.Slots))
	for i, slot := range view.Slots {
		indices[slot.InvitationID] = i
		ids = append(ids, slot.InvitationID)
		f := employeeCollectionParticipantFacts{Participant: slot.ParticipantLabel, InvitationDelivery: "unknown", AnswerTime: "unknown", ReminderPolicy: "unknown", Reminders: []employeeCollectionReminderFact{}}
		if f.Participant == "" {
			f.Participant = employeeCollectionSlots(view)[i].Participant
		}
		if slot.Answer != nil && !slot.Answer.OccurredAt.IsZero() && !slot.Answer.OccurredAt.After(before) {
			at := slot.Answer.OccurredAt
			f.AnswerOccurredAt = &at
			f.AnswerTime = "source_occurred_at"
		}
		out.Participants[i] = f
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := database.Query(ctx, `SELECT i.id::text,i.delivery_outcome,i.delivered_at,p.invitation_id IS NOT NULL
 FROM employee_task_invitation i LEFT JOIN employee_task_invitation_reminder_policy p
 ON p.invitation_id=i.id AND p.workspace_id=i.workspace_id AND p.agent_id=i.agent_id AND p.tenant_org_id=i.tenant_org_id AND p.task_id=i.task_id AND p.collection_id=i.collection_id AND p.created_at<=$7
 WHERE i.workspace_id=$1::uuid AND i.agent_id=$2::uuid AND i.tenant_org_id=$3 AND i.collection_id=$4::uuid AND i.task_id=$5::uuid AND i.id=ANY($6::uuid[]) AND i.created_at<=$7`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, view.CollectionID, view.TaskID, ids, before)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, outcome string
		var at *time.Time
		var policy bool
		if err := rows.Scan(&id, &outcome, &at, &policy); err != nil {
			rows.Close()
			return nil, err
		}
		f := &out.Participants[indices[id]]
		if at != nil && !at.After(before) && outcome == string(taskinput.DeliverySent) {
			f.InvitationDelivery = "delivered"
			f.InvitationDeliveredAt = at
		}
		f.ReminderPolicy = "not_authorized"
		if policy {
			f.ReminderPolicy = "authorized"
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = database.Query(ctx, `SELECT r.invitation_id::text,r.ordinal,r.state,r.reason,r.created_at,r.updated_at,a.state,a.updated_at,COALESCE(a.provider_message_id,'')
 FROM employee_task_invitation_reminder r LEFT JOIN response_action a ON a.id=r.action_id AND a.workspace_id=r.workspace_id AND a.agent_id=r.agent_id
 WHERE r.workspace_id=$1::uuid AND r.agent_id=$2::uuid AND r.tenant_org_id=$3 AND r.collection_id=$4::uuid AND r.task_id=$5::uuid AND r.invitation_id=ANY($6::uuid[]) AND r.created_at<=$7
 ORDER BY r.invitation_id,r.ordinal LIMIT 97`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, view.CollectionID, view.TaskID, ids, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 96 {
			return nil, errors.New("collection reminder facts exceed bound")
		}
		var id, state, reason, message string
		var ordinal int
		var at, updated time.Time
		var actionState *string
		var confirmed *time.Time
		if err := rows.Scan(&id, &ordinal, &state, &reason, &at, &updated, &actionState, &confirmed, &message); err != nil {
			return nil, err
		}
		fact := employeeCollectionReminderFact{Ordinal: ordinal, State: state, RecordedAt: at, Reason: employeeTaskData(reason, 64)}
		// Reminder rows mutate when sending is suppressed. A later row state is not
		// historical evidence at this wake boundary; the query supplies updated_at.
		if updated.After(before) {
			state = "unknown"
			fact.Reason = ""
		}
		fact.State, fact.ConfirmedAt = employeeCollectionReminderOutcome(state, actionState, confirmed, message, before)
		out.Participants[indices[id]].Reminders = append(out.Participants[indices[id]].Reminders, fact)
	}
	return out, rows.Err()
}

func employeeCollectionReminderOutcome(state string, actionState *string, confirmed *time.Time, message string, before time.Time) (string, *time.Time) {
	if state != "enqueued" {
		return state, nil
	}
	if actionState == nil || confirmed == nil || confirmed.After(before) {
		return "unknown", nil
	}
	switch *actionState {
	case "delivered":
		if message != "" {
			return "delivered", confirmed
		}
		return "unknown", nil
	case "provider_accepted":
		return "provider_accepted", confirmed
	case "pending":
		return "enqueued", nil
	case "failed", "silent", "cancelled":
		return *actionState, confirmed
	default:
		return "unknown", nil
	}
}
