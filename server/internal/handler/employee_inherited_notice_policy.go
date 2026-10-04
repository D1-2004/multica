package handler

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) employeeNoticePolicySource(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding, source employeeSourceMessage, principal string) error {
	if b.ResultState != "succeeded" {
		_, _, err := h.employeeTaskNoticeCommitment(ctx, tx, b, principal, false)
		return err
	}
	policy, origin, err := service.InheritDirectTaskNoticePolicy(b.Queue)
	if err != nil || policy != b.CompletionNotice {
		return holdEmployeeNotice("completion_notice_frozen_mismatch")
	}
	committed, committedOrigin, err := h.employeeTaskNoticeCommitment(ctx, tx, b, principal, true)
	if err != nil {
		return err
	}
	if committed != policy || committedOrigin != nil && (origin == nil || *committedOrigin != *origin) {
		return holdEmployeeNotice("completion_notice_commitment_mismatch")
	}
	if b.CompletionNoticeOrigin == nil {
		if _, err := validateEmployeeCompletionNoticePolicy(policy, source); err != nil {
			return holdEmployeeNotice("completion_notice_source_mismatch")
		}
		return nil
	}
	if origin == nil || *origin != *b.CompletionNoticeOrigin {
		return holdEmployeeNotice("completion_notice_origin_mismatch")
	}
	_, err = h.employeeInheritedNoticeSource(ctx, tx, b.Scope, b.TaskID, b.Requester, principal, policy, *origin)
	return err
}

// The root locator is deliberately flat. Verify its original admission and
// checkpoint, independently of a later steer that moved this Run's input_seq.
func (h *Handler) employeeInheritedNoticeSource(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, taskID, requester, principal string, policy employeetask.CompletionNoticePolicy, origin service.DirectTaskNoticeOrigin) (employeeSourceMessage, error) {
	return h.employeeAcceptedNoticeSource(ctx, tx, scope, taskID, requester, principal, &policy, origin)
}

