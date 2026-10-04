// Package employeeplan stores pre-authorized work plans of lifecycle v2
// (explicit_goal) Employee Tasks and the Host's follow-up decisions on their
// terminal Runs. It performs no model, provider or network calls; the Host
// verifies terminal facts and authority before recording a decision.
package employeeplan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
)

const (
	// MaxSteps bounds a plan, including the first step.
	MaxSteps = 8
	// DefaultStepBudget bounds follow-up actions (dispatches and decision
	// wakes) one plan revision may take without a human.
	DefaultStepBudget = 8
	MaxPromptBytes    = 64000
)

var (
	ErrInvalid  = errors.New("employee plan input is invalid")
	ErrNotFound = errors.New("employee plan not found in scope")
	ErrConflict = errors.New("employee plan conflicts with its accepted revision")
)

type State string

const (
	StateActive     State = "active"
	StatePaused     State = "paused"
	StateCompleted  State = "completed"
	StateSuperseded State = "superseded"
	StateStopped    State = "stopped"
)

type Decision string

const (
	DecisionDispatched Decision = "dispatched"
	DecisionWoken      Decision = "woken"
	DecisionCompleted  Decision = "completed"
	DecisionPaused     Decision = "paused"
	DecisionStopped    Decision = "stopped"
	DecisionStale      Decision = "stale"
)

// Step is one planned execution. Review asks the Host to wake the employee
// for a decision before this step runs; otherwise the Host dispatches it
// deterministically after the previous step succeeds.
type Step struct {
	Prompt string `json:"prompt"`
	Review bool   `json:"review,omitempty"`
}

// Scope is the Task's trusted scene scope.
type Scope struct {
	WorkspaceID string
	AgentID     string
	TenantOrgID string
	SceneID     string
}

func (s Scope) taskScope() employeetask.Scope {
	return employeetask.Scope{WorkspaceID: s.WorkspaceID, AgentID: s.AgentID, TenantOrgID: s.TenantOrgID, Kind: employeetask.ScopeScene, Scene: sceneRef(s.SceneID)}
}

type Plan struct {
	ID           string
	Scope        Scope
	TaskID       string
	Revision     int64
	GoalRevision int64
	Steps        []Step
	StepBudget   int
	FirstRunID   string
	State        State
	StateReason  string
	AuthorityRef string
}

// FollowUp is the Host's single decision on one terminal Run of a plan.
type FollowUp struct {
	ID            string
	PlanID        string
	PlanRevision  int64
	TaskID        string
	RunID         string
	StepIndex     int
	Decision      Decision
	Reason        string
	NextStepIndex int
	NextRunID     string
	NextQueueID   string
	WakeJobID     string
	NoteActionID  string
}

// DB accepts a pool or the caller's transaction.
type DB = employeetask.DB

func sceneRef(id string) scene.Ref { return scene.Ref{SceneID: id} }

func validID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}

func validScope(s Scope) bool {
	return validID(s.WorkspaceID) && validID(s.AgentID) && validID(s.SceneID) && s.TenantOrgID != "" && strings.TrimSpace(s.TenantOrgID) == s.TenantOrgID
}

// ValidSteps checks a plan's shape: 2..MaxSteps non-empty bounded prompts; the
// first step never needs review because it runs at dispatch.
func ValidSteps(steps []Step) bool {
	if len(steps) < 2 || len(steps) > MaxSteps || steps[0].Review {
		return false
	}
	for _, step := range steps {
		if strings.TrimSpace(step.Prompt) == "" || len(step.Prompt) > MaxPromptBytes {
			return false
		}
	}
	return true
}

