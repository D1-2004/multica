package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const DirectTaskContextType = "employee_direct"

var ErrDirectTaskAccessDenied = errors.New("direct employee task access denied")

// DirectTaskRequest is host-owned input. PrincipalID is the authenticated
// admission principal; OriginatorUserID is set only for a verified human action.
// Context contains the host's existing dispatch/identity context, never model auth.
type DirectTaskRequest struct {
	Task             employeetask.Task
	Source           employeetask.Source
	Prompt           string
	PrincipalID      pgtype.UUID
	OriginatorUserID pgtype.UUID
	Context          json.RawMessage
}
type DirectTaskResult struct {
	Task    db.AgentTaskQueue
	Run     employeetask.Run
	Created bool
}

// DirectTaskContext carries only this execution's input. EmployeeTask and Run
// remain independent records; trace, provider output and usage remain on the queue.
type DirectTaskContext struct {
	Type             string `json:"type"`
	WorkspaceID      string `json:"workspace_id"`
	EmployeeTaskID   string `json:"employee_task_id"`
	Prompt           string `json:"direct_task_prompt"`
	PrincipalID      string `json:"direct_principal_id"`
	OriginatorUserID string `json:"direct_originator_user_id,omitempty"`
	// AutomationOrigin locates the verified automation receipt of an execution
	// that also carries a real autopilot_run_id (a scene routine occurrence).
	// It is present exactly when the queue row has an autopilot_run_id.
	AutomationOrigin *AutomationOriginRef `json:"employee_automation_origin,omitempty"`
}

// IsEmployeeDirectTask identifies the host-owned execution mode even when its
// payload is damaged, so validation never falls through to issue execution.
func IsEmployeeDirectTask(task db.AgentTaskQueue) bool {
	if task.TriggerEvidenceKind.String == "employee_task" {
		return true
	}
	var marker struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(task.Context, &marker) == nil && marker.Type == DirectTaskContextType
}

func ParseDirectTaskContext(task db.AgentTaskQueue) (DirectTaskContext, bool) {
	var c DirectTaskContext
	if task.IssueID.Valid || task.ChatSessionID.Valid || json.Unmarshal(task.Context, &c) != nil || c.Type != DirectTaskContextType {
		return DirectTaskContext{}, false
	}
	// A real autopilot_run_id is accepted only together with a well-formed
	// automation locator for that same run, and a locator only with that run.
	// Readers still verify the locator against PostgreSQL before acting on it;
	// this shape check keeps any other combination from parsing as Direct.
	if task.AutopilotRunID.Valid != (c.AutomationOrigin != nil) || (c.AutomationOrigin != nil && !validAutomationOriginRef(c.AutomationOrigin, task)) {
		return DirectTaskContext{}, false
	}
	if _, err := util.ParseUUID(c.WorkspaceID); err != nil {
		return DirectTaskContext{}, false
	}
	if _, err := util.ParseUUID(c.EmployeeTaskID); err != nil {
		return DirectTaskContext{}, false
	}
	return c, strings.TrimSpace(c.Prompt) != ""
}
func directTaskContext(p DirectTaskRequest) ([]byte, error) {
	c := map[string]json.RawMessage{}
	if len(p.Context) > 0 {
		if err := json.Unmarshal(p.Context, &c); err != nil || c == nil {
			return nil, employeetask.ErrInvalid
		}
	}
	fields := DirectTaskContext{Type: DirectTaskContextType, WorkspaceID: p.Task.Scope.WorkspaceID, EmployeeTaskID: p.Task.ID, Prompt: p.Prompt, PrincipalID: util.UUIDToString(p.PrincipalID), OriginatorUserID: util.UUIDToString(p.OriginatorUserID)}
	raw, _ := json.Marshal(fields)
	var owned map[string]json.RawMessage
	_ = json.Unmarshal(raw, &owned)
	// Always stamp the originator key, including its absence, over caller context.
	delete(c, "direct_originator_user_id")
	for k, v := range owned {
		c[k] = v
	}
	delete(c, protocol.AgentSceneContextKey)
	if p.Task.Scope.Kind == employeetask.ScopeScene {
		raw, _ = json.Marshal(p.Task.Scope.Scene)
		c[protocol.AgentSceneContextKey] = raw
	}
	// Retain only the original Host input for replay checks. The queue's top
	// level may later receive runtime context, refreshed credentials or policy.
	input := make(map[string]json.RawMessage, len(c))
	for key, value := range c {
		input[key] = value
	}
	delete(input, "employee_direct_input")
	delete(input, protocol.AgentIdentityContextTokenJSONKey)
	delete(input, protocol.AgentIdentityContextTokenExpiresAtJSONKey)
	delete(input, protocol.AgentIdentityContextTokenSourceJSONKey)
	if callback, present := input["completion_callback"]; present {
		var fields map[string]json.RawMessage
		if json.Unmarshal(callback, &fields) == nil && fields != nil {
			for _, key := range []string{"telemetry_token", "telemetry_expires_at", "telemetryToken", "telemetryExpiresAt"} {
				delete(fields, key)
			}
			input["completion_callback"], _ = json.Marshal(fields)
		}
	}
	c["employee_direct_input"], _ = json.Marshal(input)
	return json.Marshal(c)
}

