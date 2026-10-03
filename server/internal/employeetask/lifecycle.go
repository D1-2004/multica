package employeetask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LifecycleVersion selects the Task state machine. It is frozen at creation.
type LifecycleVersion int

const (
	// LifecycleV1 is the accepted single-run contract: a Run terminal state
	// becomes the Task state for the same goal revision.
	LifecycleV1 LifecycleVersion = 1
	// LifecycleV2 separates the goal from its Runs. A Run terminal never
	// finishes the goal; only CompleteGoal does.
	LifecycleV2 LifecycleVersion = 2
)

// CompletionMode names how a goal completes.
type CompletionMode string

const (
	CompletionSingleRun    CompletionMode = "single_run"
	CompletionExplicitGoal CompletionMode = "explicit_goal"
)

// Lifecycle returns the Task's state machine; zero (a pre-v2 snapshot) is v1.
func (t Task) Lifecycle() LifecycleVersion {
	if t.LifecycleVersion == LifecycleV2 {
		return LifecycleV2
	}
	return LifecycleV1
}

// ErrNotReady reports a goal that cannot take the requested transition yet:
// an open mandatory wait, an active or unfenced writer, or missing evidence.
var ErrNotReady = errors.New("employee task goal is not ready for this transition")

// ErrorCode is the stable, structured classification of a lifecycle refusal.
type ErrorCode string

const (
	CodeInvalid  ErrorCode = "invalid"
	CodeNotFound ErrorCode = "not_found"
	CodeConflict ErrorCode = "conflict"
	CodeNotReady ErrorCode = "not_ready"
	CodeStopped  ErrorCode = "stopped"
)

// Machine-readable refusal reasons carried by LifecycleError.
const (
	ReasonIllegalTransition    = "illegal_transition"
	ReasonStaleVersion         = "stale_version"
	ReasonStaleGoalRevision    = "stale_goal_revision"
	ReasonInputAfterBoundary   = "input_after_boundary"
	ReasonAlreadyCompleted     = "already_completed"
	ReasonReopenNeedsAmendment = "reopen_requires_amendment"
	ReasonWaitResolved         = "wait_already_resolved"
	ReasonOpenMandatoryWait    = "open_mandatory_wait"
	ReasonActiveWriter         = "active_writer"
	ReasonPendingWriter        = "pending_writer"
	ReasonNoExecutionEvidence  = "no_execution_evidence"
	ReasonRetryNotAuthorized   = "retry_not_authorized"
	ReasonStopped              = "stopped"
	ReasonSingleRunTask        = "single_run_task"
)

// LifecycleError is a structured refusal. errors.Is matches the sentinel of
// its Code (ErrInvalid, ErrNotFound, ErrConflict, ErrNotReady, ErrStopped) and,
// for writer refusals, the established ErrActiveRun / ErrRunNotReady.
type LifecycleError struct {
	Code   ErrorCode `json:"code"`
	Reason string    `json:"reason"`
	From   State     `json:"from,omitempty"`
	To     State     `json:"to,omitempty"`
}

func (e *LifecycleError) Error() string {
	if e.From != "" || e.To != "" {
		return fmt.Sprintf("employee task lifecycle %s: %s (%s -> %s)", e.Code, e.Reason, e.From, e.To)
	}
	return fmt.Sprintf("employee task lifecycle %s: %s", e.Code, e.Reason)
}

func (e *LifecycleError) Unwrap() []error {
	out := []error{}
	switch e.Code {
	case CodeInvalid:
		out = append(out, ErrInvalid)
	case CodeNotFound:
		out = append(out, ErrNotFound)
	case CodeConflict:
		out = append(out, ErrConflict)
	case CodeNotReady:
		out = append(out, ErrNotReady)
	case CodeStopped:
		out = append(out, ErrStopped)
	}
	switch e.Reason {
	case ReasonActiveWriter:
		out = append(out, ErrActiveRun)
	case ReasonPendingWriter, ReasonRetryNotAuthorized:
		out = append(out, ErrRunNotReady)
	}
	return out
}

func lifecycleErr(code ErrorCode, reason string) error {
	return &LifecycleError{Code: code, Reason: reason}
}

