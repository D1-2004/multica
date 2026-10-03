package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeplan"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// An explicit-goal Task may carry a pre-authorized work plan: the dispatch's
// own prompt is step 1 and the requester's later steps follow it. When a
// planned Run's terminal fact is recorded, the Host consumes it exactly once
// per plan revision: it dispatches the next step without a model call, wakes
// the employee (execution.follow_up, at most three model requests) when that
// step asked for a review first, completes the goal after the last step, or
// pauses for a human with a deterministic note. A new goal revision
// supersedes the plan; a stopped Task never continues.

const (
	employeePlanNamespace = "employee_plan"
	// employeeAutonomousRoundLimit is the governor: non-human rounds of one
	// Task since a human last took part (GawkBot governor). The next round
	// waits for a human instead.
	employeeAutonomousRoundLimit = 12
)

// employeePlanSteps parses dispatch_task.follow_up_steps. The dispatch prompt
// is step 1; nil means an ordinary single-run Task.
func employeePlanSteps(args map[string]any, prompt string) ([]employeeplan.Step, error) {
	raw, present := args["follow_up_steps"]
	if !present || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("follow_up_steps must be an array of steps")
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > employeeplan.MaxSteps-1 {
		return nil, fmt.Errorf("follow_up_steps allows at most %d later steps", employeeplan.MaxSteps-1)
	}
	steps := []employeeplan.Step{{Prompt: prompt}}
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("each follow-up step must be an object")
		}
		for key := range fields {
			if key != "prompt" && key != "review_first" {
				return nil, fmt.Errorf("unknown follow-up step field %q", key)
			}
		}
		text, _ := fields["prompt"].(string)
		step := employeeplan.Step{Prompt: strings.TrimSpace(text)}
		if value, present := fields["review_first"]; present {
			review, ok := value.(bool)
			if !ok {
				return nil, errors.New("review_first must be a boolean")
			}
			step.Review = review
		}
		steps = append(steps, step)
	}
	if !employeeplan.ValidSteps(steps) {
		return nil, fmt.Errorf("each step needs a prompt of at most %d bytes", employeeplan.MaxPromptBytes)
	}
	return steps, nil
}

func employeePlanStepPrompt(step, total int, prompt string) string {
	return fmt.Sprintf("PLAN STEP %d OF %d: do only this step now. The Host starts each later step as a separate execution after this one finishes; do not perform later steps here.\n%s", step, total, prompt)
}

func employeeFollowUpStepsSchema() map[string]any {
	return map[string]any{"type": "array", "maxItems": employeeplan.MaxSteps - 1,
		"description": "Optional. Use only when the selected source explicitly asks for an ordered job whose later parts must run as separate executions after the earlier part finishes (for example: run A, and once A is done run B with its result). The prompt above is step 1; list each later step in order. One execution can already do several actions, so never split a single job into steps. Set review_first only when the requester asked to check or decide before that step runs. The Host starts each later step after the previous one succeeds, stops on a failure and reports each step's result.",
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"prompt":       map[string]any{"type": "string", "description": "Complete instruction for this step, preserving the requester's constraints."},
			"review_first": map[string]any{"type": "boolean", "description": "True only when the requester asked to review the previous result before this step."},
		}, "required": []string{"prompt"}, "additionalProperties": false}}
}

func employeePlanScope(scope employeeentry.Scope) employeeplan.Scope {
	return employeeplan.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.SceneID}
}

func employeeEntryScopeOf(scope employeeplan.Scope) employeeentry.Scope {
	return employeeentry.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.SceneID}
}

func employeeTaskScopeOf(scope employeeplan.Scope) employeetask.Scope {
	return employeetask.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: scope.SceneID}}
}

// employeePlanDecision is one evaluation of a planned terminal Run. Defer
// means the facts are not complete yet; the next reconciliation retries.
type employeePlanDecision struct {
	plan     employeeplan.Plan
	task     employeetask.Task
	run      employeetask.Run
	step     int
	next     int
	decision employeeplan.Decision
	reason   string
	wait     bool
	note     string
	deferred bool
}

