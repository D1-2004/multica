package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const employeeExecutionSource = "employee.execution"
const employeeExecutionSchema = "employee.execution/1"

// Keep the version-1 storage wrapper recognizable to old readers. Proof version
// 3 also verifies explicit continuations using their committed resume input.
// Version 2 accepts resolved legacy receipts as well as unified receipts. Old readers
// skip either wrapper; new readers reassess only older proofs, never downgrade
// a newer proof or require a foreground replica-marker change.
const employeeExecutionProofVersion = 3

// employeeExecutionTerminal is a fact, never a continuation or a user message.
// The output remains on Run/queue; the envelope contains only durable references.
type employeeExecutionTerminal struct {
	TaskID          string `json:"task_id"`
	RunID           string `json:"run_id"`
	QueueTaskID     string `json:"queue_task_id"`
	GoalRevision    int64  `json:"goal_revision"`
	State           string `json:"state"`
	ResultRef       string `json:"result_ref"`
	SceneID         string `json:"scene_id"`
	JobID           string `json:"job_id"`
	SourceReceiptID string `json:"source_receipt_id"`
}

type employeeExecutionBinding struct {
	employeeExecutionTerminal
	Scope               employeeentry.Scope
	CurrentGoalRevision int64
	FinishedAt          time.Time
	Requester           string
	Queue               db.AgentTaskQueue
	PrincipalID         pgtype.UUID
	SourceRef           string
	Original            db.SceneEventReceipt
	Source              employeeSourceMessage
	Continuation        bool
}

type employeeExecutionContext struct {
	JobID     string    `json:"employee_job_id"`
	SourceRef string    `json:"employee_source_ref"`
	Owner     string    `json:"employee_delivery_owner"`
	Scene     scene.Ref `json:"agent_scene"`
}

// ReconcileEmployeeExecutionEvents records terminal facts independently of the
// model and notice workers. The receipt and consumption commit atomically; an
// old replica sees no new job to claim. The original notice owner is unchanged.
// This is deliberately not ReconcileEmployeeRuns' still-running repair query.

func (h *Handler) ReconcileEmployeeExecutionEvents(ctx context.Context, limit int) (int, error) {
	if h == nil || h.DB == nil || h.TxStarter == nil || limit < 1 || limit > 1000 {
		return 0, errors.New("invalid employee execution reconciliation")
	}
	rows, err := h.DB.Query(ctx, `SELECT t.workspace_id::text,r.id::text FROM employee_task_run r
 JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 JOIN agent_task_queue q ON q.id=r.queue_task_id AND q.agent_id=t.agent_id
 WHERE t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'
 AND r.state IN ('succeeded','failed','cancelled') AND q.status IN ('completed','failed','cancelled')
 AND NOT EXISTS(SELECT 1 FROM employee_scene_job j WHERE j.id::text=q.context->>'employee_job_id' AND j.workspace_id=t.workspace_id AND j.agent_id=t.agent_id AND j.tenant_org_id=t.tenant_org_id AND j.scene_id=t.scene_id AND j.state<>'completed')
 AND NOT EXISTS(SELECT 1 FROM scene_event_receipt e WHERE e.workspace_id=t.workspace_id AND e.agent_id=t.agent_id AND e.source=$1 AND e.source_event_id=r.id::text)
 AND NOT COALESCE(q.context->'employee_execution_event_skip' @> jsonb_build_object('version',1,'run_id',r.id::text)
  AND jsonb_typeof(q.context->'employee_execution_event_skip'->'proof_version')='number'
  AND q.context->'employee_execution_event_skip'->'proof_version'>=to_jsonb($3::int)
  AND q.context->'employee_execution_event_skip'->>'reason'<>'',false)
 ORDER BY r.finished_at,r.id LIMIT $2`, employeeExecutionSource, limit, employeeExecutionProofVersion)
	if err != nil {
		return 0, err
	}
	type candidate struct{ workspace, run string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.workspace, &c.run); err != nil {
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
	var failures []error
	for _, c := range candidates {
		created, err := h.recordEmployeeExecutionEvent(ctx, c.workspace, c.run)
		if err != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			failures = append(failures, fmt.Errorf("employee execution run %s: %w", c.run, err))
			continue
		}
		if created {
			count++
		}
	}
	return count, errors.Join(failures...)
}