// ErrorCodeOf classifies any error this package returns.
func ErrorCodeOf(err error) ErrorCode {
	var lifecycle *LifecycleError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &lifecycle):
		return lifecycle.Code
	case errors.Is(err, ErrStopped):
		return CodeStopped
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrNotReady), errors.Is(err, ErrActiveRun), errors.Is(err, ErrRunNotReady):
		return CodeNotReady
	case errors.Is(err, ErrConflict):
		return CodeConflict
	case errors.Is(err, ErrInvalid):
		return CodeInvalid
	}
	return ""
}

// Active reports whether a state is an active implicit-selection candidate:
// execution is running, or the goal is waiting on its dependencies.
func (s State) Active() bool { return s == StateRunning || s == StateWaiting }

// cause names the operation requesting a Task state write. Every write goes
// through transition with exactly one cause (GawkBot
// transitionLifecycleLocked, 71e82a1809565281cbd0bf8185d3c125b715d934,
// re-implemented: the table is per lifecycle version and refusals are typed).
type cause string

const (
	causeInput          cause = "input"
	causeAmendment      cause = "amendment"
	causeResume         cause = "resume"
	causeStartRun       cause = "start_run"
	causeObserveRun     cause = "observe_backend_run"
	causeRunResult      cause = "run_result"
	causeBindIssue      cause = "bind_issue"
	causeSteer          cause = "steer"
	causeFenceWriter    cause = "fence_writer"
	causeStop           cause = "stop"
	causeWait           cause = "wait"
	causeWaitResolved   cause = "wait_resolved"
	causeCompleteGoal   cause = "complete_goal"
	causeAutonomousWake cause = "autonomous_round"
)

var allCauses = []cause{causeInput, causeAmendment, causeResume, causeStartRun, causeObserveRun, causeRunResult, causeBindIssue, causeSteer, causeFenceWriter, causeStop, causeWait, causeWaitResolved, causeCompleteGoal, causeAutonomousWake}

// AllStates lists every Task state in stable order.
func AllStates() []State {
	return []State{StateReady, StateRunning, StateWaiting, StateSucceeded, StateFailed, StateCancelled}
}

// lifecycleStates lists the states a Task of each version may hold. v1 never
// waits; a v2 goal never fails because a failed Run leaves it awaiting a decision.
var lifecycleStates = map[LifecycleVersion]map[State]bool{
	LifecycleV1: {StateReady: true, StateRunning: true, StateSucceeded: true, StateFailed: true, StateCancelled: true},
	LifecycleV2: {StateReady: true, StateRunning: true, StateWaiting: true, StateSucceeded: true, StateCancelled: true},
}

// lifecycleEdges is the complete legal transition table, from -> to -> causes.
// Self-transitions (no state change) are always legal for a valid state.
// v1 reproduces the accepted pre-v2 behavior exactly, including Coordinator
// Issue observations of a backend run after any terminal state.
var lifecycleEdges = map[LifecycleVersion]map[State]map[State][]cause{
	LifecycleV1: {
		StateReady:     {StateRunning: {causeStartRun, causeObserveRun}, StateCancelled: {causeStop}},
		StateRunning:   {StateReady: {causeRunResult}, StateSucceeded: {causeRunResult}, StateFailed: {causeRunResult}, StateCancelled: {causeRunResult, causeStop}},
		StateSucceeded: {StateReady: {causeResume, causeAmendment, causeSteer}, StateRunning: {causeObserveRun}, StateCancelled: {causeStop}},
		StateFailed:    {StateReady: {causeAmendment, causeSteer}, StateRunning: {causeObserveRun}, StateCancelled: {causeStop}},
		// Steer may reopen only the cancellation its own request made; Steer
		// enforces that precondition before asking for this edge.
		StateCancelled: {StateReady: {causeSteer}, StateRunning: {causeObserveRun}},
	},
	LifecycleV2: {
		StateReady:   {StateRunning: {causeStartRun}, StateWaiting: {causeWait}, StateSucceeded: {causeCompleteGoal}, StateCancelled: {causeStop}},
		StateWaiting: {StateRunning: {causeStartRun}, StateReady: {causeWaitResolved}, StateCancelled: {causeStop}},
		// A Run terminal never finishes a v2 goal.
		StateRunning: {StateReady: {causeRunResult}, StateWaiting: {causeRunResult}, StateCancelled: {causeStop}},
		// Reopening a completed goal needs a new goal revision.
		StateSucceeded: {StateReady: {causeAmendment}, StateCancelled: {causeStop}},
		// A human stop is final for a v2 goal; inputs never lift it.
		StateCancelled: {},
	},
}