func employeeRunInScope(ctx context.Context, database employeeentry.DB, scope employeeplan.Scope, taskID, runID string) (employeetask.Run, error) {
	var r employeetask.Run
	err := database.QueryRow(ctx, `SELECT id::text,task_id::text,queue_task_id::text,goal_revision,input_seq,state,result,result_ref,created_at,finished_at FROM employee_task_run WHERE id=$1::uuid AND task_id=$2::uuid AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5`, runID, taskID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID).Scan(&r.ID, &r.TaskID, &r.QueueTaskID, &r.GoalRevision, &r.InputSeq, &r.State, &r.Result, &r.ResultRef, &r.CreatedAt, &r.FinishedAt)
	return r, err
}

// evaluateEmployeePlan applies the plan's authorization to one terminal Run:
// the exact Run, its verified terminal fact, goal and plan revision, the step
// it executed, the condition the plan allows, the budget and the governor.
func evaluateEmployeePlan(ctx context.Context, database employeeentry.DB, plan employeeplan.Plan, runID string) (employeePlanDecision, error) {
	d := employeePlanDecision{plan: plan}
	task, err := employeetask.NewStore(database).Get(ctx, employeeTaskScopeOf(plan.Scope), plan.TaskID)
	if errors.Is(err, employeetask.ErrNotFound) {
		d.decision, d.reason = employeeplan.DecisionStale, "task_missing"
		return d, nil
	}
	if err != nil {
		return d, err
	}
	d.task = task
	if d.run, err = employeeRunInScope(ctx, database, plan.Scope, plan.TaskID, runID); err != nil {
		return d, err
	}
	step, inPlan, err := employeeplan.StepOfRun(ctx, database, plan, runID)
	if err != nil {
		return d, err
	}
	if !inPlan {
		d.deferred = true
		return d, nil
	}
	d.step = step
	// The Host-verified terminal fact is the only trigger: its receipt and
	// consumption record whether it belonged to the current goal revision.
	var factState, factReason string
	err = database.QueryRow(ctx, `SELECT c.state,c.reason FROM scene_event_receipt e JOIN employee_event_consumption c ON c.receipt_id=e.id AND c.workspace_id=e.workspace_id AND c.agent_id=e.agent_id
 WHERE e.workspace_id=$1::uuid AND e.agent_id=$2::uuid AND e.source=$3 AND e.source_event_id=$4`, plan.Scope.WorkspaceID, plan.Scope.AgentID, employeeExecutionSource, runID).Scan(&factState, &factReason)
	if errors.Is(err, pgx.ErrNoRows) {
		d.deferred = true
		return d, nil
	}
	if err != nil {
		return d, err
	}
	switch {
	case task.State == employeetask.StateCancelled:
		d.decision, d.reason = employeeplan.DecisionStopped, "task_stopped"
	case task.State == employeetask.StateSucceeded:
		d.decision, d.reason = employeeplan.DecisionStale, "goal_completed"
	case task.GoalRevision != plan.GoalRevision || d.run.GoalRevision != plan.GoalRevision || factReason == "historical_goal_revision":
		d.decision, d.reason = employeeplan.DecisionStale, "goal_revised"
	case factState != "completed":
		d.decision, d.reason = employeeplan.DecisionPaused, "terminal_fact_held"
	case d.run.State == employeetask.StateCancelled:
		// A stop or steer cancelled this step; those flows speak for themselves.
		d.decision, d.reason = employeeplan.DecisionPaused, "run_cancelled"
	case d.run.State == employeetask.StateFailed:
		// Let the failure notice go first so the pause note follows it.
		var noticed bool
		if err = database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_run_notice WHERE run_id=$1::uuid)`, runID).Scan(&noticed); err != nil {
			return d, err
		}
		if !noticed {
			d.deferred = true
			return d, nil
		}
		d.decision, d.reason, d.wait = employeeplan.DecisionPaused, "step_failed", true
		d.note = fmt.Sprintf("计划的第 %d 步没有成功，后面的步骤先暂停了。需要继续的话告诉我。", step)
	case d.run.State != employeetask.StateSucceeded:
		d.deferred = true
	case step >= len(plan.Steps):
		d.decision, d.reason = employeeplan.DecisionCompleted, "last_step_succeeded"
	case task.State != employeetask.StateReady:
		d.decision, d.reason = employeeplan.DecisionPaused, "goal_not_ready"
	default:
		actions, err := employeeplan.Actions(ctx, database, plan)
		if err != nil {
			return d, err
		}
		switch {
		case actions >= plan.StepBudget:
			d.decision, d.reason, d.wait = employeeplan.DecisionPaused, "step_budget_exhausted", true
			d.note = fmt.Sprintf("这个计划已经自动推进了 %d 步，达到本轮上限，后面的步骤先暂停了。需要继续的话告诉我。", actions)
		case task.AutonomousRounds >= employeeAutonomousRoundLimit:
			d.decision, d.reason, d.wait = employeeplan.DecisionPaused, "autonomous_round_limit", true
			d.note = fmt.Sprintf("这个任务已经连续自动推进了 %d 轮，先停下来了。需要继续的话告诉我。", task.AutonomousRounds)
		case plan.Steps[step].Review:
			d.decision, d.next = employeeplan.DecisionWoken, step+1
		default:
			d.decision, d.next = employeeplan.DecisionDispatched, step+1
		}
	}
	return d, nil
}

// ReconcileEmployeeTaskFollowUps consumes verified terminal facts of planned
// Runs. Plan steps and decision wakes are new work for every replica, so the
// reconciler runs only while every live replica supports them.
func (h *Handler) ReconcileEmployeeTaskFollowUps(ctx context.Context, limit int) (int, error) {
	if h == nil || h.DB == nil || h.TxStarter == nil || h.TaskService == nil || h.EmployeeSceneWorker == nil || limit < 1 || limit > 1000 {
		return 0, errors.New("invalid employee follow-up reconciliation")
	}
	if ready, _ := h.EmployeeSceneWorker.TaskWakeProducerReady(ctx); !ready {
		// Mixed replicas: plan steps and decision wakes wait for the marker.
		return 0, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT p.workspace_id::text,p.id::text,r.id::text FROM employee_task_plan p
 JOIN employee_task_run r ON r.task_id=p.task_id AND r.workspace_id=p.workspace_id AND r.agent_id=p.agent_id AND r.tenant_org_id=p.tenant_org_id
 WHERE p.state='active' AND r.state IN ('succeeded','failed','cancelled')
 AND (r.id=p.first_run_id OR EXISTS(SELECT 1 FROM employee_task_follow_up n WHERE n.plan_id=p.id AND n.next_run_id=r.id))
 AND NOT EXISTS(SELECT 1 FROM employee_task_follow_up f WHERE f.task_id=p.task_id AND f.plan_revision=p.plan_revision AND f.run_id=r.id)
 AND EXISTS(SELECT 1 FROM scene_event_receipt e WHERE e.workspace_id=p.workspace_id AND e.agent_id=p.agent_id AND e.source=$1 AND e.source_event_id=r.id::text)
 ORDER BY r.finished_at,r.id LIMIT $2`, employeeExecutionSource, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ workspace, plan, run string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.workspace, &c.plan, &c.run); err != nil {
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
		done, err := h.followUpEmployeePlanRun(ctx, c.workspace, c.plan, c.run)
		if err != nil {
			if ctx.Err() != nil {
				return count, ctx.Err()
			}
			failures = append(failures, fmt.Errorf("employee plan %s run %s: %w", c.plan, c.run, err))
			continue
		}
		if done {
			count++
		}
	}
	return count, errors.Join(failures...)
}