func (h *Handler) recordEmployeeExecutionEvent(ctx context.Context, workspaceID, runID string) (bool, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var parent string
	if err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&parent); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var b employeeExecutionBinding
	var finished pgtype.Timestamptz
	err = tx.QueryRow(ctx, `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,t.requester_ref,t.goal_revision,r.id::text,r.queue_task_id::text,r.goal_revision,r.state,r.result_ref,r.finished_at
 FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 WHERE t.workspace_id=$1::uuid AND r.id=$2::uuid AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene' AND r.state IN ('succeeded','failed','cancelled')`, workspaceID, runID).Scan(&b.Scope.WorkspaceID, &b.Scope.AgentID, &b.Scope.TenantOrgID, &b.Scope.SceneID, &b.TaskID, &b.Requester, &b.CurrentGoalRevision, &b.RunID, &b.QueueTaskID, &b.GoalRevision, &b.State, &b.ResultRef, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b.SceneID = b.Scope.SceneID
	b.FinishedAt = finished.Time
	// Match message admission's workspace -> scene lock order before Resolve
	// touches the scene directory. Never hold a Task lock while taking this lock.
	var sceneID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent_scene WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid FOR UPDATE`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.Scope.SceneID).Scan(&sceneID)
	sceneMissing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !sceneMissing {
		return false, err
	}
	q := db.New(tx)
	b.Queue, err = q.GetAgentTask(ctx, parseUUID(b.QueueTaskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Recheck after the scene lock and queue reload: a competing reconciler may
	// have committed a receipt or a newer rejection while this candidate waited.
	var settled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM scene_event_receipt WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND source=$3 AND source_event_id=$4)
 OR COALESCE($5::jsonb->'employee_execution_event_skip' @> jsonb_build_object('version',1,'run_id',$4::text)
  AND jsonb_typeof($5::jsonb->'employee_execution_event_skip'->'proof_version')='number'
  AND $5::jsonb->'employee_execution_event_skip'->'proof_version'>=to_jsonb($6::int)
  AND $5::jsonb->'employee_execution_event_skip'->>'reason'<>'',false)`, workspaceID, b.Scope.AgentID, employeeExecutionSource, runID, b.Queue.Context, employeeExecutionProofVersion).Scan(&settled); err != nil {
		return false, err
	}
	if settled {
		return false, nil
	}
	reason, err := h.employeeExecutionOrigin(ctx, tx, &b)
	if err != nil {
		return false, err
	}
	if reason == "source_job_pending" {
		return false, nil
	}
	if reason != "" {
		// No verified source principal exists for these historical/corrupt rows.
		// Record only a local skip marker; never invent an admission identity.
		skip := map[string]any{"version": 1, "proof_version": employeeExecutionProofVersion, "run_id": b.RunID, "reason": reason}
		raw, _ := json.Marshal(skip)
		tag, err := tx.Exec(ctx, `UPDATE agent_task_queue q SET context=jsonb_set(q.context,'{employee_execution_event_skip}',$2::jsonb,true)
 WHERE q.id=$1::uuid AND q.agent_id=$3::uuid AND q.context=$5::jsonb AND q.status=$7
 AND NOT COALESCE(q.context->'employee_execution_event_skip' @> jsonb_build_object('version',1,'run_id',$4::text)
  AND jsonb_typeof(q.context->'employee_execution_event_skip'->'proof_version')='number'
  AND q.context->'employee_execution_event_skip'->'proof_version'>=to_jsonb($9::int)
  AND q.context->'employee_execution_event_skip'->>'reason'<>'',false)
 AND EXISTS(SELECT 1 FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 WHERE r.id=$4::uuid AND r.queue_task_id=q.id AND r.agent_id=q.agent_id AND r.workspace_id=$6::uuid AND r.state=$8
 AND r.state IN ('succeeded','failed','cancelled') AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene')`, b.QueueTaskID, raw, b.Scope.AgentID, b.RunID, b.Queue.Context, b.Scope.WorkspaceID, b.Queue.Status, b.State, employeeExecutionProofVersion)
		if err != nil {
			return false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return false, err
		}
		if tag.RowsAffected() > 0 {
			h.logEmployeeExecutionEvent(ctx, b, "", "skipped", reason, time.Now())
		}
		return tag.RowsAffected() > 0, nil
	}
	// Scene precedes Task in message/tool admission. Refresh under a shared Task
	// lock so a correction committed while waiting cannot be labelled current.
	if err = tx.QueryRow(ctx, `SELECT goal_revision FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid FOR SHARE`, b.TaskID, b.Scope.WorkspaceID).Scan(&b.CurrentGoalRevision); err != nil {
		return false, err
	}
	state, reason := "completed", "current_goal_revision"
	if b.GoalRevision != b.CurrentGoalRevision {
		reason = "historical_goal_revision"
	}
	owner := scene.Owner{WorkspaceID: parseUUID(b.Scope.WorkspaceID), AgentID: parseUUID(b.Scope.AgentID)}
	host := eventrouter.Host{Owner: owner, PrincipalID: b.PrincipalID, TenantOrgID: b.Scope.TenantOrgID, Route: b.Original.Route, ConfigVersion: employeeExecutionSchema}
	if sceneMissing {
		state, reason = "held", "scene_unavailable"
	} else {
		// This source is a digital employee: retain the missing-identity hold
		// rather than borrowing agentTenantOrg's identity-less robot fallback.
		// Keep the binding stable through fencedScene's second identity read.
		var identityAgent pgtype.UUID
		err := tx.QueryRow(ctx, `SELECT agent_id FROM agent_dingtalk_identity WHERE workspace_id=$1 AND agent_id=$2 FOR SHARE`, owner.WorkspaceID, owner.AgentID).Scan(&identityAgent)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			state, reason = "held", "scene_tenant_unavailable"
		} else {
			sc, err := fencedScene(ctx, q, &scene.Ref{SceneID: sceneID}, owner, b.Scope.TenantOrgID)
			switch {
			case errors.Is(err, scene.ErrNotFound), errors.Is(err, scene.ErrStaleTenant), errors.Is(err, scene.ErrUnresolved):
				state, reason = "held", "scene_tenant_unavailable"
			case err != nil:
				return false, err
			default:
				host.Locator = scene.Locator{Provider: sc.Provider, TenantOrgID: sc.TenantOrgID, Namespace: sc.SourceNamespace, Kind: sc.SceneKind, ExternalID: sc.ExternalSceneID}
				host.Observation = scene.Observation{ActiveAt: b.FinishedAt}
			}
		}
	}
	if state == "held" {
		host.UnmappedReason = reason
	}
	payload, err := json.Marshal(b.employeeExecutionTerminal)
	if err != nil {
		return false, err
	}
	ev := eventrouter.Event{Version: eventrouter.Version, ID: b.RunID, Source: employeeExecutionSource, Type: "execution.terminal", Category: eventrouter.RunCallback, OccurredAt: b.FinishedAt.UTC(), PayloadSchema: employeeExecutionSchema, Payload: payload}
	raw, _ := json.Marshal(ev)
	hash := sha256.Sum256(raw)
	host.Fingerprint = hex.EncodeToString(hash[:])
	receipt, replay, err := eventrouter.AdmitWithHook(ctx, tx, ev, host, func(ctx context.Context, receiptTx pgx.Tx, r db.SceneEventReceipt) error {
		if state == "completed" && util.UUIDToString(r.SceneID) != b.SceneID {
			return errors.New("execution event scene changed")
		}
		scope := b.Scope
		scope.SceneID = util.UUIDToString(r.SceneID)
		_, _, err := employeeentry.NewStore(receiptTx).RecordExecutionFact(ctx, employeeentry.ExecutionFact{Scope: scope, ReceiptID: util.UUIDToString(r.ID), PrincipalID: util.UUIDToString(b.PrincipalID), ConfigRevision: employeeExecutionSchema, Payload: payload, State: state, Reason: reason})
		return err
	})
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if !replay {
		h.logEmployeeExecutionEvent(ctx, b, util.UUIDToString(receipt.ID), state, reason, receipt.CreatedAt.Time)
	}
	return !replay, nil
}