const planColumns = `id::text,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,task_id::text,plan_revision,goal_revision,steps,step_budget,first_run_id::text,state,state_reason,authority_ref`

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	var steps []byte
	err := row.Scan(&p.ID, &p.Scope.WorkspaceID, &p.Scope.AgentID, &p.Scope.TenantOrgID, &p.Scope.SceneID, &p.TaskID, &p.Revision, &p.GoalRevision, &steps, &p.StepBudget, &p.FirstRunID, &p.State, &p.StateReason, &p.AuthorityRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	if err = json.Unmarshal(steps, &p.Steps); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// CreateParams freezes plan revision 1 of a v2 Task whose first step is the
// already started Run FirstRunID. Source identifies the authorizing action.
type CreateParams struct {
	Scope        Scope
	TaskID       string
	Steps        []Step
	StepBudget   int
	FirstRunID   string
	AuthorityRef string
	Source       employeetask.Source
}

// Create records plan revision 1. The Task must be an Employee explicit-goal
// Task in scope and FirstRunID one of its Runs at the current goal revision.
// A replay with identical content returns the original plan.
func Create(ctx context.Context, db DB, p CreateParams) (Plan, error) {
	if p.StepBudget == 0 {
		p.StepBudget = DefaultStepBudget
	}
	if !validScope(p.Scope) || !validID(p.TaskID) || !validID(p.FirstRunID) || !ValidSteps(p.Steps) || p.StepBudget < 1 || p.StepBudget > DefaultStepBudget ||
		strings.TrimSpace(p.AuthorityRef) == "" || p.Source.Namespace == "" || p.Source.Key == "" {
		return Plan{}, ErrInvalid
	}
	steps, err := json.Marshal(p.Steps)
	if err != nil {
		return Plan{}, err
	}
	task, err := employeetask.NewStore(db).Get(ctx, p.Scope.taskScope(), p.TaskID)
	if errors.Is(err, employeetask.ErrNotFound) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	if task.Lifecycle() != employeetask.LifecycleV2 || task.CompletionMode != employeetask.CompletionExplicitGoal || task.OwnerLoop != employeetask.LoopEmployee || task.DispatchMode != employeetask.DispatchDirect {
		return Plan{}, ErrInvalid
	}
	var runRevision int64
	if err = db.QueryRow(ctx, `SELECT goal_revision FROM employee_task_run WHERE id=$1::uuid AND task_id=$2::uuid AND workspace_id=$3::uuid AND agent_id=$4::uuid AND tenant_org_id=$5`, p.FirstRunID, p.TaskID, p.Scope.WorkspaceID, p.Scope.AgentID, p.Scope.TenantOrgID).Scan(&runRevision); errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrInvalid
	} else if err != nil {
		return Plan{}, err
	}
	if runRevision != task.GoalRevision {
		return Plan{}, ErrConflict
	}
	_, err = db.Exec(ctx, `INSERT INTO employee_task_plan(workspace_id,agent_id,tenant_org_id,scene_id,task_id,plan_revision,goal_revision,steps,step_budget,first_run_id,authority_ref,source_namespace,source_key)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,1,$6,$7::jsonb,$8,$9::uuid,$10,$11,$12) ON CONFLICT DO NOTHING`,
		p.Scope.WorkspaceID, p.Scope.AgentID, p.Scope.TenantOrgID, p.Scope.SceneID, p.TaskID, task.GoalRevision, steps, p.StepBudget, p.FirstRunID, p.AuthorityRef, p.Source.Namespace, p.Source.Key)
	if err != nil {
		return Plan{}, err
	}
	var same bool
	if err = db.QueryRow(ctx, `SELECT workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND goal_revision=$6 AND steps=$7::jsonb AND step_budget=$8 AND first_run_id=$9::uuid AND authority_ref=$10 AND source_namespace=$11 AND source_key=$12
 FROM employee_task_plan WHERE task_id=$5::uuid AND plan_revision=1`, p.Scope.WorkspaceID, p.Scope.AgentID, p.Scope.TenantOrgID, p.Scope.SceneID, p.TaskID, task.GoalRevision, steps, p.StepBudget, p.FirstRunID, p.AuthorityRef, p.Source.Namespace, p.Source.Key).Scan(&same); err != nil {
		return Plan{}, err
	}
	if !same {
		return Plan{}, ErrConflict
	}
	return Get(ctx, db, p.Scope, p.TaskID, 1, false)
}

