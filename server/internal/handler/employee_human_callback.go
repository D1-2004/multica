package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

func (h *Handler) handleEmployeeHumanCard(ctx context.Context, id dwsclient.Identity, line []byte) (bool, error) {
	if h.EmployeeSceneWorker == nil {
		return false, nil
	}
	ref, known := a2ui.NativeReference(line)
	if !known {
		return false, nil
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return false, nil
	}
	q, err := humanquestion.NewStore(database).ByPublicID(ctx, id.AgentID, id.OrgID, ref)
	if errors.Is(err, humanquestion.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !h.EmployeeSceneWorker.humanQuestionsReady(ctx) {
		return true, errors.New("human question readers are not ready")
	}
	got, err := h.A2UI.InspectNativeAnswer(ctx, a2ui.Actor{AgentID: uuid.MustParse(id.AgentID), UID: id.UID, OrgID: id.OrgID}, line)
	if err != nil {
		return true, nil
	}
	response, err := mapNativeHumanAnswer(q, got)
	if err != nil {
		return true, nil
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback(ctx)
	_, r, err := humanquestion.AcceptTx(ctx, tx, q.Scope, response, h.admitEmployeeHumanResponseTx)
	if errors.Is(err, humanquestion.ErrForbidden) || errors.Is(err, humanquestion.ErrStale) || errors.Is(err, humanquestion.ErrInvalid) || errors.Is(err, humanquestion.ErrConflict) {
		slog.InfoContext(ctx, "human card response rejected", "event", "employee_human_response_rejected", "question_id", q.ID, "reason", err.Error())
		return true, nil
	}
	if err != nil {
		return true, err
	}
	raw, _ := json.Marshal(map[string]any{"outcome": got.Outcome, "selected": got.Selected, "custom": got.Custom})
	_, err = tx.Exec(ctx, `UPDATE a2ui_interaction SET status=$2,event_id=$3,operator_uid=$4,result=$5,resolved_at=now() WHERE id=$1::uuid AND status='open'`, q.ID, map[string]string{"answered": "answered", "skipped": "skipped"}[got.Outcome], got.EventID, got.Operator, raw)
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	h.EmployeeSceneWorker.Notify()
	slog.InfoContext(ctx, "human card response accepted", "event", "employee_human_response_accepted", "question_id", q.ID, "response_id", r.ID, "job_id", r.JobID, "scene_id", q.Scope.SceneID, "input_surface", r.Surface)
	return true, nil
}

// OnEmployeeHumanCardAccepted records transport facts independently from the
// decision state, including when the answer arrived before the send returned.
func (h *Handler) OnEmployeeHumanCardAccepted(ctx context.Context, in dingtalkresponse.ActionInput, receipt dwsclient.A2UIReceipt) error {
	if in.A2UICard == nil {
		return nil
	}
	database, ok := employeeEntryDB(h)
	if !ok {
		return humanquestion.ErrInvalid
	}
	scope := employeeentry.Scope{WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, TenantOrgID: in.DWSOrgID, SceneID: in.SceneID}
	q, err := humanquestion.NewStore(database).Get(ctx, scope, in.A2UICard.QuestionID)
	if err != nil {
		return err
	}
	if q.ActionID != in.ActionID && in.ActionID != "" {
		return humanquestion.ErrForbidden
	}
	if receipt.ConversationID != "" && receipt.ConversationID != in.ConversationID {
		return humanquestion.ErrForbidden
	}
	_, err = database.Exec(ctx, `UPDATE a2ui_interaction SET card_biz_id=$2,message_id=CASE WHEN $3<>'' THEN $3 ELSE message_id END WHERE id=$1::uuid AND agent_id=$4::uuid AND sender_org_id=$5`, q.ID, receipt.BizID, receipt.MessageID, q.Scope.AgentID, q.Scope.TenantOrgID)
	return err
}
