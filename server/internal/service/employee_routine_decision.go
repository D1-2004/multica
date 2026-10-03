package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// An employee_decide occurrence is admitted like a run_only one (real
// AutopilotRun, frozen receipt, EmployeeTask) but instead of a Run it admits
// one routine.decision task wake. The EmployeeLoop then decides within the
// routine's authorization only: run the frozen instructions (dispatch), reply
// once in the routine's scene, wait for the next occurrence, or stay quiet.
// A decision without a Run settles the AutopilotRun as completed with the
// decision in its result and closes the Task without inventing a Run.

// Routine decision states (employee_routine_decision.state).
const (
	RoutineDecisionPending    = "pending"
	RoutineDecisionQuiet      = "quiet"
	RoutineDecisionWaited     = "waited"
	RoutineDecisionReplied    = "replied"
	RoutineDecisionDispatched = "dispatched"
	RoutineDecisionFailed     = "failed"
)

// RoutineDecisionWakeSource is the derived source of routine.decision wakes;
// the event id is the occurrence receipt id.
const RoutineDecisionWakeSource = "scene.routine"

// ErrRoutineDecisionSettled means the decision already has an outcome.
var ErrRoutineDecisionSettled = errors.New("routine decision is already settled")

// EmployeeRoutineDecisionHost admits routine.decision wakes. The handler
// implements it with the EmployeeSceneWorker as the task wake host.
type EmployeeRoutineDecisionHost interface {
	// RoutineDecisionReady is true only while every live replica executes
	// task wakes and the routine.decision reader.
	RoutineDecisionReady(ctx context.Context) error
	// AdmitRoutineDecisionWakeTx admits the wake inside tx (a savepoint) and
	// returns its job id. No model call happens here.
	AdmitRoutineDecisionWakeTx(ctx context.Context, tx pgx.Tx, admission employeeentry.TaskWakeAdmission) (string, error)
	// NotifyRoutineDecisionWake wakes the scene worker after commit.
	NotifyRoutineDecisionWake()
}

func routineRunAttribution(ctx context.Context, qtx *db.Queries, adm routineAdmission, manualActor pgtype.UUID, run db.AutopilotRun) (attribution.Result, error) {
	var attr attribution.Result
	if manualActor.Valid {
		attr = attribution.DirectHumanRun(manualActor, attribution.EvidenceAutopilotRun, run.ID)
	} else {
		attr = triggerOwnerAttribution(ctx, qtx, run.TriggerID, adm.ap.WorkspaceID, adm.ap.ID, attribution.EvidenceAutopilotRun, run.ID)
	}
	return (&TaskService{Queries: qtx}).applyAttributionFallback(ctx, attr, adm.agent)
}