// TransitionAllowed reports whether some operation may move a Task of the
// given lifecycle version from one state to another.
func TransitionAllowed(version LifecycleVersion, from, to State) bool {
	states, ok := lifecycleStates[version]
	if !ok || !states[from] || !states[to] {
		return false
	}
	return from == to || len(lifecycleEdges[version][from][to]) > 0
}

func edgeAllows(version LifecycleVersion, from, to State, c cause) bool {
	states, ok := lifecycleStates[version]
	if !ok || !states[from] || !states[to] {
		return false
	}
	if from == to {
		return true
	}
	for _, allowed := range lifecycleEdges[version][from][to] {
		if allowed == c {
			return true
		}
	}
	return false
}

// transition is the single chokepoint for Task state writes after creation.
// mutate calls it for every persisted snapshot, so an operation that computes
// an illegal state fails before anything is written.
func transition(version LifecycleVersion, from, to State, c cause) error {
	if !edgeAllows(version, from, to, c) {
		return &LifecycleError{Code: CodeConflict, Reason: ReasonIllegalTransition, From: from, To: to}
	}
	return nil
}

// WaitKind classifies what a v2 goal is waiting for.
type WaitKind string

const (
	WaitCollection WaitKind = "collection"
	WaitHumanInput WaitKind = "human_input"
	WaitSchedule   WaitKind = "schedule"
	WaitExternal   WaitKind = "external"
)

// WaitState is a wait's own lifecycle; satisfied and cancelled are final.
type WaitState string

const (
	WaitOpen      WaitState = "open"
	WaitSatisfied WaitState = "satisfied"
	WaitCancelled WaitState = "cancelled"
)