// employeeExecutionOrigin verifies the original source, not current invoke
// permission. Recording a fact must not impersonate a newly selected principal
// or loop forever after a member is removed. No result body is copied or exported.
func (h *Handler) employeeExecutionOrigin(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding) (string, error) {
	c, ok := service.ParseDirectTaskContext(b.Queue)
	states := map[string]string{"succeeded": "completed", "failed": "failed", "cancelled": "cancelled"}
	if !ok || c.EmployeeTaskID != b.TaskID || c.WorkspaceID != b.Scope.WorkspaceID || util.UUIDToString(b.Queue.AgentID) != b.Scope.AgentID || b.Queue.Status != states[b.State] || b.ResultRef != "agent_task_queue:"+b.QueueTaskID || b.FinishedAt.IsZero() {
		return "invalid_run_queue_binding", nil
	}
	var metadata employeeExecutionContext
	if json.Unmarshal(b.Queue.Context, &metadata) != nil || metadata.Owner != "employee" || metadata.Scene.SceneID != b.SceneID {
		return "invalid_execution_context", nil
	}
	b.JobID, b.SourceRef = metadata.JobID, metadata.SourceRef
	if _, err := util.ParseUUID(b.JobID); err != nil {
		b.JobID = ""
		return "source_job_missing", nil
	}
	var job employeeentry.Job
	err := tx.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state FROM employee_scene_job WHERE id=$1::uuid`, b.JobID).Scan(&job.Scope.WorkspaceID, &job.Scope.AgentID, &job.Scope.TenantOrgID, &job.Scope.SceneID, &job.PrincipalID, &job.Items, &job.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_job_missing", nil
	}
	if err != nil {
		return "", err
	}
	if job.Scope != b.Scope {
		return "source_job_scope_mismatch", nil
	}
	if job.State != "completed" {
		return "source_job_pending", nil
	}
	if !employeeExecutionInputMatches(b.Queue, c, metadata) {
		return "execution_input_mismatch", nil
	}
	for _, item := range job.Items {
		var env employeeDispatchEnvelope
		if json.Unmarshal(item.Payload, &env) != nil {
			continue
		}
		for _, message := range employeeSourceMessages(item, env) {
			if message.SourceRef != b.SourceRef {
				continue
			}
			if b.SourceReceiptID != "" || env.PrincipalID != item.PrincipalID || env.PrincipalID != job.PrincipalID || c.PrincipalID != env.PrincipalID || env.Command.EventReceiptID != item.ReceiptID || dispatchSceneID(env.Command) != b.SceneID || dispatchRecordedOrg(env.Command) != b.Scope.TenantOrgID || message.RequesterRef != b.Requester {
				return "source_binding_mismatch", nil
			}
			principal, err := util.ParseUUID(item.PrincipalID)
			if err != nil {
				return "source_principal_invalid", nil
			}
			receiptID, err := util.ParseUUID(item.ReceiptID)
			if err != nil {
				return "source_receipt_invalid", nil
			}
			var admitted bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_event_consumption WHERE receipt_id=$1 AND job_id=$2::uuid AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5 AND scene_id=$6::uuid AND principal_id=$7 AND owner_loop='employee' AND state='completed')`, receiptID, b.JobID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.SceneID, principal).Scan(&admitted)
			if err != nil {
				return "", err
			}
			if !admitted {
				return "source_consumption_missing", nil
			}
			var original db.SceneEventReceipt
			err = tx.QueryRow(ctx, `SELECT principal_id,scene_id,route,state,reason,config_version FROM scene_event_receipt WHERE id=$1 AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4`, receiptID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID).Scan(&original.PrincipalID, &original.SceneID, &original.Route, &original.State, &original.Reason, &original.ConfigVersion)
			if errors.Is(err, pgx.ErrNoRows) {
				return "source_receipt_missing", nil
			}
			if err != nil {
				return "", err
			}
			resolvedRoute := original.Route == eventrouter.Unified && original.State == eventrouter.Ready || original.Route == eventrouter.Legacy && original.State == eventrouter.Legacy
			if original.PrincipalID != principal || util.UUIDToString(original.SceneID) != b.SceneID || original.Reason != "" || !resolvedRoute {
				return "source_receipt_mismatch", nil
			}
			b.SourceReceiptID, b.PrincipalID, b.Original, b.Source = item.ReceiptID, principal, original, message
		}
	}
	if b.SourceReceiptID == "" {
		return "source_message_missing", nil
	}
	return employeeExecutionDispatchProof(ctx, tx, b)
}