// A nil policy verifies an accepted delivery anchor independently of any quiet
// exemption. A policy additionally verifies the original explicit exemption.
func (h *Handler) employeeAcceptedNoticeSource(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, taskID, requester, principal string, policy *employeetask.CompletionNoticePolicy, origin service.DirectTaskNoticeOrigin) (employeeSourceMessage, error) {
	var source employeeSourceMessage
	invalid := func() (employeeSourceMessage, error) {
		return source, holdEmployeeNotice("completion_notice_origin_mismatch")
	}
	if origin.Version != 1 || origin.TaskID != taskID || policy != nil && (origin.SourceRef != policy.SourceRef || policy.Mode != employeetask.CompletionNoticeIfNotDelivered) {
		return invalid()
	}
	queueID, err := util.ParseUUID(origin.QueueTaskID)
	if err != nil {
		return invalid()
	}
	queue, err := db.New(tx).GetAgentTask(ctx, queueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return source, err
	}
	var fields struct {
		Input json.RawMessage `json:"employee_direct_input"`
	}
	var frozen struct {
		employeeExecutionContext
		Policy json.RawMessage `json:"employee_completion_notice_policy"`
		Origin json.RawMessage `json:"employee_completion_notice_origin"`
	}
	if json.Unmarshal(queue.Context, &fields) != nil || json.Unmarshal(fields.Input, &frozen) != nil || frozen.SourceRef != origin.SourceRef || frozen.Owner != "employee" || frozen.Scene.SceneID != scope.Scene.SceneID {
		return invalid()
	}
	// Delivery authorization does not depend on success-only quiet metadata.
	// Decode it only while proving an actual quiet exemption.
	if policy != nil {
		var acceptedPolicy employeetask.CompletionNoticePolicy
		var acceptedOrigin *service.DirectTaskNoticeOrigin
		if json.Unmarshal(frozen.Policy, &acceptedPolicy) != nil || acceptedPolicy != *policy {
			return invalid()
		}
		if len(frozen.Origin) > 0 && (json.Unmarshal(frozen.Origin, &acceptedOrigin) != nil || acceptedOrigin != nil) {
			return invalid()
		}
	}
	queue.Context = fields.Input
	direct, valid := service.ParseDirectTaskContext(queue)
	if !valid || direct.EmployeeTaskID != taskID || direct.WorkspaceID != scope.WorkspaceID || direct.PrincipalID != principal || uuidToString(queue.AgentID) != scope.AgentID {
		return invalid()
	}
	var runID, runKey, runQueue string
	var startedSeq, revision int64
	err = tx.QueryRow(ctx, `SELECT r.id::text,e.source_key,e.payload->>'queue_task_id',e.seq,e.goal_revision
 FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 JOIN employee_task_entry e ON e.task_id=t.id AND e.workspace_id=t.workspace_id AND e.agent_id=t.agent_id AND e.tenant_org_id=t.tenant_org_id AND e.run_id=r.id AND e.kind='run_started' AND e.source_namespace='employee_scene'
 WHERE r.queue_task_id=$1 AND t.id=$2::uuid AND t.workspace_id=$3::uuid AND t.agent_id=$4::uuid AND t.tenant_org_id=$5 AND t.scene_id=$6::uuid AND t.requester_ref=$7 AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'`, queueID, taskID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, requester).Scan(&runID, &runKey, &runQueue, &startedSeq, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return source, err
	}
	if runQueue != origin.QueueTaskID {
		return invalid()
	}
	if _, err := util.ParseUUID(frozen.JobID); err != nil {
		return invalid()
	}
	var job employeeentry.Job
	err = tx.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state FROM employee_scene_job WHERE id=$1::uuid`, frozen.JobID).Scan(&job.Scope.WorkspaceID, &job.Scope.AgentID, &job.Scope.TenantOrgID, &job.Scope.SceneID, &job.PrincipalID, &job.Items, &job.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return source, err
	}
	if job.Scope != (employeeentry.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.Scene.SceneID}) || job.PrincipalID != principal || job.State != "completed" {
		return invalid()
	}
	matches := 0
	for _, item := range job.Items {
		var env employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &env) != nil {
			return invalid()
		}
		for _, candidate := range employeeSourceMessages(item, env) {
			if candidate.SourceRef != origin.SourceRef {
				continue
			}
			if item.PrincipalID != principal || env.PrincipalID != principal || env.Command.EventReceiptID != item.ReceiptID || dispatchSceneID(env.Command) != scope.Scene.SceneID || dispatchRecordedOrg(env.Command) != scope.TenantOrgID || candidate.RequesterRef != requester {
				return invalid()
			}
			source = candidate
			matches++
		}
	}
	if matches != 1 {
		return invalid()
	}
	if _, err := util.ParseUUID(source.ReceiptID); err != nil {
		return invalid()
	}
	var admitted bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_event_consumption c JOIN scene_event_receipt r ON r.id=c.receipt_id AND r.workspace_id=c.workspace_id AND r.agent_id=c.agent_id AND r.tenant_org_id=c.tenant_org_id AND r.scene_id=c.scene_id AND r.principal_id=c.principal_id
 WHERE c.receipt_id=$1::uuid AND c.job_id=$2::uuid AND c.workspace_id=$3::uuid AND c.agent_id=$4::uuid AND c.tenant_org_id=$5 AND c.scene_id=$6::uuid AND c.principal_id=$7::uuid AND c.owner_loop='employee' AND c.state='completed' AND r.reason='' AND ((r.route='unified' AND r.state='ready') OR (r.route='legacy' AND r.state='legacy')))`, source.ReceiptID, frozen.JobID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.Scene.SceneID, principal).Scan(&admitted)
	if err != nil {
		return source, err
	}
	prefix := source.ReceiptID + "/"
	if !admitted || !strings.HasPrefix(runKey, prefix) || !strings.HasSuffix(runKey, "/run") {
		return invalid()
	}
	callID := strings.TrimSuffix(strings.TrimPrefix(runKey, prefix), "/run")
	if callID == "" {
		return invalid()
	}
	var journal []byte
	if err = tx.QueryRow(ctx, `SELECT tool_journal->$2 FROM employee_scene_job WHERE id=$1::uuid`, frozen.JobID, callID).Scan(&journal); err != nil {
		return source, err
	}
	var saved struct {
		Input  employeeloop.ToolCall `json:"input"`
		Result employeeToolRecord    `json:"result"`
	}
	if json.Unmarshal(journal, &saved) != nil || saved.Input.NativeToolCallID != callID || saved.Input.Arguments["source_ref"] != origin.SourceRef || saved.Result.Failure != "" || saved.Result.Result.Receipt != runID || saved.Result.Result.Terminal == nil || saved.Result.Result.Terminal.Kind != employeeloop.Dispatched {
		return invalid()
	}
	var ids struct {
		TaskID  string `json:"task_id"`
		RunID   string `json:"run_id"`
		QueueID string `json:"queue_task_id"`
	}
	if json.Unmarshal([]byte(saved.Result.Result.Content), &ids) != nil || ids.TaskID != taskID || ids.RunID != runID || ids.QueueID != origin.QueueTaskID {
		return invalid()
	}
	if policy != nil {
		acceptedPolicy, err := employeeCompletionNoticePolicy(saved.Input.Arguments, source)
		if err != nil || acceptedPolicy != *policy {
			return invalid()
		}
	}
	kind, key := "request", prefix+callID+"/definition"
	if saved.Input.Name == "continue_task" {
		kind, key = "resumed", prefix+callID+"/resume"
	} else if saved.Input.Name != "dispatch_task" {
		return invalid()
	}
	var body, actor string
	err = tx.QueryRow(ctx, `SELECT body,actor_ref FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind=$5 AND source_namespace='employee_scene' AND source_key=$6 AND seq<$7 AND goal_revision=$8`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, kind, key, startedSeq, revision).Scan(&body, &actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid()
	}
	if err != nil {
		return source, err
	}
	var original employeeSourceMessage
	if actor != requester || json.Unmarshal([]byte(body), &original) != nil || !reflect.DeepEqual(original, source) {
		return invalid()
	}
	return source, nil
}