// EnqueueDirectTask atomically admits one execution and maps it to one Run.
// Exact source replays recover the same queue row, even after completion.
func (s *TaskService) EnqueueDirectTask(ctx context.Context, p DirectTaskRequest) (DirectTaskResult, error) {
	var out DirectTaskResult
	if s == nil || s.TxStarter == nil {
		return out, ErrDirectTaskAccessDenied
	}
	prepared, err := s.PrepareDirectTask(ctx, p)
	if err != nil {
		return out, err
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	out, err = s.enqueuePreparedDirectTaskTx(ctx, tx, prepared)
	if err != nil {
		return DirectTaskResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DirectTaskResult{}, err
	}
	s.NotifyDirectTaskResult(ctx, out)
	return out, nil
}

func lockDirectTaskTx(ctx context.Context, tx pgx.Tx, p DirectTaskRequest) (employeetask.Task, error) {
	// Match the domain's deletion lock order: workspace -> task -> run.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, p.Task.Scope.WorkspaceID).Scan(&locked); err != nil {
		return employeetask.Task{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid FOR UPDATE`, p.Task.ID, p.Task.Scope.WorkspaceID).Scan(&locked); err != nil {
		return employeetask.Task{}, err
	}
	task, err := employeetask.NewStore(tx).Get(ctx, p.Task.Scope, p.Task.ID)
	if err != nil {
		return employeetask.Task{}, err
	}
	if task.DispatchMode != employeetask.DispatchDirect || task.OwnerLoop != p.Task.OwnerLoop {
		return employeetask.Task{}, employeetask.ErrConflict
	}
	return task, nil
}

func directTaskReplayTx(ctx context.Context, tx pgx.Tx, prepared PreparedDirectTask) (DirectTaskResult, bool, error) {
	var out DirectTaskResult
	p := prepared.request
	var queueID pgtype.UUID
	err := tx.QueryRow(ctx, `SELECT r.queue_task_id FROM employee_task_entry e JOIN employee_task_run r ON r.id=e.run_id AND r.task_id=e.task_id WHERE e.task_id=$1::uuid AND e.source_namespace=$2 AND e.source_key=$3 AND e.kind='run_started'`, p.Task.ID, p.Source.Namespace, p.Source.Key).Scan(&queueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	out.Task, err = db.New(tx).GetAgentTask(ctx, queueID)
	if err != nil {
		return out, false, err
	}
	var equal bool
	if err = tx.QueryRow(ctx, `SELECT ($1::jsonb->'employee_direct_input')=($2::jsonb->'employee_direct_input')`, out.Task.Context, prepared.contextJSON).Scan(&equal); err != nil {
		return out, false, err
	}
	if !equal {
		return out, false, employeetask.ErrConflict
	}
	out.Run, err = directRunByQueue(ctx, tx, queueID)
	return out, err == nil, err
}

// enqueuePreparedDirectTaskTx performs database work only. Its caller owns the
// transaction and must publish the result only after the outer commit succeeds.
func (s *TaskService) enqueuePreparedDirectTaskTx(ctx context.Context, tx pgx.Tx, prepared PreparedDirectTask) (DirectTaskResult, error) {
	var out DirectTaskResult
	p := prepared.request
	task, err := lockDirectTaskTx(ctx, tx, p)
	if err != nil {
		return out, err
	}
	qtx := s.Queries.WithTx(tx)
	agent, err := directTaskAdmissionAgent(ctx, qtx, p)
	if err != nil {
		return out, err
	}
	if replay, found, err := directTaskReplayTx(ctx, tx, prepared); err != nil || found {
		return replay, err
	}
	if task.ActiveRunID != "" {
		return out, employeetask.ErrActiveRun
	}
	if task.Version != p.Task.Version || task.State != employeetask.StateReady {
		return out, employeetask.ErrConflict
	}
	ready, reason, err := AgentReadiness(ctx, qtx, agent)
	if err != nil {
		return out, err
	}
	if !ready {
		return out, fmt.Errorf("direct task agent unavailable: %s", reason)
	}
	runtime, err := qtx.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return out, err
	}
	if runtime.WorkspaceID != agent.WorkspaceID {
		return out, ErrDirectTaskAccessDenied
	}
	if !DirectTaskRuntimeCapable(runtime) {
		return out, fmt.Errorf("direct task runtime lacks %s", protocol.DaemonCapabilityEmployeeDirectV1)
	}
	taskID, err := util.ParseUUID(task.ID)
	if err != nil {
		return out, err
	}
	attr := attribution.DirectHumanRun(p.OriginatorUserID, attribution.EvidenceKind("employee_task"), taskID)
	attr, err = (&TaskService{Queries: qtx}).applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		return out, err
	}
	source, _, evidence, ref := attributionCreateParams(attr)
	queueID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	_, err = tx.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context,originator_user_id,accountable_user_id,originator_source,trigger_evidence_kind,trigger_evidence_ref_id,trigger_summary,runtime_mcp_overlay,runtime_connected_apps,max_attempts) VALUES($1,$2,$3,'queued',$4,$5,$6,$7,$8,$9,$10,$11,$12,1)`, queueID, agent.ID, agent.RuntimeID, prepared.contextJSON, attr.UserID, attr.AccountableUserID, source, evidence, ref, truncateForSummary(task.Definition.Goal, triggerSummaryMaxLen), prepared.overlay.Overlay, prepared.overlay.ConnectedApps)
	if err != nil {
		return out, err
	}
	out.Task, err = qtx.GetAgentTask(ctx, queueID)
	if err != nil {
		return out, err
	}
	out.Run, err = employeetask.NewStore(tx).StartRun(ctx, task.Scope, task.ID, employeetask.StartRunParams{Source: p.Source, QueueTaskID: util.UUIDToString(queueID), ExpectedVersion: task.Version})
	if err != nil {
		return out, err
	}
	out.Created = true
	return out, nil
}
func directRunByQueue(ctx context.Context, tx pgx.Tx, id pgtype.UUID) (employeetask.Run, error) {
	var r employeetask.Run
	err := tx.QueryRow(ctx, `SELECT id::text,task_id::text,queue_task_id::text,goal_revision,input_seq,state,result,result_ref,created_at,finished_at FROM employee_task_run WHERE queue_task_id=$1`, id).Scan(&r.ID, &r.TaskID, &r.QueueTaskID, &r.GoalRevision, &r.InputSeq, &r.State, &r.Result, &r.ResultRef, &r.CreatedAt, &r.FinishedAt)
	return r, err
}

// DeferDirectTaskAfterClaimFailure releases an unsupported claim with backoff.
// A rolling older daemon can continue claiming ordinary tasks on this runtime.
func (s *TaskService) DeferDirectTaskAfterClaimFailure(ctx context.Context, task db.AgentTaskQueue, reason string) error {
	if s == nil || s.TxStarter == nil {
		return errors.New("direct claim deferral requires transaction")
	}
	var deferred db.AgentTaskQueue
	err := s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
		row, err := q.RequeueAgentTaskAfterClaimFailure(ctx, db.RequeueAgentTaskAfterClaimFailureParams{TaskID: task.ID, RuntimeID: task.RuntimeID, DispatchedAt: task.DispatchedAt})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE agent_task_queue SET status='deferred',fire_at=now()+interval '1 minute',error=$2 WHERE id=$1`, row.ID, reason)
		if err != nil {
			return err
		}
		deferred, err = q.GetAgentTask(ctx, row.ID)
		return err
	})
	if err != nil {
		return err
	}
	s.ReconcileAgentStatus(ctx, deferred.AgentID)
	// Wake remaining work, but the deferred row is ineligible until its fire_at.
	s.notifyTaskAvailable(deferred)
	return nil
}

func directTaskAdmissionAgent(ctx context.Context, qtx *db.Queries, p DirectTaskRequest) (db.Agent, error) {
	agentID, err := util.ParseUUID(p.Task.Scope.AgentID)
	if err != nil {
		return db.Agent{}, err
	}
	agent, err := qtx.GetAgent(ctx, agentID)
	if err != nil {
		return db.Agent{}, err
	}
	if util.UUIDToString(agent.WorkspaceID) != p.Task.Scope.WorkspaceID {
		return db.Agent{}, ErrDirectTaskAccessDenied
	}
	if _, err = qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: p.PrincipalID, WorkspaceID: agent.WorkspaceID}); err != nil {
		return db.Agent{}, ErrDirectTaskAccessDenied
	}
	if !(&AutopilotService{Queries: qtx}).canMemberInvokeAgent(ctx, agent, p.PrincipalID, agent.WorkspaceID) {
		return db.Agent{}, ErrDirectTaskAccessDenied
	}
	if p.OriginatorUserID.Valid {
		if _, err = qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: p.OriginatorUserID, WorkspaceID: agent.WorkspaceID}); err != nil {
			return db.Agent{}, ErrDirectTaskAccessDenied
		}
	}
	return agent, nil
}

// DirectTaskRuntimeCapable uses the current runtime's authenticated local
// advertisement or controlled cloud template capabilities, never a version guess.
func DirectTaskRuntimeCapable(runtime db.AgentRuntime) bool {
	if runtime.Status != "online" {
		return false
	}
	if runtime.RuntimeMode == "cloud" {
		return CloudSandboxRuntimeHasCapability(runtime, protocol.DaemonCapabilityEmployeeDirectV1)
	}
	if runtime.RuntimeMode != "local" {
		return false
	}
	var metadata struct {
		ClientCapabilities []string `json:"client_capabilities"`
	}
	if json.Unmarshal(runtime.Metadata, &metadata) != nil {
		return false
	}
	for _, capability := range metadata.ClientCapabilities {
		if capability == protocol.DaemonCapabilityEmployeeDirectV1 {
			return true
		}
	}
	return false
}
