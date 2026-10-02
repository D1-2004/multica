package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

type employeeNoticeBinding struct {
	Scope                                                  employeetask.Scope
	TaskID, RunID, QueueID, Requester, ResultState, Result string
	JobID, SourceRef                                       string
	Queue                                                  db.AgentTaskQueue
	TaskGoalRevision, RunGoalRevision                      int64
	CompletionNotice                                       employeetask.CompletionNoticePolicy
}

type employeeNoticeHold struct{ reason string }

func (e *employeeNoticeHold) Error() string  { return e.reason }
func holdEmployeeNotice(reason string) error { return &employeeNoticeHold{reason} }

// ReconcileEmployeeRunNotices records one final delivery intent per actual Run.
// It never invokes a model or retries execution. The existing response outbox
// owns provider sends, unknown-result queries and Router receipts.
func (h *Handler) ReconcileEmployeeRunNotices(ctx context.Context, limit int) (int, error) {
	if h == nil || h.TxStarter == nil || h.DB == nil || h.DingTalkResponses == nil {
		return 0, errors.New("employee notice services are unavailable")
	}
	if err := h.employeeNoticeReplicasReady(ctx); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 1000 {
		return 0, errors.New("employee notice limit is invalid")
	}
	rows, err := h.DB.Query(ctx, `SELECT r.id::text,t.workspace_id::text FROM employee_task_run r
 JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 JOIN agent_task_queue q ON q.id=r.queue_task_id AND q.agent_id=t.agent_id
 LEFT JOIN employee_scene_job j ON j.id::text=q.context->>'employee_job_id'
 LEFT JOIN employee_run_notice n ON n.run_id=r.id
 WHERE t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'
 AND r.state IN ('succeeded','failed','cancelled') AND q.status IN ('completed','failed','cancelled')
 AND q.context->>'employee_delivery_owner'='employee' AND (j.state='completed' OR j.id IS NULL) AND n.run_id IS NULL
 ORDER BY r.finished_at,r.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ run, workspace string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.run, &c.workspace); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range candidates {
		created, e := h.enqueueEmployeeRunNotice(ctx, c.workspace, c.run)
		if e != nil {
			return count, fmt.Errorf("employee run notice %s: %w", c.run, e)
		}
		if created {
			count++
		}
	}
	if count > 0 {
		h.DingTalkResponses.Notify()
	}
	return count, nil
}

func (h *Handler) loadEmployeeNoticeBinding(ctx context.Context, tx pgx.Tx, workspaceID, runID string) (employeeNoticeBinding, error) {
	var b employeeNoticeBinding
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&locked); err != nil {
		return b, err
	}
	err := tx.QueryRow(ctx, `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,t.requester_ref,r.id::text,r.queue_task_id::text,r.state,r.result,t.goal_revision,r.goal_revision
 FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.tenant_org_id=t.tenant_org_id
 WHERE t.workspace_id=$1::uuid AND r.id=$2::uuid AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'
 AND r.state IN ('succeeded','failed','cancelled') FOR UPDATE OF t,r`, workspaceID, runID).Scan(&b.Scope.WorkspaceID, &b.Scope.AgentID, &b.Scope.TenantOrgID, &b.Scope.Scene.SceneID, &b.TaskID, &b.Requester, &b.RunID, &b.QueueID, &b.ResultState, &b.Result, &b.TaskGoalRevision, &b.RunGoalRevision)
	if err != nil {
		return b, err
	}
	b.Scope.Kind = employeetask.ScopeScene
	b.Queue, err = db.New(tx).GetAgentTask(ctx, parseUUID(b.QueueID))
	if err != nil {
		return b, err
	}
	var metadata struct {
		JobID     string    `json:"employee_job_id"`
		SourceRef string    `json:"employee_source_ref"`
		Owner     string    `json:"employee_delivery_owner"`
		Scene     scene.Ref `json:"agent_scene"`
	}
	if json.Unmarshal(b.Queue.Context, &metadata) != nil {
		return b, holdEmployeeNotice("invalid_queue_context")
	}
	b.JobID, b.SourceRef = metadata.JobID, metadata.SourceRef
	var policyEnvelope struct {
		Policy employeetask.CompletionNoticePolicy `json:"employee_completion_notice_policy"`
	}
	if json.Unmarshal(b.Queue.Context, &policyEnvelope) != nil {
		return b, holdEmployeeNotice("invalid_completion_notice_policy")
	}
	b.CompletionNotice, err = employeetask.NormalizeCompletionNoticePolicy(policyEnvelope.Policy, b.SourceRef)
	if err != nil {
		return b, holdEmployeeNotice("invalid_completion_notice_policy")
	}

	if _, err := util.ParseUUID(b.JobID); err != nil {
		b.JobID = ""
		return b, holdEmployeeNotice("invalid_job_binding")
	}
	c, ok := service.ParseDirectTaskContext(b.Queue)
	states := map[string]string{"succeeded": "completed", "failed": "failed", "cancelled": "cancelled"}
	if !ok || metadata.Owner != "employee" || c.EmployeeTaskID != b.TaskID || c.WorkspaceID != workspaceID || metadata.Scene.SceneID != b.Scope.Scene.SceneID || uuidToString(b.Queue.AgentID) != b.Scope.AgentID || b.Queue.Status != states[b.ResultState] {
		return b, holdEmployeeNotice("run_queue_binding_mismatch")
	}
	// Keep the old Run and its result for audit, but never deliver it as the
	// answer to a subsequently corrected goal. Both enqueue and BeforeSend
	// load these revisions under the same Task/Run locks.
	if b.RunGoalRevision != b.TaskGoalRevision {
		return b, holdEmployeeNotice("stale_goal_revision")
	}
	return b, nil
}

// employeeNoticeTarget resolves only the originally selected source. It uses
// current membership, invocation, identity and scene fences, never current mode.
func (h *Handler) employeeNoticeTarget(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding) (dingtalkresponse.ActionInput, bool, error) {
	var in dingtalkresponse.ActionInput
	job := employeeentry.Job{ID: b.JobID}
	err := tx.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state FROM employee_scene_job WHERE id=$1::uuid`, b.JobID).Scan(&job.Scope.WorkspaceID, &job.Scope.AgentID, &job.Scope.TenantOrgID, &job.Scope.SceneID, &job.PrincipalID, &job.Items, &job.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, holdEmployeeNotice("source_job_missing")
	}
	if err != nil {
		return in, false, err
	}
	if job.State != "completed" || job.Scope.WorkspaceID != b.Scope.WorkspaceID || job.Scope.AgentID != b.Scope.AgentID || job.Scope.TenantOrgID != b.Scope.TenantOrgID || job.Scope.SceneID != b.Scope.Scene.SceneID {
		return in, false, holdEmployeeNotice("source_job_scope_mismatch")
	}
	var env employeeDispatchEnvelope
	var source employeeSourceMessage
	matches := 0
	for _, item := range job.Items {
		var candidate employeeDispatchEnvelope
		if err = json.Unmarshal(item.Payload, &candidate); err != nil {
			return in, false, holdEmployeeNotice("invalid_source_envelope")
		}
		for _, message := range employeeSourceMessages(item, candidate) {
			if message.SourceRef != b.SourceRef {
				continue
			}
			if candidate.PrincipalID != item.PrincipalID || candidate.Command.EventReceiptID != item.ReceiptID || dispatchSceneID(candidate.Command) != job.Scope.SceneID || dispatchRecordedOrg(candidate.Command) != job.Scope.TenantOrgID {
				return in, false, holdEmployeeNotice("source_envelope_scope_mismatch")
			}
			if _, err := util.ParseUUID(item.PrincipalID); err != nil {
				return in, false, holdEmployeeNotice("invalid_source_principal")
			}
			if _, err := util.ParseUUID(item.ReceiptID); err != nil {
				return in, false, holdEmployeeNotice("invalid_source_receipt")
			}
			var admitted bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_event_consumption WHERE receipt_id=$1::uuid AND job_id=$2::uuid AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5 AND scene_id=$6::uuid AND principal_id=$7::uuid AND owner_loop='employee')`, item.ReceiptID, b.JobID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.Scope.Scene.SceneID, item.PrincipalID).Scan(&admitted)
			if err != nil {
				return in, false, err
			}
			if !admitted {
				return in, false, holdEmployeeNotice("source_receipt_mismatch")
			}
			env, source = candidate, message
			matches++
		}
	}
	if matches != 1 || source.RequesterRef == "" || source.RequesterRef != b.Requester {
		return in, false, holdEmployeeNotice("source_requester_mismatch")
	}
	direct, ok := service.ParseDirectTaskContext(b.Queue)
	if !ok || direct.PrincipalID != env.PrincipalID {
		return in, false, holdEmployeeNotice("queue_principal_mismatch")
	}
	var requestBody string
	err = tx.QueryRow(ctx, `SELECT body FROM employee_task_entry WHERE task_id=$1::uuid AND kind='request' ORDER BY seq LIMIT 1`, b.TaskID).Scan(&requestBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, holdEmployeeNotice("task_request_missing")
	}
	if err != nil {
		return in, false, err
	}
	var original employeeSourceMessage
	if json.Unmarshal([]byte(requestBody), &original) != nil || !reflect.DeepEqual(original, source) {
		return in, false, holdEmployeeNotice("task_source_mismatch")
	}
	if _, err := validateEmployeeCompletionNoticePolicy(b.CompletionNotice, source); err != nil {
		return in, false, holdEmployeeNotice("completion_notice_source_mismatch")
	}
	q := db.New(tx)
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(b.Scope.AgentID), WorkspaceID: parseUUID(b.Scope.WorkspaceID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, holdEmployeeNotice("agent_removed")
	}
	if err != nil {
		return in, false, err
	}
	if agent.ArchivedAt.Valid {
		return in, false, holdEmployeeNotice("agent_archived")
	}
	if _, err = q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: parseUUID(env.PrincipalID), WorkspaceID: agent.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
		return in, false, holdEmployeeNotice("principal_revoked")
	} else if err != nil {
		return in, false, err
	}
	if uuidToString(agent.OwnerID) != env.PrincipalID {
		if agent.PermissionMode != "public_to" {
			return in, false, holdEmployeeNotice("invoke_revoked")
		}
		targets, e := q.ListAgentInvocationTargets(ctx, agent.ID)
		if e != nil {
			return in, false, e
		}
		if !memberHitsInvocationTargets(targets, env.PrincipalID) {
			return in, false, holdEmployeeNotice("invoke_revoked")
		}
	}
	ep, err := q.GetAgentDispatchEndpointByEndpointID(ctx, env.EndpointID)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, holdEmployeeNotice("endpoint_revoked")
	}
	if err != nil {
		return in, false, err
	}
	if uuidToString(ep.ID) != env.EndpointNamespaceID || ep.WorkspaceID != agent.WorkspaceID || ep.AgentID != agent.ID || uuidToString(ep.ActorUserID) != env.PrincipalID {
		return in, false, holdEmployeeNotice("endpoint_binding_changed")
	}
	guard := *h
	guard.Queries = q
	registered, err := employeeSceneFence(ctx, &guard, job)
	if errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
		return in, false, holdEmployeeNotice("tenant_revoked")
	}
	if err != nil {
		return in, false, err
	}
	if registered.SceneKind != scene.KindGroup && registered.SceneKind != scene.KindDM {
		return in, false, holdEmployeeNotice("unsupported_scene_kind")
	}
	identity, err := q.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, holdEmployeeNotice("identity_removed")
	}
	if err != nil {
		return in, false, err
	}
	command := env.Command
	if command.ExternalIdentity.DWS == nil || command.ExternalIdentity.DWS.UID != identity.DwsUid || command.ExternalIdentity.DWS.OrgID != b.Scope.TenantOrgID {
		return in, false, holdEmployeeNotice("identity_changed")
	}
	sender := source.Message.SenderOpenDingTalkID
	if registered.SceneKind != scene.KindGroup && sender == "" {
		return in, false, holdEmployeeNotice("requester_target_unresolved")
	}
	callbackRoute := command.CompletionCallback != nil
	if callbackRoute {
		// A native dwsn callback has its own stable target even on a deployment
		// without an external Router. Derive both targets using the same restore
		// contract as the original dispatch; only Router targets rotate with it.
		frozenTarget := completionTargetFor(command.CompletionCallback.URL, env.TargetIdentity)
		currentTarget := completionTargetFor(command.CompletionCallback.URL, h.TaskCompletionTargetIdentity)
		if frozenTarget == "" || currentTarget != frozenTarget {
			return in, false, holdEmployeeNotice("router_target_changed")
		}
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT input FROM response_route WHERE callback_url=$1 AND workspace_id=$2::uuid AND agent_id=$3::uuid`, command.CompletionCallback.URL, b.Scope.WorkspaceID, b.Scope.AgentID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return in, false, holdEmployeeNotice("response_route_missing")
		}
		if err != nil {
			return in, false, err
		}
		if json.Unmarshal(raw, &in) != nil || in.WorkspaceID != b.Scope.WorkspaceID || in.AgentID != b.Scope.AgentID || in.SceneID != b.Scope.Scene.SceneID || in.DWSUID != identity.DwsUid || in.DWSOrgID != b.Scope.TenantOrgID || in.ConversationID != registered.ExternalSceneID || in.IsGroup != (registered.SceneKind == scene.KindGroup) || in.CallbackURL != command.CompletionCallback.ResponseURL || in.CallbackTarget != frozenTarget {
			return in, false, holdEmployeeNotice("response_route_mismatch")
		}
		if !in.IsGroup && in.SenderOpenDingTalkID != sender {
			return in, false, holdEmployeeNotice("response_requester_mismatch")
		}
		in.RequestID = "employee-run:" + b.RunID
		in.TaskID = b.QueueID
		in.ActionID = ""
		in.IssueID = ""
		in.CloseState = ""
		in.ReplyToOpenMsgID = source.Message.OpenMsgID
	} else {
		in = dingtalkresponse.ActionInput{WorkspaceID: b.Scope.WorkspaceID, AgentID: b.Scope.AgentID, DWSUID: identity.DwsUid, DWSOrgID: b.Scope.TenantOrgID, SceneID: b.Scope.Scene.SceneID, ConversationID: registered.ExternalSceneID, IsGroup: registered.SceneKind == scene.KindGroup, DWSEnvironment: commandDWSEnvironment(command)}
		if command.ResponsePolicy != nil {
			in.ShowAITag = command.ResponsePolicy.ShowAITag
		}
	}
	in.SenderOpenDingTalkID = sender
	in.EmployeeRunNoticeID = b.RunID
	return in, callbackRoute, nil
}