// followUpEmployeePlanRun evaluates without locks to prepare a deterministic
// step outside the transaction, then re-evaluates under the scene, Task and
// plan locks and records exactly one decision with its effects.
func (h *Handler) followUpEmployeePlanRun(ctx context.Context, workspaceID, planID, runID string) (bool, error) {
	database, ok := employeeEntryDB(h)
	if !ok {
		return false, errors.New("employee plan storage is unavailable")
	}
	var scope employeeplan.Scope
	scope.WorkspaceID = workspaceID
	if err := database.QueryRow(ctx, `SELECT agent_id::text,tenant_org_id,scene_id::text FROM employee_task_plan WHERE id=$1::uuid AND workspace_id=$2::uuid`, planID, workspaceID).Scan(&scope.AgentID, &scope.TenantOrgID, &scope.SceneID); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	plan, err := employeeplan.ByID(ctx, database, scope, planID, false)
	if err != nil {
		return false, err
	}
	first, err := evaluateEmployeePlan(ctx, database, plan, runID)
	if err != nil || first.deferred {
		return false, err
	}
	var prepared *service.PreparedDirectTask
	var stepHold string
	if first.decision == employeeplan.DecisionDispatched {
		p, err := h.prepareEmployeePlanStep(ctx, database, first.plan, first.task, first.run, first.next)
		var hold *employeeentry.TaskOriginHold
		switch {
		case errors.As(err, &hold):
			stepHold = hold.Reason
		case err != nil:
			return false, err
		default:
			prepared = &p
		}
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Lock order: workspace, scene, Task, plan (message admission and the
	// scene job lease take the scene before any Task).
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&locked); err != nil {
		return false, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM agent_scene WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid FOR UPDATE`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID).Scan(&locked); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid FOR UPDATE`, plan.TaskID, workspaceID).Scan(&locked); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if plan, err = employeeplan.ByID(ctx, tx, scope, planID, true); err != nil {
		return false, err
	}
	if plan.State != employeeplan.StateActive {
		return false, nil
	}
	if _, err = employeeplan.FollowUpOf(ctx, tx, plan, runID); err == nil {
		return false, nil
	} else if !errors.Is(err, employeeplan.ErrNotFound) {
		return false, err
	}
	d, err := evaluateEmployeePlan(ctx, tx, plan, runID)
	if err != nil || d.deferred {
		return false, err
	}
	if d.decision != first.decision || d.next != first.next {
		// Facts moved between the two reads; the next pass starts over.
		return false, nil
	}
	if d.decision == employeeplan.DecisionDispatched && stepHold != "" {
		d.decision, d.reason, d.next, d.wait = employeeplan.DecisionPaused, stepHold, 0, false
	}
	if d.decision == employeeplan.DecisionDispatched && prepared != nil && prepared.TaskVersion() != d.task.Version {
		return false, nil
	}
	return h.applyEmployeePlanDecision(ctx, tx, d, prepared)
}

func (h *Handler) applyEmployeePlanDecision(ctx context.Context, tx pgx.Tx, d employeePlanDecision, prepared *service.PreparedDirectTask) (bool, error) {
	plan := d.plan
	tscope := employeeTaskScopeOf(plan.Scope)
	record := employeeplan.FollowUp{RunID: d.run.ID, StepIndex: d.step, Decision: d.decision, Reason: d.reason}
	var dispatched *service.DirectTaskResult
	var wakeQueued bool
	switch d.decision {
	case employeeplan.DecisionDispatched:
		if prepared == nil {
			return false, errors.New("plan step was not prepared")
		}
		out, err := h.TaskService.StartPreparedDirectTaskTx(ctx, tx, *prepared)
		if errors.Is(err, employeetask.ErrConflict) || errors.Is(err, employeetask.ErrActiveRun) || errors.Is(err, employeetask.ErrRunNotReady) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if _, _, err = employeetask.NewStore(tx).NoteAutonomousRound(ctx, tscope, plan.TaskID, employeetask.AutonomousRoundParams{Source: employeetask.Source{Namespace: employeePlanNamespace, Key: plan.ID + "/" + d.run.ID + "/round"}, WakeKind: "plan_step", AuthorityRef: plan.AuthorityRef}); err != nil {
			return false, err
		}
		record.NextStepIndex, record.NextRunID, record.NextQueueID = d.next, out.Run.ID, util.UUIDToString(out.Task.ID)
		dispatched = &out
	case employeeplan.DecisionWoken:
		c, err := employeeentry.NewStore(tx).AdmitTaskWake(ctx, h.EmployeeSceneWorker, employeeentry.TaskWakeAdmission{
			Scope: employeeEntryScopeOf(plan.Scope), Source: "execution", EventID: plan.ID + "/" + d.run.ID, OccurredAt: time.Now().UTC(),
			Wake: employeeentry.TaskWake{SchemaVersion: employeeentry.TaskWakeSchemaVersion, Kind: employeeentry.TaskWakeExecutionFollowUp, TaskID: plan.TaskID, GoalRevision: d.task.GoalRevision, InputSeq: d.task.LastEntrySeq, AuthorityRef: "plan:" + plan.ID, EvidenceRef: "run:" + d.run.ID},
		})
		if errors.Is(err, employeeentry.ErrTaskWakeNotReady) {
			return false, nil
		}
		var hold *employeeentry.TaskOriginHold
		if errors.As(err, &hold) || errors.Is(err, employeeentry.ErrTaskWakeOrigin) || errors.Is(err, employeeentry.ErrTaskWakeStale) || errors.Is(err, employeeentry.ErrTaskWakeStopped) || errors.Is(err, employeeentry.ErrNotFound) {
			record.Decision, record.Reason = employeeplan.DecisionPaused, "decision_wake_refused"
			d.decision = employeeplan.DecisionPaused
			break
		}
		if err != nil {
			return false, err
		}
		record.WakeJobID, wakeQueued = c.JobID, true
	case employeeplan.DecisionCompleted:
		completed, _, err := employeetask.CompleteGoalTx(ctx, tx, tscope, plan.TaskID, employeetask.CompleteGoalParams{
			Source: employeetask.Source{Namespace: employeePlanNamespace, Key: plan.ID + "/" + d.run.ID + "/complete"}, GoalRevision: d.task.GoalRevision, InputSeq: d.task.LastEntrySeq,
			AuthorityRef: plan.AuthorityRef, Summary: fmt.Sprintf("plan %d steps completed", len(plan.Steps)), ExpectedVersion: d.task.Version,
		})
		var lifecycle *employeetask.LifecycleError
		if errors.As(err, &lifecycle) {
			// New input after the last step, an open wait or a pending writer:
			// the goal stays open for the human; nothing is guessed.
			record.Decision, record.Reason = employeeplan.DecisionPaused, "complete_"+lifecycle.Reason
			d.decision = employeeplan.DecisionPaused
		} else if err != nil {
			return false, err
		} else if _, releaseErr := service.ReleaseEmployeeDependentsTx(ctx, tx, completed, employeetask.Source{Namespace: employeePlanNamespace, Key: plan.ID + "/" + d.run.ID + "/complete"}); releaseErr != nil {
			// The release reconciler applies the completion later.
			slog.WarnContext(ctx, "employee dependent release deferred", "task_id", plan.TaskID, "error", releaseErr)
		}
	}
	if d.wait && d.task.Lifecycle() == employeetask.LifecycleV2 {
		if _, _, err := employeetask.WaitTaskTx(ctx, tx, tscope, plan.TaskID, employeetask.WaitParams{Source: employeetask.Source{Namespace: employeePlanNamespace, Key: plan.ID + "/" + d.run.ID + "/wait"}, Kind: employeetask.WaitHumanInput, RefID: "plan:" + plan.ID + ":" + d.run.ID, Mandatory: true, AuthorityRef: plan.AuthorityRef, Body: d.reason}); err != nil {
			return false, err
		}
	}
	saved, err := employeeplan.Record(ctx, tx, plan, record)
	if errors.Is(err, employeeplan.ErrConflict) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch record.Decision {
	case employeeplan.DecisionCompleted:
		err = employeeplan.SetState(ctx, tx, plan, employeeplan.StateCompleted, record.Reason)
	case employeeplan.DecisionPaused:
		err = employeeplan.SetState(ctx, tx, plan, employeeplan.StatePaused, record.Reason)
	case employeeplan.DecisionStopped:
		err = employeeplan.SetState(ctx, tx, plan, employeeplan.StateStopped, record.Reason)
	case employeeplan.DecisionStale:
		err = employeeplan.SetState(ctx, tx, plan, employeeplan.StateSuperseded, record.Reason)
	}
	if err != nil {
		return false, err
	}
	noteSent := false
	if d.note != "" && record.Decision == employeeplan.DecisionPaused {
		if noteSent, err = h.enqueueEmployeePlanNote(ctx, tx, plan, d.task, saved.ID, d.note); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if dispatched != nil {
		h.TaskService.NotifyDirectTaskResult(ctx, *dispatched)
	}
	if wakeQueued {
		h.EmployeeSceneWorker.Notify()
	}
	if noteSent && h.DingTalkResponses != nil {
		h.DingTalkResponses.Notify()
	}
	slog.InfoContext(ctx, "employee plan follow-up recorded", "event", "employee_plan_follow_up", "workspace_id", plan.Scope.WorkspaceID, "agent_id", plan.Scope.AgentID, "scene_id", plan.Scope.SceneID, "task_id", plan.TaskID, "plan_id", plan.ID, "plan_revision", plan.Revision, "run_id", d.run.ID, "step", d.step, "decision", record.Decision, "reason", record.Reason, "next_run_id", record.NextRunID, "wake_job_id", record.WakeJobID, "note", noteSent)
	return true, nil
}

// enqueueEmployeePlanNote sends a deterministic note to the Task's origin
// conversation in the decision's transaction. A Task whose origin can no longer
// be addressed gets no note; the paused state is still recorded.
func (h *Handler) enqueueEmployeePlanNote(ctx context.Context, tx pgx.Tx, plan employeeplan.Plan, task employeetask.Task, followUpID, text string) (bool, error) {
	if h.DingTalkResponses == nil || h.EmployeeSceneWorker == nil {
		return false, nil
	}
	scope := employeeEntryScopeOf(plan.Scope)
	origin, err := h.EmployeeSceneWorker.TaskOrigin(ctx, tx, scope, task.ID)
	var hold *employeeentry.TaskOriginHold
	if errors.As(err, &hold) || errors.Is(err, employeeentry.ErrTaskWakeOrigin) || errors.Is(err, employeeentry.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !origin.Anchor.Conversation {
		return false, nil
	}
	job := employeeentry.Job{Scope: scope}
	registered, err := employeeSceneFence(ctx, &Handler{Queries: db.New(tx)}, job)
	if errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	in, err := employeeTaskWakeDelivery(ctx, db.New(tx), job, origin, registered, text)
	var refused *employeeTaskWakeHold
	if errors.As(err, &refused) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	actionID, err := h.DingTalkResponses.EnqueueSceneNotice(ctx, tx, in, uuid.NewSHA1(uuid.MustParse(followUpID), []byte("plan-note")).String())
	if err != nil {
		return false, err
	}
	if err = employeeentry.RecordHostNotice(ctx, tx, employeeentry.HostNotice{ActionID: actionID, Scope: scope, PrincipalID: origin.HistoryPrincipalID, SourceKind: employeeentry.HostNoticeTaskPlan, SourceID: followUpID, OriginReceiptID: origin.ReceiptID}); err != nil {
		return false, err
	}
	return true, employeeplan.SetNote(ctx, tx, followUpID, actionID)
}

// prepareEmployeePlanStep compiles step next for the Task's original request
// and resolves its execution context before any Host transaction. The Run is
// bound to the original source message, job and principal; its own source is
// the plan and the consumed terminal Run.
func (h *Handler) prepareEmployeePlanStep(ctx context.Context, database employeeentry.DB, plan employeeplan.Plan, task employeetask.Task, consumed employeetask.Run, next int) (service.PreparedDirectTask, error) {
	scope := employeeEntryScopeOf(plan.Scope)
	tscope := employeeTaskScopeOf(plan.Scope)
	store := employeetask.NewStore(database)
	entries, err := store.ReadEntries(ctx, tscope, task.ID, 0, 1)
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	if len(entries) != 1 || entries[0].Kind != "request" {
		return service.PreparedDirectTask{}, &employeeentry.TaskOriginHold{Reason: "task_request_missing"}
	}
	parts, err := employeeSceneOriginParts(ctx, database, scope, task, entries[0])
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	snapshot, err := store.ReadCurrent(ctx, tscope, task.RequesterRef, task.ID)
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	principal := parts.admission.PrincipalID
	material := func(ref, body string) employeetask.PacketMaterial {
		return employeetask.PacketMaterial{Ref: ref, Scope: tscope, PrincipalID: principal, Body: body}
	}
	history := employeetask.PacketHistory{State: employeetask.HistoryAvailable}
	for _, entry := range snapshot.Entries {
		history.Items = append(history.Items, material(fmt.Sprintf("entry:%s:%d", task.ID, entry.Seq), employeeTaskData(entry.Kind+": "+entry.Body, 2000)))
	}
	if snapshot.LatestRun != nil {
		run := snapshot.LatestRun
		history.Items = append(history.Items, material("run-report:"+run.ID, "Previous plan step's executor report, not verified external delivery:\n"+employeeTaskData(run.Result, 8000)))
	}
	if snapshot.Truncated {
		history.State = employeetask.HistoryTruncated
	}
	if len(history.Items) == 0 {
		history.State = employeetask.HistoryEmpty
	}
	var corrections []employeetask.PacketMaterial
	for _, entry := range snapshot.Corrections {
		corrections = append(corrections, material(fmt.Sprintf("employee_task_entry:%s/%d", task.ID, entry.Seq), entry.Body))
	}
	evidence, err := json.Marshal(parts.source)
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	packet, err := employeetask.Compile(employeetask.CompileInput{Scope: tscope, PrincipalID: principal, Definition: task.Definition, Corrections: corrections,
		CompletionNotice: employeetask.CompletionNoticePolicy{Mode: employeetask.CompletionNoticeAlways}, Source: material(parts.source.SourceRef, string(evidence)),
		Prompt: employeePlanStepPrompt(next, len(plan.Steps), plan.Steps[next-1].Prompt), History: history, ReturnAddress: "scene:" + plan.Scope.SceneID + "; source_ref:" + parts.source.SourceRef})
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	key := plan.ID + "/" + consumed.ID
	command := parts.envelope.Command
	source := parts.source
	command.Event.Data.Messages = []DispatchMessage{source.Message}
	command.Event.Data.Sender = DispatchSender{UID: source.Message.SenderUID, StaffID: source.Message.SenderStaffID, OpenDingTalkID: source.Message.SenderOpenDingTalkID, DisplayName: source.Message.SenderDisplayName}
	command.CompletionCallback = nil
	command.ExtraCompletionCallbacks = nil
	var taskContext map[string]any
	if err = json.Unmarshal(dispatchRuntimeContext(command, key), &taskContext); err != nil {
		return service.PreparedDirectTask{}, err
	}
	delete(taskContext, "completion_callback")
	delete(taskContext, "execution_update_callback")
	taskContext["employee_delivery_owner"] = "employee"
	taskContext["employee_job_id"] = parts.admission.JobID
	taskContext["employee_source_ref"] = source.SourceRef
	taskContext["employee_context_used"] = packet.ContextUsed
	taskContext["employee_plan_step"] = map[string]any{"plan_id": plan.ID, "plan_revision": plan.Revision, "step": next, "steps": len(plan.Steps)}
	contextJSON, err := json.Marshal(taskContext)
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	principalID, err := util.ParseUUID(principal)
	if err != nil {
		return service.PreparedDirectTask{}, err
	}
	return h.TaskService.PrepareDirectTask(ctx, service.DirectTaskRequest{Task: task, Source: employeetask.Source{Namespace: employeePlanNamespace, Key: key + "/run"}, Prompt: packet.Text, PrincipalID: principalID, Context: contextJSON})
}

// employeeExecutionPlanProof verifies a Host-dispatched plan step: its
// run_started entry, the follow-up decision that queued it for this exact Run
// and queue row, its plan at the Run's goal revision, the frozen execution
// input and the Task's original request as source. handled is false for a
// Run that a plan did not start.
func employeeExecutionPlanProof(ctx context.Context, tx pgx.Tx, b *employeeExecutionBinding) (string, bool, error) {
	var key, queue string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT source_key,payload->>'queue_task_id',goal_revision FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND run_id=$5::uuid AND kind='run_started' AND source_namespace=$6`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID, b.RunID, employeePlanNamespace).Scan(&key, &queue, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	var planID, consumed string
	err = tx.QueryRow(ctx, `SELECT f.plan_id::text,f.run_id::text FROM employee_task_follow_up f JOIN employee_task_plan p ON p.id=f.plan_id AND p.task_id=f.task_id AND p.workspace_id=f.workspace_id AND p.agent_id=f.agent_id
 WHERE f.task_id=$1::uuid AND f.workspace_id=$2::uuid AND f.agent_id=$3::uuid AND f.tenant_org_id=$4 AND f.scene_id=$5::uuid AND f.next_run_id=$6::uuid AND f.next_queue_task_id=$7::uuid
 AND f.decision IN ('dispatched','woken') AND p.goal_revision=$8`, b.TaskID, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.SceneID, b.RunID, b.QueueTaskID, b.GoalRevision).Scan(&planID, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "plan_step_missing", true, nil
	}
	if err != nil {
		return "", true, err
	}
	if key != planID+"/"+consumed+"/run" || queue != b.QueueTaskID || revision != b.GoalRevision {
		return "plan_step_missing", true, nil
	}
	direct, valid := service.ParseDirectTaskContext(b.Queue)
	metadata := employeeExecutionContext{JobID: b.JobID, SourceRef: b.SourceRef, Owner: "employee", Scene: scene.Ref{SceneID: b.SceneID}}
	if !valid || !employeeExecutionInputMatches(b.Queue, direct, metadata) {
		return "execution_input_mismatch", true, nil
	}
	var body string
	err = tx.QueryRow(ctx, `SELECT body FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='request' ORDER BY seq LIMIT 1`, b.Scope.WorkspaceID, b.Scope.AgentID, b.Scope.TenantOrgID, b.TaskID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "task_request_missing", true, nil
	}
	if err != nil {
		return "", true, err
	}
	var original employeeSourceMessage
	if json.Unmarshal([]byte(body), &original) != nil || original.SourceRef != b.SourceRef || original.ReceiptID != b.SourceReceiptID {
		return "task_source_mismatch", true, nil
	}
	return "", true, nil
}

// ReconcileEmployeeUpstreamReleases applies terminal facts that were recorded
// without releasing their dependents (an older replica, or a deferred inline
// release). Only a succeeded upstream changes a dependent; a failed or stopped
// one leaves it waiting for a human. Released goals become ready; they get no
// wake unless their own plan authorizes one.
func (h *Handler) ReconcileEmployeeUpstreamReleases(ctx context.Context, limit int) (int, error) {
	if h == nil || h.DB == nil || h.TxStarter == nil || h.EmployeeSceneWorker == nil || limit < 1 || limit > 1000 {
		return 0, errors.New("invalid employee upstream release reconciliation")
	}
	if ready, _ := h.EmployeeSceneWorker.TaskWakeProducerReady(ctx); !ready {
		return 0, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT DISTINCT u.workspace_id::text,u.id::text FROM employee_task_wait w
 JOIN employee_task u ON u.id::text=w.ref_id AND u.workspace_id=w.workspace_id AND u.agent_id=w.agent_id AND u.tenant_org_id=w.tenant_org_id
 WHERE w.kind='task' AND w.state='open' AND u.state='succeeded' LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ workspace, task string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.workspace, &c.task); err != nil {
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
		released, err := h.releaseEmployeeUpstream(ctx, c.workspace, c.task)
		if err != nil {
			failures = append(failures, fmt.Errorf("employee upstream %s: %w", c.task, err))
			continue
		}
		count += released
	}
	return count, errors.Join(failures...)
}

func (h *Handler) releaseEmployeeUpstream(ctx context.Context, workspaceID, taskID string) (int, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var scope employeetask.Scope
	err = tx.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scope_kind,COALESCE(scene_id::text,''),COALESCE(legacy_id::text,'') FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid`, taskID, workspaceID).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Kind, &scope.Scene.SceneID, &scope.LegacyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	task, err := employeetask.NewStore(tx).Get(ctx, scope, taskID)
	if err != nil {
		return 0, err
	}
	release, err := service.ReleaseEmployeeDependentsTx(ctx, tx, task, employeetask.Source{Namespace: "employee_task_terminal", Key: taskID + "/" + string(task.State)})
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	released := 0
	for _, dependent := range release.Dependents {
		if dependent.Outcome == employeetask.ReleaseSatisfied {
			released++
		}
	}
	return released, nil
}