// insertRoutineQueueTx writes the Direct queue row of a routine execution:
// the frozen packet, the automation locator, the routine binding and the real
// autopilot_run_id. It performs no wakeup.
func (s *AutopilotService) insertRoutineQueueTx(ctx context.Context, tx pgx.Tx, adm routineAdmission, kind AutomationOriginKind, attr attribution.Result, task employeetask.Task, run db.AutopilotRun, packet employeetask.WorkPacket, occurrenceID string) (pgtype.UUID, error) {
	contextJSON, err := directTaskContext(DirectTaskRequest{Task: task, Prompt: packet.Text, Context: routineQueueContext(adm, kind, occurrenceID, run.ID, packet.ContextUsed)})
	if err != nil {
		return pgtype.UUID{}, err
	}
	queueID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	attrSource, _, evidence, evidenceRef := attributionCreateParams(attr)
	if _, err = tx.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context,autopilot_run_id,originator_user_id,accountable_user_id,originator_source,trigger_evidence_kind,trigger_evidence_ref_id,rule_version_id,trigger_summary,max_attempts)
 VALUES($1,$2,$3,'queued',$4,$5,$6,$7,$8,$9,$10,$11,$12,1)`,
		queueID, adm.agent.ID, adm.agent.RuntimeID, contextJSON, run.ID, attr.UserID, attr.AccountableUserID, attrSource, evidence, evidenceRef, attr.RuleVersionID,
		truncateForSummary(adm.ap.Title, triggerSummaryMaxLen)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("employee routine: insert queue: %w", err)
	}
	return queueID, nil
}

// admitRoutineDecision commits one employee_decide occurrence: AutopilotRun,
// EmployeeTask, receipt, pending decision and its routine.decision wake.
func (s *AutopilotService) admitRoutineDecision(ctx context.Context, tx pgx.Tx, qtx *db.Queries, host EmployeeRoutineHost, adm routineAdmission, fire routineFire) (*db.AutopilotRun, dispatch.ReasonCode, bool, error) {
	decisions, ok := s.SceneRoutines.(EmployeeRoutineDecisionHost)
	var gate error = errors.New("routine decisions are not wired")
	if ok {
		gate = decisions.RoutineDecisionReady(ctx)
	}
	if gate != nil {
		// Never fall back to running the instructions unconditionally.
		refusal := skipRoutine(dispatch.ReasonTargetUnavailable, "employee decision is unavailable while a server lacks the routine decision reader")
		run, err := s.recordRoutineRefusalTx(ctx, tx, qtx, adm, fire, *refusal)
		if err != nil {
			return s.routineSlotConflict(ctx, fire, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return s.routineSlotConflict(ctx, fire, err)
		}
		s.touchRoutineLastRun(ctx, adm.ap)
		s.publishRoutineRefusal(adm.ap, run, *refusal)
		slog.InfoContext(ctx, "employee routine decision gate closed", "routine_id", adm.routine.ID, "reason", gate.Error())
		return &run, refusal.code, true, nil
	}
	planned := pgtype.Timestamptz{}
	if fire.scheduled() {
		planned = pgtype.Timestamptz{Time: fire.PlannedAt.UTC(), Valid: true}
	}
	run, err := qtx.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{
		AutopilotID: adm.ap.ID, TriggerID: fire.TriggerID, Source: fire.runSource(), Status: "running",
		PlannedAt: planned, RuntimeContext: routineRunContext(adm.routine, adm.ap.Title),
	})
	if err != nil {
		return s.routineSlotConflict(ctx, fire, err)
	}
	eventID := routineManualEventID(run.ID)
	if fire.scheduled() {
		eventID = routineScheduleEventID(fire.TriggerID, fire.PlannedAt)
	}
	occurrenceID := uuid.NewString()
	input := s.routineOccurrenceInput(adm, fire, eventID)
	scope := routineTaskScope(adm.routine)
	packet, err := compileRoutinePacket(scope, input)
	if err != nil {
		return nil, dispatch.ReasonInternalError, true, fmt.Errorf("employee routine: compile: %w", err)
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, dispatch.ReasonInternalError, true, err
	}
	task, err := employeetask.NewStore(tx).Create(ctx, employeetask.CreateParams{
		Scope: scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: routineRequesterRef(adm.routine.ID), Definition: packet.Definition,
		Source: employeetask.Source{Namespace: fire.source(), Key: eventID + "/definition"}, Input: string(inputJSON),
	})
	if err != nil {
		return s.routineSlotConflict(ctx, fire, fmt.Errorf("employee routine: create decision task: %w", err))
	}
	ids := &routineAcceptedIDs{occurrenceID: occurrenceID, taskID: task.ID, promptSHA: promptSHA256(packet.Text)}
	occurredAt, err := s.insertRoutineOccurrenceAtTx(ctx, tx, adm, fire, run, routineOccurrenceDecision, "", "", input, ids)
	if err != nil {
		return s.routineSlotConflict(ctx, fire, fmt.Errorf("employee routine: record decision occurrence: %w", err))
	}
	if err := s.routineFault("decision_occurrence"); err != nil {
		return nil, dispatch.ReasonInternalError, true, err
	}
	jobID, err := decisions.AdmitRoutineDecisionWakeTx(ctx, tx, employeeentry.TaskWakeAdmission{
		Scope:  employeeentry.Scope{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, TenantOrgID: scope.TenantOrgID, SceneID: scope.Scene.SceneID},
		Source: RoutineDecisionWakeSource, EventID: occurrenceID, OccurredAt: occurredAt,
		Wake: employeeentry.TaskWake{SchemaVersion: employeeentry.TaskWakeSchemaVersion, Kind: employeeentry.TaskWakeRoutineDecision, TaskID: task.ID,
			GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq, AuthorityRef: "routine:" + adm.routine.ID + "/occurrence:" + occurrenceID, EvidenceRef: "autopilot_run:" + util.UUIDToString(run.ID)},
	})
	if refusal := routineDecisionAdmissionRefusal(err); refusal != nil {
		// A durable refusal of the wake (origin hold, scene fence, gate)
		// records the occurrence instead of retrying the slot forever.
		_ = tx.Rollback(ctx)
		return s.recordRoutineRefusalNewTx(ctx, adm, fire, *refusal)
	}
	if err != nil {
		return nil, dispatch.ReasonInternalError, true, fmt.Errorf("employee routine: admit decision wake: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO employee_routine_decision(occurrence_id,workspace_id,agent_id,routine_id,autopilot_run_id,employee_task_id,job_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6::uuid,$7::uuid)`,
		occurrenceID, scope.WorkspaceID, scope.AgentID, adm.routine.ID, run.ID, task.ID, jobID); err != nil {
		return nil, dispatch.ReasonInternalError, true, fmt.Errorf("employee routine: record decision: %w", err)
	}
	if err := s.routineFault("decision_row"); err != nil {
		return nil, dispatch.ReasonInternalError, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return s.routineSlotConflict(ctx, fire, err)
	}
	s.touchRoutineLastRun(ctx, adm.ap)
	decisions.NotifyRoutineDecisionWake()
	s.captureAutopilotRunStarted(adm.ap, run, run.Source)
	slog.InfoContext(ctx, "employee routine decision admitted", "routine_id", adm.routine.ID, "autopilot_run_id", util.UUIDToString(run.ID),
		"occurrence_id", occurrenceID, "employee_task_id", task.ID, "job_id", jobID, "planned_at", util.TimestampToString(run.PlannedAt))
	return &run, "", true, nil
}