func employeeExecutionInputMatches(queue db.AgentTaskQueue, direct service.DirectTaskContext, metadata employeeExecutionContext) bool {
	var saved struct {
		Input json.RawMessage `json:"employee_direct_input"`
	}
	if json.Unmarshal(queue.Context, &saved) != nil {
		return false
	}
	var original employeeExecutionContext
	queue.Context = saved.Input
	frozenDirect, valid := service.ParseDirectTaskContext(queue)
	return valid && frozenDirect == direct && json.Unmarshal(saved.Input, &original) == nil && original == metadata
}

// A self-consistent mutable queue context is not provenance. Match the original
// run_started ledger key and the Host's committed dispatch tool checkpoint. A
// continuation additionally proves the exact accepted resume input boundary.
func employeeExecutionDispatchProof(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding) (string, error) {
	var key, queue string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT source_key,payload->>'queue_task_id',goal_revision FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND run_id=$5::uuid AND kind='run_started' AND source_namespace='employee_scene'`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID, b.RunID).Scan(&key, &queue, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_dispatch_missing", nil
	}
	if err != nil {
		return "", err
	}
	prefix := b.SourceReceiptID + "/"
	if queue != b.QueueTaskID || revision != b.GoalRevision || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "/run") {
		return "source_dispatch_missing", nil
	}
	callID := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "/run")
	if callID == "" {
		return "source_dispatch_missing", nil
	}
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT tool_journal->$2 FROM employee_scene_job WHERE id=$1::uuid`, b.JobID, callID).Scan(&raw); err != nil {
		return "", err
	}
	var saved struct {
		Input  employeeloop.ToolCall `json:"input"`
		Result employeeToolRecord    `json:"result"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.Input.NativeToolCallID != callID || saved.Input.Arguments["source_ref"] != b.SourceRef || saved.Result.Failure != "" || saved.Result.Result.Receipt != b.RunID {
		return "source_dispatch_missing", nil
	}
	var ids struct {
		TaskID      string `json:"task_id"`
		RunID       string `json:"run_id"`
		QueueTaskID string `json:"queue_task_id"`
	}
	if json.Unmarshal([]byte(saved.Result.Result.Content), &ids) != nil || ids.TaskID != b.TaskID || ids.RunID != b.RunID || ids.QueueTaskID != b.QueueTaskID {
		return "source_dispatch_missing", nil
	}
	if saved.Input.Name == "dispatch_task" {
		return "", nil
	}
	if saved.Input.Name != "continue_task" || saved.Result.Result.Terminal == nil || saved.Result.Result.Terminal.Kind != employeeloop.Dispatched {
		return "source_dispatch_missing", nil
	}
	// The queue may acquire runtime fields after dispatch. Its frozen Host input
	// must still identify the same current source, principal and execution.
	direct, valid := service.ParseDirectTaskContext(b.Queue)
	metadata := employeeExecutionContext{JobID: b.JobID, SourceRef: b.SourceRef, Owner: "employee", Scene: scene.Ref{SceneID: b.SceneID}}
	if !valid || !employeeExecutionInputMatches(b.Queue, direct, metadata) {
		return "execution_input_mismatch", nil
	}
	var body, actor string
	var resumedRevision int64
	err = tx.QueryRow(ctx, `SELECT e.body,e.actor_ref,e.goal_revision FROM employee_task_entry e
 JOIN employee_task_run r ON r.task_id=e.task_id AND r.workspace_id=e.workspace_id AND r.agent_id=e.agent_id AND r.tenant_org_id=e.tenant_org_id AND r.input_seq=e.seq
 WHERE e.workspace_id=$1::uuid AND e.agent_id=$2::uuid AND e.tenant_org_id=$3 AND e.task_id=$4::uuid AND r.id=$5::uuid
 AND e.kind='resumed' AND e.source_namespace='employee_scene' AND e.source_key=$6 AND e.run_id IS NULL`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID, b.RunID, prefix+callID+"/resume").Scan(&body, &actor, &resumedRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "source_continuation_missing", nil
	}
	if err != nil {
		return "", err
	}
	var accepted employeeSourceMessage
	if resumedRevision != b.GoalRevision || actor != b.Requester || actor != b.Source.RequesterRef || json.Unmarshal([]byte(body), &accepted) != nil || !reflect.DeepEqual(accepted, b.Source) {
		return "source_continuation_mismatch", nil
	}
	b.Continuation = true
	return "", nil
}

func (h *Handler) logEmployeeExecutionEvent(ctx context.Context, b employeeExecutionBinding, receiptID, state, reason string, at time.Time) {
	metadata := map[string]any{"workspace_id": b.Scope.WorkspaceID, "agent_id": b.Scope.AgentID, "tenant_org_id": b.Scope.TenantOrgID, "scene_id": b.SceneID, "task_id": b.TaskID, "run_id": b.RunID, "queue_task_id": b.QueueTaskID, "job_id": b.JobID, "source_receipt_id": b.SourceReceiptID, "receipt_id": receiptID, "state": state, "reason": reason, "goal_revision": b.GoalRevision, "run_state": b.State, "result_ref": b.ResultRef}
	args := []any{"event", "employee_execution_event_recorded"}
	for _, key := range []string{"workspace_id", "agent_id", "tenant_org_id", "scene_id", "task_id", "run_id", "queue_task_id", "job_id", "source_receipt_id", "receipt_id", "state", "reason", "goal_revision", "run_state", "result_ref"} {
		args = append(args, key, metadata[key])
	}
	slog.InfoContext(ctx, "employee execution fact recorded", args...)
	if h.EmployeeSceneWorker == nil || h.EmployeeSceneWorker.Langfuse == nil || receiptID == "" || strings.TrimSpace(b.JobID) == "" || b.SourceReceiptID == "" || !b.PrincipalID.Valid {
		return
	}
	client := h.EmployeeSceneWorker.Langfuse
	trace := langfuse.TraceOptions{TraceID: b.JobID, Name: "employee_loop", UserID: util.UUIDToString(b.PrincipalID), SessionID: b.SceneID, StartTime: at, Tags: []string{"employee_loop", langfuse.Tag("workspace", b.Scope.WorkspaceID), langfuse.Tag("agent", b.Scope.AgentID)}}
	spanID := langfuse.DeterministicSpanID("employee.execution:" + b.RunID)
	client.StartObservationInTrace(ctx, trace, langfuse.ObservationOptions{Type: langfuse.TypeEvent, Name: "employee_execution_event", SpanID: spanID, StartTime: at, Metadata: metadata}).End(langfuse.EndOptions{EndTime: at})
	client.IndexInTrace(ctx, trace, spanID, map[string]string{"employee_task_id": b.TaskID, "employee_run_id": b.RunID, "queue_task_id": b.QueueTaskID, "employee_job_id": b.JobID, "source_receipt_id": b.SourceReceiptID, "receipt_id": receiptID})
}