// Wait is a durable dependency fact. Task.state is its projection; a wait is
// only satisfied by a Host-verified domain fact, never by a model claim or TTL.
type Wait struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	Kind         WaitKind  `json:"kind"`
	RefID        string    `json:"ref_id"`
	Mandatory    bool      `json:"mandatory"`
	State        WaitState `json:"state"`
	Revision     int64     `json:"revision"`
	GoalRevision int64     `json:"goal_revision"`
	OpenedSeq    int64     `json:"opened_seq"`
	ResolvedSeq  int64     `json:"resolved_seq,omitempty"`
	EvidenceRef  string    `json:"evidence_ref,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// WaitParams opens one wait on a v2 goal. RefID is the owning domain's
// identity (for example a collection ID); one task holds one wait per
// kind/ref. AuthorityRef names the original Task authority that allowed it.
type WaitParams struct {
	Source       Source   `json:"source"`
	Kind         WaitKind `json:"kind"`
	RefID        string   `json:"ref_id"`
	Mandatory    bool     `json:"mandatory"`
	AuthorityRef string   `json:"authority_ref"`
	// Body is an audit note for the requester's ledger. It must not carry
	// another participant's answer.
	Body string `json:"body,omitempty"`
	// ExpectedVersion is optional; zero skips the aggregate CAS.
	ExpectedVersion int64 `json:"-"`
}

// ReadyParams resolves one wait with Host-verified evidence. When no open
// mandatory wait remains, the goal becomes ready for its deciding wake.
type ReadyParams struct {
	Source       Source    `json:"source"`
	Kind         WaitKind  `json:"kind"`
	RefID        string    `json:"ref_id"`
	Outcome      WaitState `json:"outcome"`
	EvidenceRef  string    `json:"evidence_ref"`
	AuthorityRef string    `json:"authority_ref"`
	// ExpectedWaitRevision is optional; zero skips the wait CAS.
	ExpectedWaitRevision int64 `json:"-"`
	// ExpectedVersion is optional; zero skips the aggregate CAS.
	ExpectedVersion int64 `json:"-"`
}

// CompleteGoalParams completes a v2 goal from a deciding wake. The wake froze
// GoalRevision and InputSeq when it read the Task; ExpectedVersion is the
// aggregate version it read. EvidenceRef is required when no Run of the
// current goal revision succeeded (for example a collection summary effect).
type CompleteGoalParams struct {
	Source          Source `json:"source"`
	GoalRevision    int64  `json:"goal_revision"`
	InputSeq        int64  `json:"input_seq"`
	AuthorityRef    string `json:"authority_ref"`
	EvidenceRef     string `json:"evidence_ref,omitempty"`
	Summary         string `json:"summary,omitempty"`
	ExpectedVersion int64  `json:"-"`
}

// AutonomousRoundParams records one non-human wake of a Task, such as a
// collection.ready or execution.follow_up wake. Source is the wake identity,
// so a redelivered wake is counted once.
type AutonomousRoundParams struct {
	Source       Source `json:"source"`
	WakeKind     string `json:"wake_kind"`
	AuthorityRef string `json:"authority_ref"`
	// ExpectedVersion is optional; zero skips the aggregate CAS.
	ExpectedVersion int64 `json:"-"`
}

// WaitTaskTx opens a wait inside the caller's transaction.
func WaitTaskTx(ctx context.Context, tx pgx.Tx, scope Scope, id string, p WaitParams) (Task, Wait, error) {
	if tx == nil {
		return Task{}, Wait{}, ErrInvalid
	}
	return NewStore(tx).WaitTask(ctx, scope, id, p)
}

// ReadyTaskTx resolves a wait inside the caller's transaction.
func ReadyTaskTx(ctx context.Context, tx pgx.Tx, scope Scope, id string, p ReadyParams) (Task, Wait, error) {
	if tx == nil {
		return Task{}, Wait{}, ErrInvalid
	}
	return NewStore(tx).ReadyTask(ctx, scope, id, p)
}

// CompleteGoalTx completes a v2 goal inside the caller's transaction, so the
// completion entry commits atomically with the caller's delivery intent.
func CompleteGoalTx(ctx context.Context, tx pgx.Tx, scope Scope, id string, p CompleteGoalParams) (Task, Entry, error) {
	if tx == nil {
		return Task{}, Entry{}, ErrInvalid
	}
	return NewStore(tx).CompleteGoal(ctx, scope, id, p)
}

// normalizeLifecycle validates the requested lifecycle. v1 is canonicalized to
// zero values so its create payload stays byte-identical to old binaries.
func normalizeLifecycle(p *CreateParams) (LifecycleVersion, CompletionMode, error) {
	switch p.Lifecycle {
	case 0, LifecycleV1:
		if p.CompletionMode != "" && p.CompletionMode != CompletionSingleRun {
			return 0, "", ErrInvalid
		}
		p.Lifecycle, p.CompletionMode = 0, ""
		return LifecycleV1, CompletionSingleRun, nil
	case LifecycleV2:
		// Explicit goals are Employee Direct work in a registered scene.
		if p.CompletionMode != CompletionExplicitGoal || p.OwnerLoop != LoopEmployee || p.DispatchMode != DispatchDirect || p.Scope.Kind != ScopeScene {
			return 0, "", ErrInvalid
		}
		return LifecycleV2, CompletionExplicitGoal, nil
	}
	return 0, "", ErrInvalid
}

// noteHumanInput resets the autonomous-round counter for accepted input from
// a human actor; the governor measures rounds since a human last took part.
func noteHumanInput(task *Task, actor string) {
	if strings.TrimSpace(actor) != "" {
		task.AutonomousRounds = 0
	}
}

func openMandatoryWaits(ctx context.Context, tx pgx.Tx, task Task) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM employee_task_wait WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND state='open' AND mandatory`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID).Scan(&n)
	return n, err
}

// openGoalState projects a v2 goal that is neither stopped nor completed from
// its durable facts: an active Run, then open mandatory waits, else ready.
func openGoalState(ctx context.Context, tx pgx.Tx, task Task) (State, error) {
	if task.ActiveRunID != "" {
		return StateRunning, nil
	}
	n, err := openMandatoryWaits(ctx, tx, task)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return StateWaiting, nil
	}
	return StateReady, nil
}