// Read the accepted Task inputs, not queue copies of the quiet fields. Default
// or absent model policy never revokes an earlier explicit file-only instruction.
// The input boundary excludes promises accepted after this particular Run.
func (h *Handler) employeeTaskNoticeCommitment(ctx context.Context, tx pgx.Tx, b employeeNoticeBinding, principal string, checkPolicy bool) (employeetask.CompletionNoticePolicy, *service.DirectTaskNoticeOrigin, error) {
	if policy, origin, handled, err := h.employeeHumanNoticeCommitment(ctx, tx, b, principal); handled {
		return policy, origin, err
	}
	policy := employeetask.CompletionNoticePolicy{Mode: employeetask.CompletionNoticeAlways}
	var origin *service.DirectTaskNoticeOrigin
	rows, err := tx.Query(ctx, `SELECT e.kind,e.source_key,e.body,s.payload->>'queue_task_id',COALESCE(j.id::text,''),j.tool_journal
 FROM employee_task_entry e JOIN employee_task_entry s ON s.task_id=e.task_id AND s.workspace_id=e.workspace_id AND s.agent_id=e.agent_id AND s.tenant_org_id=e.tenant_org_id
 AND s.kind='run_started' AND s.source_namespace=e.source_namespace AND s.source_key=regexp_replace(e.source_key,'/(definition|resume)$','/run')
 LEFT JOIN employee_event_consumption c ON c.receipt_id::text=split_part(e.source_key,'/',1) AND c.workspace_id=e.workspace_id AND c.agent_id=e.agent_id AND c.tenant_org_id=e.tenant_org_id AND c.scene_id=$4::uuid AND c.owner_loop='employee' AND c.state='completed'
 LEFT JOIN employee_scene_job j ON j.id=c.job_id
 WHERE e.workspace_id=$1::uuid AND e.agent_id=$2::uuid AND e.tenant_org_id=$3 AND e.task_id=$5::uuid AND e.kind IN ('request','resumed') AND e.source_namespace='employee_scene'
 AND e.seq<=(SELECT input_seq FROM employee_task_run WHERE id=$6::uuid AND task_id=e.task_id AND workspace_id=e.workspace_id AND agent_id=e.agent_id AND tenant_org_id=e.tenant_org_id) ORDER BY e.seq`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.Scope.Scene.SceneID, b.TaskID, b.RunID)
	if err != nil {
		return policy, nil, err
	}
	type accepted struct {
		kind, key, body, queue, job string
		journal                     []byte
	}
	var inputs []accepted
	for rows.Next() {
		var in accepted
		if err = rows.Scan(&in.kind, &in.key, &in.body, &in.queue, &in.job, &in.journal); err != nil {
			break
		}
		inputs = append(inputs, in)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return policy, nil, err
	}
	deliveryProved := false
	for _, in := range inputs {
		if !checkPolicy && (in.job != b.JobID || !strings.HasPrefix(in.key, strings.SplitN(b.SourceRef, "/", 2)[0]+"/")) {
			continue
		}
		var source employeeSourceMessage
		if json.Unmarshal([]byte(in.body), &source) != nil {
			return policy, nil, holdEmployeeNotice("completion_notice_source_mismatch")
		}
		isDelivery := source.SourceRef == b.SourceRef && in.job == b.JobID
		if !checkPolicy && !isDelivery {
			continue
		}
		prefix, suffix, tool := source.ReceiptID+"/", "/definition", "dispatch_task"
		if in.kind == "resumed" {
			suffix, tool = "/resume", "continue_task"
		}
		if !strings.HasPrefix(in.key, prefix) || !strings.HasSuffix(in.key, suffix) {
			return policy, nil, holdEmployeeNotice("completion_notice_source_mismatch")
		}
		callID := strings.TrimSuffix(strings.TrimPrefix(in.key, prefix), suffix)
		var journal map[string]json.RawMessage
		var saved struct {
			Input  employeeloop.ToolCall `json:"input"`
			Result employeeToolRecord    `json:"result"`
		}
		if json.Unmarshal(in.journal, &journal) != nil || json.Unmarshal(journal[callID], &saved) != nil || saved.Input.Name != tool || saved.Input.NativeToolCallID != callID || saved.Input.Arguments["source_ref"] != source.SourceRef || saved.Result.Failure != "" {
			return policy, nil, holdEmployeeNotice("completion_notice_source_mismatch")
		}
		candidate := service.DirectTaskNoticeOrigin{Version: 1, TaskID: b.TaskID, QueueTaskID: in.queue, SourceRef: source.SourceRef}
		if isDelivery {
			if _, err := h.employeeAcceptedNoticeSource(ctx, tx, b.Scope, b.TaskID, b.Requester, principal, nil, candidate); err != nil {
				return policy, nil, err
			}
			deliveryProved = true
		}
		if !checkPolicy {
			continue
		}
		declared, err := employeeCompletionNoticePolicy(saved.Input.Arguments, source)
		if err != nil {
			return policy, nil, holdEmployeeNotice("completion_notice_source_mismatch")
		}
		if declared.Mode == employeetask.CompletionNoticeIfNotDelivered {
			if _, err := h.employeeInheritedNoticeSource(ctx, tx, b.Scope, b.TaskID, b.Requester, principal, declared, candidate); err != nil {
				return policy, nil, err
			}
			policy, origin = declared, &candidate
		}
	}
	if !deliveryProved {
		return policy, nil, holdEmployeeNotice("completion_notice_delivery_unproven")
	}
	return policy, origin, nil
}