func employeeNoticeBody(b employeeNoticeBinding) string {
	result := strings.TrimSpace(redact.Text(b.Result))
	switch b.ResultState {
	case "succeeded":
		if result == "" {
			return "本次执行已完成，未返回文本结果。"
		}
		return "本次执行结果：\n" + result
	case "failed":
		if result == "" {
			return "本次执行失败，未返回失败详情。"
		}
		return "本次执行失败。\n" + result
	default:
		return "已记录本次执行的取消请求；运行进程是否退出尚未确认。"
	}
}

func (h *Handler) enqueueEmployeeRunNotice(ctx context.Context, workspaceID, runID string) (bool, error) {
	checkedFiles := employeeFileChecks{}
	artifacts := []EmployeeTaskArtifactRef{}
	artifactsLoaded := h.EmployeeRunNoticeArtifacts == nil
	for {
		if err := h.employeeNoticeReplicasReady(ctx); err != nil {
			return false, err
		}
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return false, err
		}
		defer tx.Rollback(ctx)
		b, err := h.loadEmployeeNoticeBinding(ctx, tx, workspaceID, runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		var held *employeeNoticeHold
		if err != nil && !errors.As(err, &held) {
			return false, err
		}
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_run_notice WHERE run_id=$1::uuid)`, runID).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
		var in dingtalkresponse.ActionInput
		callbackRoute := false
		if held == nil {
			in, callbackRoute, err = h.employeeNoticeTarget(ctx, tx, b)
			if err != nil && !errors.As(err, &held) {
				return false, err
			}
		}
		deliveryDecision := "notify"
		if held == nil {
			var lookup []string
			deliveryDecision, lookup, err = h.employeeNoticeDeliveryDecision(ctx, tx, b, in, checkedFiles)
			if err != nil {
				return false, err
			}
			if deliveryDecision == "wait" {
				return false, nil
			}
			if len(lookup) > 0 {
				if err = tx.Rollback(ctx); err != nil {
					return false, err
				}
				if err = h.verifyEmployeeNoticeFiles(ctx, in, b.QueueID, lookup, checkedFiles); err != nil {
					return false, err
				}
				continue
			}
			if deliveryDecision == "suppress" {
				held = &employeeNoticeHold{"native_file_delivered"}
			}
		}
		// The artifact reader may use this same pool. Release aggregate locks and
		// the connection before calling it, then repeat the complete authority and
		// idempotency checks in a fresh transaction before writing either outbox.
		if held == nil && !artifactsLoaded {
			if err = tx.Rollback(ctx); err != nil {
				return false, err
			}
			artifacts, err = h.EmployeeRunNoticeArtifacts(ctx, b.Scope, b.TaskID, b.RunID)
			if err != nil {
				return false, err
			}
			artifactsLoaded = true
			continue
		}
		state, reason, body, actionID := "enqueued", "", "", ""
		if held != nil {
			state, reason = "suppressed", held.reason
		} else {
			body = employeeNoticeDeliveryBody(deliveryDecision, b.Result)
			if body == "" {
				body = employeeNoticeBody(b)
			}
			for _, artifact := range artifacts {
				if artifact.TaskID != b.TaskID || artifact.RunID != b.RunID || artifact.QueueTaskID != b.QueueID {
					return false, errors.New("employee notice artifact run mismatch")
				}
				// A saved ArtifactRef is not a native DingTalk file receipt. Its
				// authenticated URL is not necessarily readable by an external UID.
				body += "\n已保存产物：" + strings.ReplaceAll(artifact.Filename, "\n", " ") + "（需授权访问）"
			}
			in.Text = body
			if callbackRoute {
				actionID, err = h.DingTalkResponses.Enqueue(ctx, tx, in)
			} else {
				actionID, err = h.DingTalkResponses.EnqueueSceneNotice(ctx, tx, in, b.RunID)
			}
			if err != nil {
				return false, err
			}
		}
		if artifacts == nil {
			artifacts = []EmployeeTaskArtifactRef{}
		}
		raw, _ := json.Marshal(artifacts)
		_, err = tx.Exec(ctx, `INSERT INTO employee_run_notice(run_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,queue_task_id,job_id,source_ref,requester_ref,result_state,state,reason,body,artifacts,action_id)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,NULLIF($8,'')::uuid,$9,$10,$11,$12,$13,$14,$15,NULLIF($16,''))`, b.RunID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.Scope.Scene.SceneID, b.TaskID, b.QueueID, b.JobID, b.SourceRef, b.Requester, b.ResultState, state, reason, body, raw, actionID)
		if err != nil {
			return false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		logEmployeeRunNotice(ctx, "employee_run_notice_recorded", b, state, reason, actionID)
		return true, nil
	}
}

// BeforeEmployeeRunNoticeSend affects only actions already linked to a Run
// notice. It fences a fresh submission; provider-accepted/unknown actions never
// call it and continue the existing query/receipt reconciliation path.
func (h *Handler) BeforeEmployeeRunNoticeSend(ctx context.Context, in dingtalkresponse.ActionInput) error {
	if h == nil || h.DB == nil || h.TxStarter == nil {
		return errors.New("employee notice authority is unavailable")
	}
	var runID, workspaceID, state, reason, body string
	var recorded employeeNoticeBinding
	err := h.DB.QueryRow(ctx, `SELECT run_id::text,workspace_id::text,state,reason,body,agent_id::text,scene_id::text,task_id::text,queue_task_id::text,COALESCE(job_id::text,''),result_state FROM employee_run_notice WHERE action_id=$1`, in.ActionID).Scan(&runID, &workspaceID, &state, &reason, &body, &recorded.Scope.AgentID, &recorded.Scope.Scene.SceneID, &recorded.TaskID, &recorded.QueueID, &recorded.JobID, &recorded.ResultState)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.EmployeeRunNoticeID != "" {
			return &dingtalkresponse.SuppressSendError{Reason: "notice_binding_removed"}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if state == "suppressed" {
		return &dingtalkresponse.SuppressSendError{Reason: reason}
	}
	recorded.RunID, recorded.Scope.WorkspaceID = runID, workspaceID
	checkedFiles := employeeFileChecks{}
	for {
		if err := h.employeeNoticeReplicasReady(ctx); err != nil {
			return err
		}
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		b, err := h.loadEmployeeNoticeBinding(ctx, tx, workspaceID, runID)
		var held *employeeNoticeHold
		if errors.Is(err, pgx.ErrNoRows) {
			held = &employeeNoticeHold{"run_binding_removed"}
		} else if err != nil && !errors.As(err, &held) {
			return err
		}
		if held == nil {
			target, callbackRoute, e := h.employeeNoticeTarget(ctx, tx, b)
			if e != nil && !errors.As(e, &held) {
				return e
			}
			if held == nil && in.Text != body {
				reloaded := in
				reloaded.Text = body
				if employeeNoticeActionMatches(reloaded, target, b.RunID, body, callbackRoute) {
					return errors.New("employee notice text changed; reload pending action")
				}
			}
			if held == nil && !employeeNoticeActionMatches(in, target, b.RunID, body, callbackRoute) {
				held = &employeeNoticeHold{"notice_action_mismatch"}
			}
		}
		if held == nil {
			decision, lookup, e := h.employeeNoticeDeliveryDecision(ctx, tx, b, in, checkedFiles)
			if e != nil {
				return e
			}
			if decision == "wait" {
				return errors.New("native file send receipt is still being verified")
			}
			if len(lookup) > 0 {
				if err = tx.Rollback(ctx); err != nil {
					return err
				}
				if err = h.verifyEmployeeNoticeFiles(ctx, in, b.QueueID, lookup, checkedFiles); err != nil {
					return err
				}
				continue
			}
			if newBody := employeeNoticeDeliveryBody(decision, b.Result); newBody != "" && newBody != body {
				if err = refreshEmployeeNoticeBody(ctx, tx, in, runID, body, newBody); err != nil {
					return err
				}
				if err = tx.Commit(ctx); err != nil {
					return err
				}
				return errors.New("employee notice body refreshed; reload before submission")
			}
			if decision == "suppress" {
				held = &employeeNoticeHold{"native_file_delivered"}
			}
		}
		if held == nil {
			return tx.Commit(ctx)
		}

		changed, err := tx.Exec(ctx, `UPDATE employee_run_notice SET state='suppressed',reason=$2,updated_at=now() WHERE run_id=$1::uuid AND state='enqueued'`, runID, held.reason)
		if err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		if changed.RowsAffected() == 1 {
			logEmployeeRunNotice(ctx, "employee_run_notice_state_changed", recorded, "suppressed", held.reason, in.ActionID)
		}
		return &dingtalkresponse.SuppressSendError{Reason: held.reason}

	}
}

func employeeNoticeActionMatches(in, target dingtalkresponse.ActionInput, runID, body string, callbackRoute bool) bool {
	if in.EmployeeRunNoticeID != runID || in.WorkspaceID != target.WorkspaceID ||
		in.AgentID != target.AgentID || in.SceneID != target.SceneID ||
		in.DWSOrgID != target.DWSOrgID || in.DWSUID != target.DWSUID ||
		in.ConversationID != target.ConversationID || in.IsGroup != target.IsGroup ||
		in.DWSEnvironment != target.DWSEnvironment || in.SenderOpenDingTalkID != target.SenderOpenDingTalkID ||
		in.Text != body || in.IssueID != "" || in.CloseState != "" ||
		in.RoutineRunID != "" || in.CoordinatorWaitJobID != "" {
		return false
	}
	if callbackRoute {
		return in.CallbackURL == target.CallbackURL && in.CallbackTarget == target.CallbackTarget &&
			in.ReplyToOpenMsgID == target.ReplyToOpenMsgID && in.RequestID == target.RequestID &&
			in.TaskID == target.TaskID && in.SceneNoticeID == ""
	}
	return in.SceneNoticeID == runID && in.RequestID == "scene-notice:"+runID &&
		in.CallbackURL == "" && in.TaskID == "" && in.ReplyToOpenMsgID == ""
}

// Log only committed state and correlation IDs. Task output, source quotes and
// artifact URLs stay in their existing access-controlled records.
func logEmployeeRunNotice(ctx context.Context, event string, b employeeNoticeBinding, state, reason, actionID string) {
	slog.InfoContext(ctx, "employee run notice", "event", event,
		"state", state, "reason", reason, "result_state", b.ResultState,
		"workspace_id", b.Scope.WorkspaceID, "agent_id", b.Scope.AgentID,
		"scene_id", b.Scope.Scene.SceneID, "job_id", b.JobID,
		"task_id", b.TaskID, "run_id", b.RunID, "queue_task_id", b.QueueID,
		"action_id", actionID)
}