// unresolvedWriter reports a failed or cancelled Run whose writer has no
// Host fence evidence, so its process may still be producing effects.
func unresolvedWriter(ctx context.Context, tx pgx.Tx, scope Scope, id string) (bool, error) {
	var unresolved bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_run r WHERE r.workspace_id=$1::uuid AND r.agent_id=$2::uuid AND r.tenant_org_id=$3 AND r.task_id=$4::uuid AND r.state IN ('failed','cancelled')
 AND NOT EXISTS(SELECT 1 FROM employee_task_entry e WHERE e.workspace_id=r.workspace_id AND e.agent_id=r.agent_id AND e.tenant_org_id=r.tenant_org_id AND e.task_id=r.task_id AND e.run_id=r.id AND e.kind='writer_fenced'))`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id).Scan(&unresolved)
	return unresolved, err
}

// goalRetryGuard keeps a failed v2 execution from being retried implicitly.
// A failed latest Run needs an explicit, authorized retry action (not yet
// offered). A cancelled latest Run may be succeeded only when a recorded steer
// names it as the cancellation that request made.
func goalRetryGuard(ctx context.Context, tx pgx.Tx, scope Scope, id string) error {
	latest, err := NewStore(tx).LatestRun(ctx, scope, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch latest.State {
	case StateFailed:
		return lifecycleErr(CodeNotReady, ReasonRetryNotAuthorized)
	case StateCancelled:
		var interrupted bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='steer' AND payload->>'interrupted_run_id'=$5)`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, latest.ID).Scan(&interrupted); err != nil {
			return err
		}
		if !interrupted {
			return lifecycleErr(CodeNotReady, ReasonRetryNotAuthorized)
		}
	}
	return nil
}

// steerGoal applies a non-merge steer to an idle v2 goal. A human stop is
// final, a completed goal reopens only with an amendment, and the named
// interruption must be the latest, cancelled Run.
func steerGoal(ctx context.Context, tx pgx.Tx, scope Scope, id string, task *Task, interrupted string) error {
	switch task.State {
	case StateCancelled:
		return lifecycleErr(CodeStopped, ReasonStopped)
	case StateSucceeded:
		return lifecycleErr(CodeConflict, ReasonReopenNeedsAmendment)
	}
	if interrupted != "" {
		latest, err := NewStore(tx).LatestRun(ctx, scope, id)
		if err != nil {
			return err
		}
		if latest.ID != interrupted || latest.State != StateCancelled {
			return ErrConflict
		}
	}
	state, err := openGoalState(ctx, tx, *task)
	if err != nil {
		return err
	}
	task.State = state
	return nil
}

// closeOpenWaits resolves every open wait at the entry the caller is writing.
func closeOpenWaits(ctx context.Context, tx pgx.Tx, task Task, outcome WaitState, evidence string) error {
	_, err := tx.Exec(ctx, `UPDATE employee_task_wait SET state=$5,revision=revision+1,resolved_seq=$6,evidence_ref=$7,updated_at=now()
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND state='open'`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID, outcome, task.LastEntrySeq+1, evidence)
	return err
}

// goalGate refuses operations that a stopped or non-v2 goal cannot take, then
// applies the optional aggregate CAS. A stop is reported before a stale version.
func goalGate(task *Task, expected int64) error {
	if task.Lifecycle() != LifecycleV2 || task.CompletionMode != CompletionExplicitGoal {
		return lifecycleErr(CodeInvalid, ReasonSingleRunTask)
	}
	if task.State == StateCancelled {
		return lifecycleErr(CodeStopped, ReasonStopped)
	}
	if expected > 0 && task.Version != expected {
		return lifecycleErr(CodeConflict, ReasonStaleVersion)
	}
	return nil
}

func validWaitRef(kind WaitKind, ref string) bool {
	switch kind {
	case WaitCollection, WaitHumanInput, WaitSchedule, WaitExternal:
	default:
		return false
	}
	return ref != "" && ref == strings.TrimSpace(ref) && len(ref) <= 512
}

const waitColumns = `id::text, task_id::text, kind, ref_id, mandatory, state, revision, goal_revision,
 opened_seq, COALESCE(resolved_seq,0), evidence_ref, created_at, updated_at`

func scanWait(row pgx.Row) (Wait, error) {
	var w Wait
	err := row.Scan(&w.ID, &w.TaskID, &w.Kind, &w.RefID, &w.Mandatory, &w.State, &w.Revision, &w.GoalRevision, &w.OpenedSeq, &w.ResolvedSeq, &w.EvidenceRef, &w.CreatedAt, &w.UpdatedAt)
	return w, mapError(err)
}

func waitByRef(ctx context.Context, db DB, scope Scope, id string, kind WaitKind, ref string, lock bool) (Wait, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanWait(db.QueryRow(ctx, `SELECT `+waitColumns+` FROM employee_task_wait WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind=$5 AND ref_id=$6`+suffix, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, kind, ref))
}

