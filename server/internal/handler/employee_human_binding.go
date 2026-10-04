package handler

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeHumanBinding struct {
	Question   humanquestion.Question
	Response   humanquestion.Response
	OriginJob  employeeentry.Job
	Source     employeeSourceMessage
	Envelope   employeeDispatchEnvelope
	Registered db.AgentScene
}

// readEmployeeQuestionSource preserves the admitted human message. A card
// answer is separate evidence and never replaces its text or source receipt.
func readEmployeeQuestionSource(ctx context.Context, database employeeentry.DB, q humanquestion.Question) (employeeHumanBinding, error) {
	b := employeeHumanBinding{Question: q}
	var items []byte
	err := database.QueryRow(ctx, `SELECT id::text,kind,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state,input_snapshot FROM employee_scene_job WHERE id=$1::uuid`, q.SourceJobID).Scan(&b.OriginJob.ID, &b.OriginJob.Kind, &b.OriginJob.Scope.WorkspaceID, &b.OriginJob.Scope.AgentID, &b.OriginJob.Scope.TenantOrgID, &b.OriginJob.Scope.SceneID, &b.OriginJob.PrincipalID, &items, &b.OriginJob.State, &b.OriginJob.InputSnapshot)
	if err != nil {
		return b, err
	}
	if b.OriginJob.Kind != employeeentry.KindMessage || b.OriginJob.Scope != q.Scope || b.OriginJob.PrincipalID != q.PrincipalID || json.Unmarshal(items, &b.OriginJob.Items) != nil {
		return b, humanquestion.ErrForbidden
	}
	matches := 0
	for _, item := range b.OriginJob.Items {
		if item.ReceiptID != q.SourceReceiptID {
			continue
		}
		var env employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &env) != nil || env.PrincipalID != item.PrincipalID || env.Command.EventReceiptID != item.ReceiptID {
			return b, humanquestion.ErrForbidden
		}
		for _, source := range employeeSourceMessages(item, env) {
			if source.SourceRef == q.SourceRef {
				if source.RequesterRef != q.RequesterRef || source.Message.SenderOpenDingTalkID != q.OperatorOpenID {
					return b, humanquestion.ErrForbidden
				}
				b.Source, b.Envelope = source, env
				matches++
			}
		}
	}
	if matches != 1 {
		return b, humanquestion.ErrForbidden
	}
	view := &Handler{Queries: db.New(database)}
	b.Registered, err = employeeSceneFence(ctx, view, b.OriginJob)
	if err != nil {
		return b, err
	}
	if err = employeePrincipalAllowed(ctx, view, q.Scope, q.PrincipalID); err != nil {
		return b, err
	}
	agent, err := view.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(q.Scope.AgentID), WorkspaceID: parseUUID(q.Scope.WorkspaceID)})
	if err != nil {
		return b, err
	}
	if uuidToString(agent.OwnerID) != q.PrincipalID {
		if agent.PermissionMode != "public_to" {
			return b, humanquestion.ErrForbidden
		}
		targets, e := view.Queries.ListAgentInvocationTargets(ctx, agent.ID)
		if e != nil {
			return b, e
		}
		if !memberHitsInvocationTargets(targets, q.PrincipalID) {
			return b, humanquestion.ErrForbidden
		}
	}
	endpoint, err := view.Queries.GetAgentDispatchEndpointByEndpointID(ctx, b.Envelope.EndpointID)
	if err != nil {
		return b, err
	}
	if uuidToString(endpoint.ID) != b.Envelope.EndpointNamespaceID || uuidToString(endpoint.ActorUserID) != q.PrincipalID || uuidToString(endpoint.AgentID) != q.Scope.AgentID || uuidToString(endpoint.WorkspaceID) != q.Scope.WorkspaceID {
		return b, humanquestion.ErrForbidden
	}
	var valid bool
	err = database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_event_consumption c JOIN scene_event_receipt r ON r.id=c.receipt_id WHERE c.receipt_id=$1::uuid AND c.job_id=$2::uuid AND c.owner_loop='employee' AND c.principal_id=$3::uuid AND r.workspace_id=$4::uuid AND r.agent_id=$5::uuid AND r.tenant_org_id=$6 AND r.scene_id=$7::uuid AND r.principal_id=$3::uuid AND r.reason='' AND ((r.route='unified' AND r.state='ready') OR (r.route='legacy' AND r.state='legacy')))`, q.SourceReceiptID, q.SourceJobID, q.PrincipalID, q.Scope.WorkspaceID, q.Scope.AgentID, q.Scope.TenantOrgID, q.Scope.SceneID).Scan(&valid)
	if err != nil {
		return b, err
	}
	if !valid {
		return b, humanquestion.ErrForbidden
	}
	return b, nil
}

func readEmployeeHumanBinding(ctx context.Context, database employeeentry.DB, job employeeentry.Job) (employeeHumanBinding, error) {
	if job.Kind != employeeentry.KindHumanResponse || len(job.Items) != 1 {
		return employeeHumanBinding{}, humanquestion.ErrInvalid
	}
	ref, err := employeeentry.DecodeHumanResponse(job.Items[0])
	if err != nil {
		return employeeHumanBinding{}, err
	}
	store := humanquestion.NewStore(database)
	q, err := store.Get(ctx, job.Scope, ref.QuestionRef)
	if err != nil {
		return employeeHumanBinding{}, err
	}
	r, err := store.Response(ctx, ref.ResponseRef)
	if err != nil {
		return employeeHumanBinding{}, err
	}
	if q.PrincipalID != job.PrincipalID || q.Version != ref.Version || r.QuestionID != q.ID || r.JobID != job.ID || r.ReceiptID != job.Items[0].ReceiptID || q.ResponseID != r.ID || q.ValidateResponse(r) != nil {
		return employeeHumanBinding{}, humanquestion.ErrForbidden
	}
	b, err := readEmployeeQuestionSource(ctx, database, q)
	if err != nil {
		return b, err
	}
	b.Response = r
	if b.OriginJob.State != "completed" {
		return b, errors.New("human question source is still completing")
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		return b, err
	}
	defer tx.Rollback(ctx)
	if err = humanquestion.LockScope(ctx, tx, q.Scope); err != nil {
		return b, err
	}
	if err = humanquestion.CurrentTargetTx(ctx, tx, q); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}
