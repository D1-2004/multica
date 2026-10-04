package dingtalkresponse

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

const firstFeedbackTarget = "employee-first-feedback"

func FirstFeedbackRequestID(jobID, receiptID string) string {
	return "employee-first-feedback:" + jobID + ":" + receiptID
}

// EnqueueFirstFeedback closes no request and cannot enter Task delivery evidence.
func (s *Service) EnqueueFirstFeedback(ctx context.Context, tx DBTX, in ActionInput, jobID, receiptID, sourceRef string) (string, error) {
	in.EmployeeFirstFeedbackJobID, in.EmployeeFirstFeedbackReceiptID, in.EmployeeFirstFeedbackSourceRef = jobID, receiptID, sourceRef
	in.A2UICard = nil
	in.RequestID = FirstFeedbackRequestID(jobID, receiptID)
	in.ActionID = ""
	in.TaskID, in.IssueID, in.CallbackURL, in.CloseState, in.ReplyToOpenMsgID = "", "", "", "", ""
	in.EmployeeMessageJobID, in.EmployeeRunNoticeID, in.CoordinatorWaitJobID, in.SceneNoticeID, in.RoutineRunID, in.InvitationActionID = "", "", "", "", "", ""
	in.CallbackTarget = firstFeedbackTarget
	return s.enqueue(ctx, tx, in)
}
func validateFirstFeedbackInput(in ActionInput) error {
	for _, id := range []string{in.EmployeeFirstFeedbackJobID, in.EmployeeFirstFeedbackReceiptID} {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("first feedback identity invalid")
		}
	}
	if in.A2UICard != nil || in.EmployeeFirstFeedbackSourceRef == "" || in.RequestID != FirstFeedbackRequestID(in.EmployeeFirstFeedbackJobID, in.EmployeeFirstFeedbackReceiptID) || in.CallbackTarget != firstFeedbackTarget || in.CallbackURL != "" || in.TaskID != "" || in.IssueID != "" || in.CloseState != "" || in.ReplyToOpenMsgID != "" || in.EmployeeMessageJobID != "" || in.EmployeeRunNoticeID != "" || in.CoordinatorWaitJobID != "" || in.SceneNoticeID != "" || in.RoutineRunID != "" || in.InvitationActionID != "" || in.Text == "" {
		return errors.New("first feedback cannot close a request or claim business execution")
	}
	return nil
}

// Reserve after BeforeSend. A final transition that cancels pending feedback wins
// the response row race; accepted/unknown actions never pass through here again.
func (s *Service) reserveFirstFeedbackSubmission(ctx context.Context, a *action) (bool, error) {
	now := time.Now()
	tag, err := s.pool.Exec(ctx, `UPDATE response_action a SET state='unknown',error_code='submission_interrupted',updated_at=$3,first_attempt_at=COALESCE(first_attempt_at,$3)
 WHERE a.id=$1 AND a.lease_token=$2::uuid AND a.state='pending'
 AND EXISTS(SELECT 1 FROM employee_first_feedback f JOIN employee_scene_job j ON j.id=f.job_id
 WHERE f.action_id=a.id AND f.state='enqueued' AND j.state='running' AND j.outcome IS NULL
 AND NOT EXISTS(SELECT 1 FROM employee_scene_participation p WHERE p.workspace_id=j.workspace_id AND p.agent_id=j.agent_id AND p.tenant_org_id=j.tenant_org_id AND p.scene_id=j.scene_id AND p.mode='quiet'))`, a.ID, a.LeaseToken, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		a.State, a.ErrorCode, a.UpdatedAt = "unknown", "submission_interrupted", now
		return true, nil
	}
	// Reload cancellation instead of resurrecting an in-memory pending action.
	err = s.pool.QueryRow(ctx, `SELECT state,error_code FROM response_action WHERE id=$1 AND lease_token=$2::uuid`, a.ID, a.LeaseToken).Scan(&a.State, &a.ErrorCode)
	if err == nil && a.State == "pending" {
		err = s.saveState(ctx, a, "cancelled", "", "", "", "host_send_suppressed:feedback_job_resolved")
	}
	return false, err
}