// WaitTask opens a wait on a v2 goal. A mandatory wait moves an idle goal to
// waiting; a wait opened while a Run is active takes effect when it ends. The
// source replay returns the same wait; one task holds one wait per kind/ref.
func (s *Store) WaitTask(ctx context.Context, scope Scope, id string, p WaitParams) (Task, Wait, error) {
	if !validWaitRef(p.Kind, p.RefID) || strings.TrimSpace(p.AuthorityRef) == "" || p.ExpectedVersion < 0 {
		return Task{}, Wait{}, ErrInvalid
	}
	task, _, err := s.mutate(ctx, scope, id, p.Source, p, 0, causeWait, func(tx pgx.Tx, task *Task) (Entry, error) {
		if err := goalGate(task, p.ExpectedVersion); err != nil {
			return Entry{}, err
		}
		if task.State == StateSucceeded {
			return Entry{}, lifecycleErr(CodeConflict, ReasonAlreadyCompleted)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO employee_task_wait(workspace_id,agent_id,tenant_org_id,task_id,kind,ref_id,mandatory,goal_revision,opened_seq)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9)`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.Kind, p.RefID, p.Mandatory, task.GoalRevision, task.LastEntrySeq+1); err != nil {
			return Entry{}, err
		}
		state, err := openGoalState(ctx, tx, *task)
		if err != nil {
			return Entry{}, err
		}
		task.State = state
		return Entry{Kind: "wait_opened", Body: p.Body}, nil
	})
	if err != nil {
		return Task{}, Wait{}, err
	}
	wait, err := waitByRef(ctx, s.db, scope, id, p.Kind, p.RefID, false)
	return task, wait, err
}

// ReadyTask resolves one open wait with Host-verified evidence and reprojects
// the goal. A duplicate or late resolution is refused, never re-applied.
func (s *Store) ReadyTask(ctx context.Context, scope Scope, id string, p ReadyParams) (Task, Wait, error) {
	if !validWaitRef(p.Kind, p.RefID) || (p.Outcome != WaitSatisfied && p.Outcome != WaitCancelled) || strings.TrimSpace(p.EvidenceRef) == "" || strings.TrimSpace(p.AuthorityRef) == "" || p.ExpectedVersion < 0 || p.ExpectedWaitRevision < 0 {
		return Task{}, Wait{}, ErrInvalid
	}
	task, _, err := s.mutate(ctx, scope, id, p.Source, p, 0, causeWaitResolved, func(tx pgx.Tx, task *Task) (Entry, error) {
		if err := goalGate(task, p.ExpectedVersion); err != nil {
			return Entry{}, err
		}
		wait, err := waitByRef(ctx, tx, scope, id, p.Kind, p.RefID, true)
		if err != nil {
			return Entry{}, err
		}
		if wait.State != WaitOpen {
			return Entry{}, lifecycleErr(CodeConflict, ReasonWaitResolved)
		}
		if p.ExpectedWaitRevision > 0 && wait.Revision != p.ExpectedWaitRevision {
			return Entry{}, lifecycleErr(CodeConflict, ReasonStaleVersion)
		}
		if _, err = tx.Exec(ctx, `UPDATE employee_task_wait SET state=$3,revision=revision+1,resolved_seq=$4,evidence_ref=$5,updated_at=now() WHERE id=$1::uuid AND task_id=$2::uuid`, wait.ID, id, p.Outcome, task.LastEntrySeq+1, p.EvidenceRef); err != nil {
			return Entry{}, err
		}
		state, err := openGoalState(ctx, tx, *task)
		if err != nil {
			return Entry{}, err
		}
		task.State = state
		return Entry{Kind: "wait_resolved", Body: string(p.Kind) + ":" + p.RefID + " " + string(p.Outcome)}, nil
	})
	if err != nil {
		return Task{}, Wait{}, err
	}
	wait, err := waitByRef(ctx, s.db, scope, id, p.Kind, p.RefID, false)
	return task, wait, err
}

var goalInputKinds = []string{"request", "input", "amendment", "steer", "resumed"}

// CompleteGoal records the single completion of a v2 goal. It requires the
// current goal revision and version, every accepted input inside the frozen
// boundary, no open mandatory wait, no active or unfenced writer, and real
// evidence: a Run of this goal revision that succeeded, or EvidenceRef.
// Zero-work completion is a structured not_ready refusal, never a terminal
// state (GawkBot office_eval integrity checks). A source replay returns the
// original entry; at most one completion exists per goal revision.
func (s *Store) CompleteGoal(ctx context.Context, scope Scope, id string, p CompleteGoalParams) (Task, Entry, error) {
	if p.ExpectedVersion <= 0 || p.GoalRevision <= 0 || p.InputSeq <= 0 || strings.TrimSpace(p.AuthorityRef) == "" {
		return Task{}, Entry{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, p.Source, p, 0, causeCompleteGoal, func(tx pgx.Tx, task *Task) (Entry, error) {
		if task.Lifecycle() != LifecycleV2 || task.CompletionMode != CompletionExplicitGoal {
			return Entry{}, lifecycleErr(CodeInvalid, ReasonSingleRunTask)
		}
		switch task.State {
		case StateCancelled:
			return Entry{}, lifecycleErr(CodeStopped, ReasonStopped)
		case StateSucceeded:
			return Entry{}, lifecycleErr(CodeConflict, ReasonAlreadyCompleted)
		}
		if task.Version != p.ExpectedVersion {
			return Entry{}, lifecycleErr(CodeConflict, ReasonStaleVersion)
		}
		if task.GoalRevision != p.GoalRevision {
			return Entry{}, lifecycleErr(CodeConflict, ReasonStaleGoalRevision)
		}
		if p.InputSeq > task.LastEntrySeq {
			return Entry{}, ErrInvalid
		}
		var later bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND seq>$5 AND kind=ANY($6))`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.InputSeq, goalInputKinds).Scan(&later); err != nil {
			return Entry{}, err
		}
		if later {
			return Entry{}, lifecycleErr(CodeConflict, ReasonInputAfterBoundary)
		}
		if task.ActiveRunID != "" {
			return Entry{}, lifecycleErr(CodeNotReady, ReasonActiveWriter)
		}
		unresolved, err := unresolvedWriter(ctx, tx, scope, id)
		if err != nil {
			return Entry{}, err
		}
		if unresolved {
			return Entry{}, lifecycleErr(CodeNotReady, ReasonPendingWriter)
		}
		waits, err := openMandatoryWaits(ctx, tx, *task)
		if err != nil {
			return Entry{}, err
		}
		if waits > 0 {
			return Entry{}, lifecycleErr(CodeNotReady, ReasonOpenMandatoryWait)
		}
		var runID string
		err = tx.QueryRow(ctx, `SELECT id::text FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND goal_revision=$5 AND state='succeeded' ORDER BY created_at DESC, id DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, task.GoalRevision).Scan(&runID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Entry{}, err
		}
		if runID == "" && strings.TrimSpace(p.EvidenceRef) == "" {
			return Entry{}, lifecycleErr(CodeNotReady, ReasonNoExecutionEvidence)
		}
		// Optional waits do not hold the goal; they close with it.
		if err = closeOpenWaits(ctx, tx, *task, WaitCancelled, "goal_completed"); err != nil {
			return Entry{}, err
		}
		task.State = StateSucceeded
		return Entry{Kind: "goal_completed", RunID: runID, Body: p.Summary}, nil
	})
}

// NoteAutonomousRound counts one non-human wake of a Task for the governor.
// A redelivered wake (same source) is counted once; a stopped goal takes none.
func (s *Store) NoteAutonomousRound(ctx context.Context, scope Scope, id string, p AutonomousRoundParams) (Task, Entry, error) {
	if strings.TrimSpace(p.WakeKind) == "" || strings.TrimSpace(p.AuthorityRef) == "" || p.ExpectedVersion < 0 {
		return Task{}, Entry{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, p.Source, p, 0, causeAutonomousWake, func(_ pgx.Tx, task *Task) (Entry, error) {
		if task.State == StateCancelled {
			return Entry{}, lifecycleErr(CodeStopped, ReasonStopped)
		}
		if p.ExpectedVersion > 0 && task.Version != p.ExpectedVersion {
			return Entry{}, lifecycleErr(CodeConflict, ReasonStaleVersion)
		}
		task.AutonomousRounds++
		return Entry{Kind: "autonomous_round", Body: p.WakeKind}, nil
	})
}

// Waits returns a task's waits in the order they were opened.
func (s *Store) Waits(ctx context.Context, scope Scope, id string) ([]Wait, error) {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+waitColumns+` FROM employee_task_wait WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid ORDER BY opened_seq, id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	waits := []Wait{}
	for rows.Next() {
		w, err := scanWait(rows)
		if err != nil {
			return nil, err
		}
		waits = append(waits, w)
	}
	return waits, rows.Err()
}