// Get loads one plan revision in scope; lock takes FOR UPDATE.
func Get(ctx context.Context, db DB, scope Scope, taskID string, revision int64, lock bool) (Plan, error) {
	if !validScope(scope) || !validID(taskID) || revision < 1 {
		return Plan{}, ErrInvalid
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanPlan(db.QueryRow(ctx, `SELECT `+planColumns+` FROM employee_task_plan WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND task_id=$5::uuid AND plan_revision=$6`+suffix, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, taskID, revision))
}

// ByID loads a plan by id in scope.
func ByID(ctx context.Context, db DB, scope Scope, id string, lock bool) (Plan, error) {
	if !validScope(scope) || !validID(id) {
		return Plan{}, ErrInvalid
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanPlan(db.QueryRow(ctx, `SELECT `+planColumns+` FROM employee_task_plan WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND id=$5::uuid`+suffix, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, id))
}

// ForTask returns the newest plan revision of a Task, if any.
func ForTask(ctx context.Context, db DB, scope Scope, taskID string) (Plan, error) {
	if !validScope(scope) || !validID(taskID) {
		return Plan{}, ErrInvalid
	}
	return scanPlan(db.QueryRow(ctx, `SELECT `+planColumns+` FROM employee_task_plan WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND task_id=$5::uuid ORDER BY plan_revision DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, taskID))
}

// StepOfRun returns the step a Run executed: step 1 is the plan's first Run;
// later steps are Runs a recorded follow-up dispatched. ok is false for a Run
// that is not part of the plan (a steer successor, a continuation).
func StepOfRun(ctx context.Context, db DB, plan Plan, runID string) (int, bool, error) {
	if runID == plan.FirstRunID {
		return 1, true, nil
	}
	var step int
	err := db.QueryRow(ctx, `SELECT next_step_index FROM employee_task_follow_up WHERE plan_id=$1::uuid AND task_id=$2::uuid AND next_run_id=$3::uuid`, plan.ID, plan.TaskID, runID).Scan(&step)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return step, err == nil, err
}

// Actions counts the follow-up actions a plan revision has taken.
func Actions(ctx context.Context, db DB, plan Plan) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM employee_task_follow_up WHERE plan_id=$1::uuid AND decision IN ('dispatched','woken')`, plan.ID).Scan(&n)
	return n, err
}

const followUpColumns = `id::text,plan_id::text,plan_revision,task_id::text,run_id::text,step_index,decision,reason,COALESCE(next_step_index,0),COALESCE(next_run_id::text,''),COALESCE(next_queue_task_id::text,''),COALESCE(wake_job_id::text,''),COALESCE(note_action_id,'')`

func scanFollowUp(row pgx.Row) (FollowUp, error) {
	var f FollowUp
	err := row.Scan(&f.ID, &f.PlanID, &f.PlanRevision, &f.TaskID, &f.RunID, &f.StepIndex, &f.Decision, &f.Reason, &f.NextStepIndex, &f.NextRunID, &f.NextQueueID, &f.WakeJobID, &f.NoteActionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FollowUp{}, ErrNotFound
	}
	return f, err
}

// FollowUpOf returns the decision recorded for a plan revision's terminal Run.
func FollowUpOf(ctx context.Context, db DB, plan Plan, runID string) (FollowUp, error) {
	return scanFollowUp(db.QueryRow(ctx, `SELECT `+followUpColumns+` FROM employee_task_follow_up WHERE task_id=$1::uuid AND plan_revision=$2 AND run_id=$3::uuid`, plan.TaskID, plan.Revision, runID))
}

// FollowUpOfWake returns the decision whose wake is jobID.
func FollowUpOfWake(ctx context.Context, db DB, scope Scope, jobID string, lock bool) (FollowUp, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanFollowUp(db.QueryRow(ctx, `SELECT `+followUpColumns+` FROM employee_task_follow_up WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND wake_job_id=$5::uuid`+suffix, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, jobID))
}

// Record writes the single decision for (plan revision, Run). The caller holds
// the Task lock; a second decision for the same Run is ErrConflict.
func Record(ctx context.Context, db DB, plan Plan, f FollowUp) (FollowUp, error) {
	if !validID(f.RunID) || f.StepIndex < 1 || f.StepIndex > len(plan.Steps) || (f.NextRunID != "" && (!validID(f.NextRunID) || !validID(f.NextQueueID) || f.NextStepIndex != f.StepIndex+1)) || (f.WakeJobID != "" && !validID(f.WakeJobID)) {
		return FollowUp{}, ErrInvalid
	}
	switch f.Decision {
	case DecisionDispatched:
		if f.NextRunID == "" {
			return FollowUp{}, ErrInvalid
		}
	case DecisionWoken:
		if f.WakeJobID == "" || f.NextRunID != "" {
			return FollowUp{}, ErrInvalid
		}
	case DecisionCompleted, DecisionPaused, DecisionStopped, DecisionStale:
		if f.NextRunID != "" || f.WakeJobID != "" {
			return FollowUp{}, ErrInvalid
		}
	default:
		return FollowUp{}, ErrInvalid
	}
	next := any(nil)
	if f.NextStepIndex > 0 {
		next = f.NextStepIndex
	}
	if f.Decision == DecisionWoken {
		next = f.StepIndex + 1
	}
	tag, err := db.Exec(ctx, `INSERT INTO employee_task_follow_up(workspace_id,agent_id,tenant_org_id,scene_id,task_id,plan_id,plan_revision,run_id,step_index,decision,reason,next_step_index,next_run_id,next_queue_task_id,wake_job_id,note_action_id)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid,$7,$8::uuid,$9,$10,$11,$12,NULLIF($13,'')::uuid,NULLIF($14,'')::uuid,NULLIF($15,'')::uuid,NULLIF($16,'')) ON CONFLICT DO NOTHING`,
		plan.Scope.WorkspaceID, plan.Scope.AgentID, plan.Scope.TenantOrgID, plan.Scope.SceneID, plan.TaskID, plan.ID, plan.Revision, f.RunID, f.StepIndex, f.Decision, f.Reason, next, f.NextRunID, f.NextQueueID, f.WakeJobID, f.NoteActionID)
	if err != nil {
		return FollowUp{}, err
	}
	if tag.RowsAffected() == 0 {
		return FollowUp{}, ErrConflict
	}
	return FollowUpOf(ctx, db, plan, f.RunID)
}

// AttachNextRun records the Run a decision wake dispatched for the next step.
// It applies once; another Run for the same decision is ErrConflict.
func AttachNextRun(ctx context.Context, db DB, followUpID, runID, queueID string) error {
	if !validID(followUpID) || !validID(runID) || !validID(queueID) {
		return ErrInvalid
	}
	tag, err := db.Exec(ctx, `UPDATE employee_task_follow_up SET next_run_id=$2::uuid,next_queue_task_id=$3::uuid,updated_at=now() WHERE id=$1::uuid AND decision='woken' AND (next_run_id IS NULL OR (next_run_id=$2::uuid AND next_queue_task_id=$3::uuid))`, followUpID, runID, queueID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// SetNote records the deterministic note action of a decision.
func SetNote(ctx context.Context, db DB, followUpID, actionID string) error {
	_, err := db.Exec(ctx, `UPDATE employee_task_follow_up SET note_action_id=$2,updated_at=now() WHERE id=$1::uuid AND (note_action_id IS NULL OR note_action_id=$2)`, followUpID, actionID)
	return err
}

// SetState moves a plan out of active; terminal plan states are final.
func SetState(ctx context.Context, db DB, plan Plan, state State, reason string) error {
	switch state {
	case StatePaused, StateCompleted, StateSuperseded, StateStopped:
	default:
		return ErrInvalid
	}
	_, err := db.Exec(ctx, `UPDATE employee_task_plan SET state=$2,state_reason=$3,updated_at=now() WHERE id=$1::uuid AND state='active'`, plan.ID, state, reason)
	return err
}