func (h *Handler) employeeContinuationNoticePolicy(ctx context.Context, task employeetask.Task, queueID, principal string) (employeetask.CompletionNoticePolicy, *service.DirectTaskNoticeOrigin, *employeetask.PacketMaterial, error) {
	var policy employeetask.CompletionNoticePolicy
	id, err := util.ParseUUID(queueID)
	if err != nil {
		return policy, nil, nil, err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return policy, nil, nil, err
	}
	defer tx.Rollback(ctx)
	queue, err := db.New(tx).GetAgentTask(ctx, id)
	if err != nil {
		return policy, nil, nil, err
	}
	policy, origin, err := service.InheritDirectTaskNoticePolicy(queue)
	if err != nil {
		return policy, origin, nil, err
	}
	var delivery employeeExecutionContext
	if json.Unmarshal(queue.Context, &delivery) != nil {
		return policy, nil, nil, holdEmployeeNotice("completion_notice_source_mismatch")
	}
	b := employeeNoticeBinding{Scope: task.Scope, TaskID: task.ID, QueueID: queueID, Queue: queue, Requester: task.RequesterRef, JobID: delivery.JobID, SourceRef: delivery.SourceRef}
	if err = tx.QueryRow(ctx, `SELECT id::text,goal_revision FROM employee_task_run WHERE queue_task_id=$1 AND task_id=$2::uuid AND workspace_id=$3::uuid`, id, task.ID, task.Scope.WorkspaceID).Scan(&b.RunID, &b.RunGoalRevision); err != nil {
		return policy, nil, nil, err
	}
	committed, committedOrigin, err := h.employeeTaskNoticeCommitment(ctx, tx, b, principal, true)
	if err != nil {
		return policy, nil, nil, err
	}
	if committed != policy || committedOrigin != nil && (origin == nil || *committedOrigin != *origin) {
		return policy, nil, nil, holdEmployeeNotice("completion_notice_commitment_mismatch")
	}
	if origin == nil {
		return policy, nil, nil, nil
	}
	source, err := h.employeeInheritedNoticeSource(ctx, tx, task.Scope, task.ID, task.RequesterRef, principal, policy, *origin)
	if err != nil {
		return policy, nil, nil, err
	}
	body, _ := json.Marshal(source)
	return policy, origin, &employeetask.PacketMaterial{Ref: source.SourceRef, Scope: task.Scope, PrincipalID: principal, Body: string(body)}, nil
}