// routineDecisionAdmissionRefusal classifies a wake admission error that will
// not change on retry.
func routineDecisionAdmissionRefusal(err error) *routineRefusal {
	var hold *employeeentry.TaskOriginHold
	switch {
	case err == nil:
		return nil
	case errors.As(err, &hold):
		return failRoutine("employee decision refused: " + hold.Reason)
	case errors.Is(err, employeeentry.ErrTaskWakeOrigin), errors.Is(err, employeeentry.ErrTaskWakeRevoked):
		return failRoutine("employee decision refused: " + err.Error())
	case errors.Is(err, employeeentry.ErrTaskWakeNotReady):
		return skipRoutine(dispatch.ReasonTargetUnavailable, "employee decision is unavailable while a server lacks the routine decision reader")
	case errors.Is(err, employeeentry.ErrNotFound):
		return skipRoutine(dispatch.ReasonTargetUnavailable, "the routine's scene is no longer served")
	}
	return nil
}

// recordRoutineRefusalNewTx records a refused occurrence after the admission
// transaction was rolled back.
func (s *AutopilotService) recordRoutineRefusalNewTx(ctx context.Context, adm routineAdmission, fire routineFire, refusal routineRefusal) (*db.AutopilotRun, dispatch.ReasonCode, bool, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, dispatch.ReasonInternalError, true, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	run, err := s.recordRoutineRefusalTx(ctx, tx, s.Queries.WithTx(tx), adm, fire, refusal)
	if err != nil {
		return s.routineSlotConflict(ctx, fire, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return s.routineSlotConflict(ctx, fire, err)
	}
	s.touchRoutineLastRun(ctx, adm.ap)
	s.publishRoutineRefusal(adm.ap, run, refusal)
	return &run, refusal.code, true, nil
}

// RoutineDecision is the decision record of one employee_decide occurrence.
type RoutineDecision struct {
	OccurrenceID   string
	WorkspaceID    string
	AgentID        string
	RoutineID      string
	AutopilotRunID string
	TaskID         string
	JobID          string
	State          string
	Reason         string
	QueueTaskID    string
}

func lockRoutineDecisionTx(ctx context.Context, tx pgx.Tx, occurrenceID string) (RoutineDecision, error) {
	var d RoutineDecision
	err := tx.QueryRow(ctx, `SELECT occurrence_id::text,workspace_id::text,agent_id::text,routine_id::text,autopilot_run_id::text,employee_task_id::text,job_id::text,state,reason,COALESCE(queue_task_id::text,'')
 FROM employee_routine_decision WHERE occurrence_id=$1::uuid FOR UPDATE`, occurrenceID).Scan(&d.OccurrenceID, &d.WorkspaceID, &d.AgentID, &d.RoutineID, &d.AutopilotRunID, &d.TaskID, &d.JobID, &d.State, &d.Reason, &d.QueueTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrAutomationOriginInvalid
	}
	return d, err
}

// RoutineDecisionDispatch is a committed dispatch decision to wake afterwards.
type RoutineDecisionDispatch struct {
	Queue  db.AgentTaskQueue
	RunID  string
	Replay bool
}

// DispatchRoutineDecisionTx carries out a dispatch decision of the wake jobID
// for its occurrence: it re-checks the routine's current authority and starts
// exactly the frozen packet as the Task's Direct Run, with the AutopilotRun
// mapping and the routine start notice. It writes in tx only; the caller
// commits and then calls NotifyRoutineDecisionDispatch. A replay of the same
// decision returns the same execution.
func (s *AutopilotService) DispatchRoutineDecisionTx(ctx context.Context, tx pgx.Tx, occurrenceID, jobID string) (RoutineDecisionDispatch, error) {
	var out RoutineDecisionDispatch
	host, ok := s.SceneRoutines.(EmployeeRoutineHost)
	if !ok || s.Queries == nil || tx == nil {
		return out, errors.New("employee routine host is unavailable")
	}
	tx, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	qtx := s.Queries.WithTx(tx)
	var locked string
	if err := tx.QueryRow(ctx, `SELECT w.id::text FROM workspace w JOIN employee_routine_decision d ON d.workspace_id=w.id WHERE d.occurrence_id=$1::uuid FOR KEY SHARE OF w`, occurrenceID).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		return out, ErrAutomationOriginInvalid
	} else if err != nil {
		return out, err
	}
	decision, err := lockRoutineDecisionTx(ctx, tx, occurrenceID)
	if err != nil {
		return out, err
	}
	if decision.JobID != jobID {
		return out, ErrAutomationOriginInvalid
	}
	switch decision.State {
	case RoutineDecisionDispatched:
		out.Queue, err = qtx.GetAgentTask(ctx, parseRoutineUUID(decision.QueueTaskID))
		out.Replay = true
		if err == nil {
			err = tx.Commit(ctx)
		}
		return out, err
	case RoutineDecisionPending:
	default:
		return out, ErrRoutineDecisionSettled
	}
	adm, frozen, err := s.loadDecisionAdmissionTx(ctx, tx, qtx, occurrenceID)
	if err != nil {
		return out, err
	}
	if refusal, err := s.verifyRoutineAuthority(ctx, tx, qtx, host, &adm, parseRoutineUUID(frozen.ManualActorID)); err != nil {
		return out, err
	} else if refusal != nil {
		return out, fmt.Errorf("%w: %s", ErrRoutineDecisionRefused, refusal.reason)
	}
	scope := routineTaskScope(adm.routine)
	packet, err := compileRoutinePacket(scope, frozen)
	if err != nil {
		return out, err
	}
	if promptSHA256(packet.Text) != adm.promptSHA {
		// The deterministic compiler must reproduce the frozen packet.
		return out, ErrAutomationOriginInvalid
	}
	task, err := employeetask.NewStore(tx).Get(ctx, scope, decision.TaskID)
	if err != nil {
		return out, err
	}
	if task.State != employeetask.StateReady || task.ActiveRunID != "" {
		return out, ErrRoutineDecisionSettled
	}
	run, err := qtx.GetAutopilotRun(ctx, parseRoutineUUID(decision.AutopilotRunID))
	if err != nil {
		return out, err
	}
	attr, err := routineRunAttribution(ctx, qtx, adm, parseRoutineUUID(frozen.ManualActorID), run)
	if err != nil {
		return out, fmt.Errorf("%w: workspace fail-closed: no accountable human for routine run", ErrRoutineDecisionRefused)
	}
	queueID, err := s.insertRoutineQueueTx(ctx, tx, adm, AutomationOriginSceneRoutine, attr, task, run, packet, occurrenceID)
	if err != nil {
		return out, err
	}
	erun, err := employeetask.NewStore(tx).StartRun(ctx, task.Scope, task.ID, employeetask.StartRunParams{
		Source: employeetask.Source{Namespace: frozen.Source, Key: frozen.EventID + "/run"}, QueueTaskID: util.UUIDToString(queueID), ExpectedVersion: task.Version,
	})
	if err != nil {
		return out, fmt.Errorf("employee routine: start decided run: %w", err)
	}
	if run, err = qtx.UpdateAutopilotRunRunning(ctx, db.UpdateAutopilotRunRunningParams{ID: run.ID, TaskID: queueID}); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_routine_decision SET state='dispatched',employee_run_id=$2::uuid,queue_task_id=$3,decided_at=now(),updated_at=now() WHERE occurrence_id=$1::uuid AND state='pending'`, occurrenceID, erun.ID, queueID); err != nil {
		return out, err
	}
	if err := host.EnqueueRoutineStartNoticeTx(ctx, tx, RoutineStartNotice{Routine: adm.routine, Title: frozen.Title, Run: run, Timezone: adm.timezone}); err != nil {
		return out, fmt.Errorf("employee routine: start notice: %w", err)
	}
	if out.Queue, err = qtx.GetAgentTask(ctx, queueID); err != nil {
		return out, err
	}
	out.RunID = erun.ID
	return out, tx.Commit(ctx)
}

// ErrRoutineDecisionRefused: the routine's current authority no longer allows
// running its instructions; the decision can still stay quiet or reply.
var ErrRoutineDecisionRefused = errors.New("routine decision refused")

// NotifyRoutineDecisionDispatch publishes a committed dispatch decision.
func (s *AutopilotService) NotifyRoutineDecisionDispatch(ctx context.Context, d RoutineDecisionDispatch) {
	if s.TaskSvc != nil && d.Queue.ID.Valid {
		if d.Replay {
			s.TaskSvc.NotifyDirectTaskResult(ctx, DirectTaskResult{Task: d.Queue})
		} else {
			s.TaskSvc.NotifyTaskEnqueued(ctx, d.Queue)
		}
	}
	if host, ok := s.SceneRoutines.(EmployeeRoutineHost); ok {
		host.NotifyRoutineNotices()
	}
}

// loadDecisionAdmissionTx rebuilds a verified admission from the frozen
// receipt and the current routine, autopilot and agent rows.
func (s *AutopilotService) loadDecisionAdmissionTx(ctx context.Context, tx pgx.Tx, qtx *db.Queries, occurrenceID string) (routineAdmission, routineOccurrenceInput, error) {
	var adm routineAdmission
	var frozen routineOccurrenceInput
	var raw []byte
	var routineID, creatorKind, creatorID, state string
	err := tx.QueryRow(ctx, `SELECT routine_id::text,creator_kind,creator_id::text,timezone,config_revision,prompt_sha256,input,state FROM employee_routine_occurrence WHERE id=$1::uuid`, occurrenceID).
		Scan(&routineID, &creatorKind, &creatorID, &adm.timezone, &adm.configRevision, &adm.promptSHA, &raw, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return adm, frozen, ErrAutomationOriginInvalid
	}
	if err != nil {
		return adm, frozen, err
	}
	if state != routineOccurrenceDecision || json.Unmarshal(raw, &frozen) != nil || frozen.Schema != routineOccurrenceSchema {
		return adm, frozen, ErrAutomationOriginInvalid
	}
	routine, err := contextcap.GetRoutine(ctx, tx, frozen.WorkspaceID, frozen.AgentID, routineID)
	if errors.Is(err, contextcap.ErrNotFound) {
		return adm, frozen, fmt.Errorf("%w: the routine was deleted", ErrRoutineDecisionRefused)
	}
	if err != nil {
		return adm, frozen, err
	}
	if routine.SceneID != frozen.SceneID || routine.TenantOrgID != frozen.TenantOrgID || routine.AutopilotID != frozen.AutopilotID {
		return adm, frozen, ErrAutomationOriginInvalid
	}
	ap, err := qtx.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{ID: parseRoutineUUID(routine.AutopilotID), WorkspaceID: parseRoutineUUID(routine.WorkspaceID)})
	if err != nil {
		return adm, frozen, err
	}
	adm.routine, adm.ap, adm.execution = routine, ap, contextcap.RoutineEmployeeDecide
	adm.creator = AutomationPrincipal{Kind: AutomationPrincipalKind(creatorKind), ID: creatorID}
	return adm, frozen, nil
}

// RoutineDecisionOutcome is how a decision ended without a dispatch.
type RoutineDecisionOutcome struct {
	State  string // quiet, waited, replied or failed
	Reason string
}

// SettleRoutineDecisionTx records a decision that ends without a Run: the
// AutopilotRun becomes completed (failed for a failed decision) with the
// decision in its result, and the Task is closed by its own requester without
// a Run. It writes in tx only. A settled or dispatched decision is left alone.
func (s *AutopilotService) SettleRoutineDecisionTx(ctx context.Context, tx pgx.Tx, occurrenceID, jobID string, outcome RoutineDecisionOutcome) (bool, error) {
	switch outcome.State {
	case RoutineDecisionQuiet, RoutineDecisionWaited, RoutineDecisionReplied, RoutineDecisionFailed:
	default:
		return false, employeetask.ErrInvalid
	}
	if tx == nil {
		return false, employeetask.ErrInvalid
	}
	tx, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	decision, err := lockRoutineDecisionTx(ctx, tx, occurrenceID)
	if err != nil {
		return false, err
	}
	if jobID != "" && decision.JobID != jobID {
		return false, ErrAutomationOriginInvalid
	}
	if decision.State != RoutineDecisionPending {
		return false, tx.Commit(ctx)
	}
	reason := strings.TrimSpace(outcome.Reason)
	if len(reason) > 500 {
		reason = reason[:500]
		for len(reason) > 0 && !utf8RuneStart(reason[len(reason)-1]) {
			reason = reason[:len(reason)-1]
		}
	}
	result, _ := json.Marshal(map[string]any{"employee_decision": map[string]any{"state": outcome.State, "reason": reason, "job_id": decision.JobID, "occurrence_id": occurrenceID}})
	if outcome.State == RoutineDecisionFailed {
		failure := "employee decision failed"
		if reason != "" {
			failure += ": " + reason
		}
		_, err = tx.Exec(ctx, `UPDATE autopilot_run SET status='failed',completed_at=now(),failure_reason=$2,result=$3 WHERE id=$1::uuid AND status='running'`, decision.AutopilotRunID, failure, result)
	} else {
		_, err = tx.Exec(ctx, `UPDATE autopilot_run SET status='completed',completed_at=now(),result=$2 WHERE id=$1::uuid AND status='running'`, decision.AutopilotRunID, result)
	}
	if err != nil {
		return false, err
	}
	// Close the Task by its own (automation) requester: no Run exists, so no
	// result, notice or learning is produced for it.
	var scope employeetask.Scope
	var requester string
	if err := tx.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,requester_ref FROM employee_task WHERE id=$1::uuid`, decision.TaskID).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Scene.SceneID, &requester); err != nil {
		return false, err
	}
	scope.Kind = employeetask.ScopeScene
	task, err := employeetask.NewStore(tx).Get(ctx, scope, decision.TaskID)
	if err != nil {
		return false, err
	}
	if task.State != employeetask.StateCancelled {
		body := "routine decision: " + outcome.State
		if reason != "" {
			body += " — " + reason
		}
		if _, _, err := employeetask.NewStore(tx).Stop(ctx, scope, task.ID, employeetask.StopParams{Source: employeetask.Source{Namespace: "scene.routine.decision", Key: decision.JobID}, ActorRef: requester, Body: body, ExpectedVersion: task.Version}); err != nil {
			return false, fmt.Errorf("employee routine: close decision task: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_routine_decision SET state=$2,reason=$3,decided_at=now(),updated_at=now() WHERE occurrence_id=$1::uuid AND state='pending'`, occurrenceID, outcome.State, reason); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// RoutineDecisionByJob returns the decision a routine.decision job settles.
func RoutineDecisionByJob(ctx context.Context, q AutomationOriginQuerier, jobID string) (RoutineDecision, error) {
	var d RoutineDecision
	err := q.QueryRow(ctx, `SELECT occurrence_id::text,workspace_id::text,agent_id::text,routine_id::text,autopilot_run_id::text,employee_task_id::text,job_id::text,state,reason,COALESCE(queue_task_id::text,'')
 FROM employee_routine_decision WHERE job_id=$1::uuid`, jobID).Scan(&d.OccurrenceID, &d.WorkspaceID, &d.AgentID, &d.RoutineID, &d.AutopilotRunID, &d.TaskID, &d.JobID, &d.State, &d.Reason, &d.QueueTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrAutomationOriginInvalid
	}
	return d, err
}

// RoutineDecisionFacts are the frozen rule and decision context of a
// routine.decision wake, read from PostgreSQL for the model as data.
type RoutineDecisionFacts struct {
	Decision     RoutineDecision
	Title        string
	Instructions string
	Cron         string
	Timezone     string
	PlannedLocal string
	Source       string
	SceneKind    string
	Recent       []RoutineOccurrenceOutcome
}

// LoadRoutineDecisionFacts returns the frozen routine rule of the wake's
// occurrence and the last outcomes of the same routine (excluding this one).
func LoadRoutineDecisionFacts(ctx context.Context, q interface {
	AutomationOriginQuerier
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, jobID string, recent int) (RoutineDecisionFacts, error) {
	var f RoutineDecisionFacts
	d, err := RoutineDecisionByJob(ctx, q, jobID)
	if err != nil {
		return f, err
	}
	f.Decision = d
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT input FROM employee_routine_occurrence WHERE id=$1::uuid AND state='decision'`, d.OccurrenceID).Scan(&raw); err != nil {
		return f, err
	}
	var in routineOccurrenceInput
	if json.Unmarshal(raw, &in) != nil {
		return f, ErrAutomationOriginInvalid
	}
	f.Title, f.Instructions, f.Cron, f.Timezone, f.PlannedLocal, f.Source, f.SceneKind = in.Title, in.Instructions, in.Cron, in.Timezone, in.PlannedLocal, in.Source, in.SceneKind
	outcomes, err := ListRoutineOccurrenceOutcomes(ctx, q, d.WorkspaceID, d.RoutineID, min(50, recent+1))
	if err != nil {
		return f, err
	}
	for _, o := range outcomes {
		if o.OccurrenceID != d.OccurrenceID && len(f.Recent) < recent {
			f.Recent = append(f.Recent, o)
		}
	}
	return f, nil
}

// ReconcileRoutineDecisions settles pending decisions whose wake job ended
// without one (held, or completed by a binary that does not settle them).
func (s *AutopilotService) ReconcileRoutineDecisions(ctx context.Context, limit int) (int, error) {
	pool := s.routineDB()
	if pool == nil || s.TxStarter == nil || limit < 1 || limit > 1000 {
		return 0, nil
	}
	// A held job is completed with outcome.held_reason; a completed job that
	// left the decision pending was finished by a binary without the routine
	// decision extension. Both settle as failed; no model is called here.
	rows, err := pool.Query(ctx, `SELECT d.occurrence_id::text,d.job_id::text,j.state,COALESCE(j.outcome->>'held_reason',j.last_error,'') FROM employee_routine_decision d
 JOIN employee_scene_job j ON j.id=d.job_id
 WHERE d.state='pending' AND j.state='completed' AND j.updated_at < now()-interval '10 seconds' ORDER BY d.created_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type pending struct{ occurrence, job, state, reason string }
	var list []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.occurrence, &p.job, &p.state, &p.reason); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, p := range list {
		tx, err := s.TxStarter.Begin(ctx)
		if err != nil {
			return settled, err
		}
		reason := "decision job " + p.state
		if p.reason != "" {
			reason += ": " + p.reason
		}
		ok, err := s.SettleRoutineDecisionTx(ctx, tx, p.occurrence, p.job, RoutineDecisionOutcome{State: RoutineDecisionFailed, Reason: reason})
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return settled, err
		}
		if ok {
			settled++
		}
	}
	return settled, nil
}

// VerifyRoutineOriginAuthority re-checks the current authority of a routine
// origin's principal against the agent: a member by membership and invoke
// rules, an Agent creator as a live same-workspace Agent under the Agent
// invoke rules. It returns a durable hold reason, or an error for storage
// failures. It never substitutes another principal.
func VerifyRoutineOriginAuthority(ctx context.Context, q *db.Queries, origin AutomationTaskOrigin) (string, error) {
	scope := origin.Scope
	workspaceID, agentID := parseRoutineUUID(scope.WorkspaceID), parseRoutineUUID(scope.AgentID)
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && agent.ArchivedAt.Valid) {
		return "agent_archived", nil
	}
	if err != nil {
		return "", err
	}
	facts, ok := SceneRoutineOccurrenceOf(origin.Origin)
	if !ok {
		return "routine_origin_invalid", nil
	}
	principal := origin.Origin.Principal()
	svc := &AutopilotService{Queries: q}
	switch principal.Kind {
	case AutomationPrincipalMember:
		member := parseRoutineUUID(principal.ID)
		if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: member, WorkspaceID: workspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return "routine_principal_revoked", nil
		} else if err != nil {
			return "", err
		}
		if !svc.canMemberInvokeAgent(ctx, agent, member, workspaceID) {
			return "routine_principal_revoked", nil
		}
	case AutomationPrincipalAgent:
		creator, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseRoutineUUID(principal.ID), WorkspaceID: workspaceID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && creator.ArchivedAt.Valid) {
			return "routine_principal_revoked", nil
		}
		if err != nil {
			return "", err
		}
		ap, err := q.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{ID: parseRoutineUUID(facts.AutopilotID), WorkspaceID: workspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "routine_gone", nil
		}
		if err != nil {
			return "", err
		}
		if ap.CreatedByType != string(AutomationPrincipalAgent) || util.UUIDToString(ap.CreatedByID) != principal.ID || !svc.canCreatorInvokeAgent(ctx, ap, agent) {
			return "routine_principal_revoked", nil
		}
	default:
		return "routine_origin_invalid", nil
	}
	return "", nil
}
