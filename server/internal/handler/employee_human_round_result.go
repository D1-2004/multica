package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

// materializeEmployeeRoundResultTx runs only after the verified Run ended. The
// model supplies display content; the Host supplies every routing/goal binding.
func (h *Handler) materializeEmployeeRoundResultTx(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding, in dingtalkresponse.ActionInput) (string, bool, error) {
	var marker struct {
		Version string `json:"employee_round_result_contract"`
	}
	if json.Unmarshal(b.Queue.Context, &marker) != nil || marker.Version != humanquestion.Version || b.ResultState != "succeeded" {
		return "", false, nil
	}
	if h.EmployeeSceneWorker == nil || !h.EmployeeSceneWorker.humanQuestionsReady(ctx) {
		return "", true, errors.New("human round result readers are not ready")
	}
	result, recognized, err := humanquestion.Decode(b.Result)
	if err != nil || !recognized {
		return "本轮已结束，但结果格式不完整，尚未确认事项完成。请告诉我下一步要求。", true, nil
	}
	taskScope := b.Scope
	task, err := employeetask.NewStore(tx).Get(ctx, taskScope, b.TaskID)
	if err != nil {
		return "", true, err
	}
	if task.Lifecycle() != employeetask.LifecycleV2 {
		return "", true, errors.New("human result requires an explicit goal")
	}
	authority := "employee_scene:" + b.SourceRef
	if result.Choice == nil || result.Choice.Intent == "suggest" {
		if task.State != employeetask.StateSucceeded {
			task, _, err = employeetask.CompleteGoalTx(ctx, tx, taskScope, task.ID, employeetask.CompleteGoalParams{Source: employeetask.Source{Namespace: "employee_human_result", Key: b.RunID + "/complete"}, GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq, ExpectedVersion: task.Version, AuthorityRef: authority, EvidenceRef: "run:" + b.RunID, Summary: result.Summary})
			if err != nil {
				return "", true, err
			}
		}
	}
	if result.Choice == nil {
		return result.Summary, true, nil
	}
	originID := b.JobID
	var kind string
	if err = tx.QueryRow(ctx, `SELECT kind FROM employee_scene_job WHERE id=$1::uuid`, originID).Scan(&kind); err != nil {
		return "", true, err
	}
	if kind == employeeentry.KindHumanResponse {
		actual := employeeentry.Job{ID: originID, Kind: kind, Scope: employeeentry.Scope{WorkspaceID: b.Scope.WorkspaceID, AgentID: b.Scope.AgentID, TenantOrgID: b.Scope.TenantOrgID, SceneID: b.Scope.Scene.SceneID}}
		if err = tx.QueryRow(ctx, `SELECT principal_id::text,items,state FROM employee_scene_job WHERE id=$1::uuid`, originID).Scan(&actual.PrincipalID, &actual.Items, &actual.State); err != nil {
			return "", true, err
		}
		binding, e := readEmployeeHumanBinding(ctx, tx, actual)
		if e != nil {
			return "", true, e
		}
		originID = binding.Question.SourceJobID
	}
	q := humanquestion.Question{ID: uuid.NewSHA1(humanQuestionNamespace, []byte("run/"+b.RunID)).String(), Scope: employeeentry.Scope{WorkspaceID: b.Scope.WorkspaceID, AgentID: b.Scope.AgentID, TenantOrgID: b.Scope.TenantOrgID, SceneID: b.Scope.Scene.SceneID}, SourceJobID: originID, SourceRef: b.SourceRef, RequesterRef: b.Requester, TaskID: b.TaskID, RunID: b.RunID, GoalRevision: b.RunGoalRevision, Version: 1, Summary: result.Summary, Choice: *result.Choice, OperatorOpenID: in.SenderOpenDingTalkID}
	if err = tx.QueryRow(ctx, `SELECT principal_id::text FROM employee_scene_job WHERE id=$1::uuid`, originID).Scan(&q.PrincipalID); err != nil {
		return "", true, err
	}
	for i, c := range q.SourceRef {
		if c == '/' {
			q.SourceReceiptID = q.SourceRef[:i]
			break
		}
	}
	if q.SourceReceiptID == "" {
		return "", true, humanquestion.ErrInvalid
	}
	in.EmployeeRunNoticeID = ""
	in.Text = result.Summary
	if _, err = h.stageEmployeeHumanQuestion(ctx, tx, &q, in); err != nil {
		return "", true, err
	}
	if q.Choice.Intent == "clarify" {
		_, _, err = employeetask.WaitTaskTx(ctx, tx, taskScope, task.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "employee_human_result", Key: b.RunID + "/wait"}, Kind: employeetask.WaitHumanInput, RefID: q.ID, Mandatory: true, AuthorityRef: authority, Body: q.Choice.Question, ExpectedVersion: task.Version})
		if err != nil {
			return "", true, err
		}
	}
	return result.Summary, true, nil
}
