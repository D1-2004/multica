package employeetask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Relation names a Task-to-Task link. Links stay inside one Task scope
// (workspace, agent, tenant and scene) and one requester; a link never grants
// the downstream Task anything the upstream's requester could not already use.
type Relation string

const (
	// RelationBuildsOn: the downstream work packet carries the upstream's
	// latest successful executor report as data (GawkBot DependsOn).
	RelationBuildsOn Relation = "builds_on"
	// RelationBlockedBy: the downstream goal waits until the upstream reaches a
	// real terminal fact (GawkBot BlockedOn/unblockDependentsLocked).
	RelationBlockedBy Relation = "blocked_by"
)

// MaxBuildsOn bounds the upstream Tasks one Task can build on, which also
// bounds the upstream reports rendered into its work packet.
const MaxBuildsOn = 3

// Link is one durable Task-to-Task relation. EntrySeq is the downstream ledger
// entry that recorded it: the request entry for builds_on, the wait_opened
// entry for blocked_by.
type Link struct {
	TaskID        string    `json:"task_id"`
	RelatedTaskID string    `json:"related_task_id"`
	Relation      Relation  `json:"relation"`
	EntrySeq      int64     `json:"entry_seq"`
	CreatedAt     time.Time `json:"created_at"`
}

// TaskLinks lists a Task's outgoing links and the Tasks that link to it.
type TaskLinks struct {
	BuildsOn   []Link `json:"builds_on"`
	BlockedBy  []Link `json:"blocked_by"`
	Dependents []Link `json:"dependents"`
}

// BlockParams makes a v2 goal wait for an upstream Task in the same scope.
type BlockParams struct {
	Source         Source `json:"source"`
	UpstreamTaskID string `json:"upstream_task_id"`
	AuthorityRef   string `json:"authority_ref"`
	// ExpectedVersion is optional; zero skips the aggregate CAS.
	ExpectedVersion int64 `json:"-"`
}

// Reasons specific to Task-to-Task dependencies.
const (
	ReasonUpstreamCompleted   = "upstream_already_completed"
	ReasonUpstreamNotLanded   = "upstream_not_landed"
	ReasonUpstreamNotTerminal = "upstream_not_terminal"
	ReasonDependencyCycle     = "dependency_cycle"
)

// ReleaseOutcome is what one upstream terminal fact did to one dependent.
type ReleaseOutcome string

const (
	// ReleaseSatisfied: the upstream succeeded and the dependent's wait is satisfied.
	ReleaseSatisfied ReleaseOutcome = "released"
	// ReleaseHeld: the upstream failed or was stopped. The work did not land,
	// so the dependent keeps waiting for a human decision.
	ReleaseHeld ReleaseOutcome = "held_not_landed"
	// ReleaseSkipped: the dependent's wait was already resolved or the
	// dependent was stopped.
	ReleaseSkipped ReleaseOutcome = "skipped"
)

type ReleasedDependent struct {
	TaskID  string         `json:"task_id"`
	Outcome ReleaseOutcome `json:"outcome"`
}

type UpstreamRelease struct {
	UpstreamTaskID string              `json:"upstream_task_id"`
	UpstreamState  State               `json:"upstream_state"`
	Dependents     []ReleasedDependent `json:"dependents"`
}

// UpstreamReport is an upstream Task's latest successful executor report. It
// is executor-reported content, never proof of delivery.
type UpstreamReport struct {
	TaskID       string     `json:"task_id"`
	Goal         string     `json:"goal"`
	State        State      `json:"state"`
	RunID        string     `json:"run_id,omitempty"`
	GoalRevision int64      `json:"goal_revision,omitempty"`
	Result       string     `json:"result,omitempty"`
	ResultRef    string     `json:"result_ref,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

func validateBuildsOn(ids []string) error {
	if len(ids) > MaxBuildsOn {
		return fmt.Errorf("%w: at most %d builds_on tasks", ErrInvalid, MaxBuildsOn)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validUUID(id) || seen[id] {
			return fmt.Errorf("%w: builds_on needs distinct task IDs", ErrInvalid)
		}
		seen[id] = true
	}
	return nil
}

// insertBuildsOn links a new Task to upstream Tasks of the same scope and
// requester inside its creating transaction. A mismatch is not_found.
func insertBuildsOn(ctx context.Context, tx pgx.Tx, task Task, upstream []string) error {
	for _, related := range upstream {
		var exists bool
		args := append(scopeArgs(task.Scope), related, task.RequesterRef)
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task WHERE `+taskScope+` AND id=$6::uuid AND requester_ref=$7)`, args...).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := insertLink(ctx, tx, task, related, RelationBuildsOn, 1); err != nil {
			return err
		}
	}
	return nil
}

func insertLink(ctx context.Context, tx pgx.Tx, task Task, related string, relation Relation, seq int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO employee_task_link(workspace_id,agent_id,tenant_org_id,task_id,related_task_id,relation,entry_seq) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7)`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID, related, relation, seq)
	return mapError(err)
}

const linkColumns = `task_id::text, related_task_id::text, relation, entry_seq, created_at`

func scanLinks(rows pgx.Rows, err error) ([]Link, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.TaskID, &l.RelatedTaskID, &l.Relation, &l.EntrySeq, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Links returns a Task's outgoing links and its dependents in this scope.
func (s *Store) Links(ctx context.Context, scope Scope, id string) (TaskLinks, error) {
	if _, err := s.Get(ctx, scope, id); err != nil {
		return TaskLinks{}, err
	}
	outgoing, err := scanLinks(s.db.Query(ctx, `SELECT `+linkColumns+` FROM employee_task_link WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid ORDER BY entry_seq, related_task_id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id))
	if err != nil {
		return TaskLinks{}, err
	}
	out := TaskLinks{BuildsOn: []Link{}, BlockedBy: []Link{}}
	for _, l := range outgoing {
		if l.Relation == RelationBlockedBy {
			out.BlockedBy = append(out.BlockedBy, l)
		} else {
			out.BuildsOn = append(out.BuildsOn, l)
		}
	}
	out.Dependents, err = scanLinks(s.db.Query(ctx, `SELECT `+linkColumns+` FROM employee_task_link WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND related_task_id=$4::uuid ORDER BY created_at, task_id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id))
	return out, err
}

// UpstreamReport reads an upstream Task in scope and its latest successful
// Run. A Task without one has an empty RunID.
func (s *Store) UpstreamReport(ctx context.Context, scope Scope, id string) (UpstreamReport, error) {
	task, err := s.Get(ctx, scope, id)
	if err != nil {
		return UpstreamReport{}, err
	}
	out := UpstreamReport{TaskID: task.ID, Goal: task.Definition.Goal, State: task.State}
	err = s.db.QueryRow(ctx, `SELECT id::text, goal_revision, result, result_ref, finished_at FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND state='succeeded' ORDER BY finished_at DESC NULLS LAST, created_at DESC, id DESC LIMIT 1`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id).Scan(&out.RunID, &out.GoalRevision, &out.Result, &out.ResultRef, &out.FinishedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return UpstreamReport{}, err
	}
	return out, nil
}

// upstreamTerminal reports whether a Task reached a real terminal fact and
// whether its work landed. v1 ends with its Run; v2 ends only by explicit
// completion or a human stop.
func upstreamTerminal(task Task) (terminal, landed bool) {
	switch task.State {
	case StateSucceeded:
		return true, true
	case StateCancelled:
		return true, false
	case StateFailed:
		return task.Lifecycle() == LifecycleV1, false
	}
	return false, false
}

// BlockOnTaskTx makes a v2 goal wait for an upstream Task inside the caller's
// transaction.
func BlockOnTaskTx(ctx context.Context, tx pgx.Tx, scope Scope, id string, p BlockParams) (Task, Link, Wait, error) {
	if tx == nil {
		return Task{}, Link{}, Wait{}, ErrInvalid
	}
	return NewStore(tx).BlockOnTask(ctx, scope, id, p)
}

// BlockOnTask records a blocked_by link and opens a mandatory 'task' wait on a
// v2 goal. The upstream must be another Task of the same scope and requester
// that has not ended yet; a completed upstream needs no wait (use builds_on),
// and an upstream that failed or was stopped would block forever. The
// upstream row is share-locked before the goal, the same order the release
// takes, so an upstream that ends concurrently either is seen as ended here or
// finds this wait when it releases its dependents.
func (s *Store) BlockOnTask(ctx context.Context, scope Scope, id string, p BlockParams) (Task, Link, Wait, error) {
	if err := validateScope(scope); err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	if !validUUID(id) || !validUUID(p.UpstreamTaskID) || p.UpstreamTaskID == id || strings.TrimSpace(p.AuthorityRef) == "" || p.ExpectedVersion < 0 {
		return Task{}, Link{}, Wait{}, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	upstream, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM employee_task WHERE `+taskWhere+` FOR SHARE`, taskArgs(scope, p.UpstreamTaskID)...))
	if err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	task, _, err := NewStore(tx).mutate(ctx, scope, id, p.Source, p, 0, causeWait, func(tx pgx.Tx, task *Task) (Entry, error) {
		if err := goalGate(task, p.ExpectedVersion); err != nil {
			return Entry{}, err
		}
		if task.State == StateSucceeded {
			return Entry{}, lifecycleErr(CodeConflict, ReasonAlreadyCompleted)
		}
		if upstream.RequesterRef != task.RequesterRef {
			return Entry{}, ErrNotFound
		}
		switch terminal, landed := upstreamTerminal(upstream); {
		case terminal && landed:
			return Entry{}, lifecycleErr(CodeConflict, ReasonUpstreamCompleted)
		case terminal:
			return Entry{}, lifecycleErr(CodeConflict, ReasonUpstreamNotLanded)
		}
		// The upstream must not (transitively) wait for this goal.
		var cycle bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE chain(id, depth) AS (
 SELECT related_task_id, 1 FROM employee_task_link WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND relation='blocked_by'
 UNION SELECT l.related_task_id, c.depth+1 FROM employee_task_link l JOIN chain c ON l.task_id=c.id
  WHERE l.workspace_id=$1::uuid AND l.agent_id=$2::uuid AND l.tenant_org_id=$3 AND l.relation='blocked_by' AND c.depth < 32)
 SELECT EXISTS(SELECT 1 FROM chain WHERE id=$5::uuid)`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, p.UpstreamTaskID, id).Scan(&cycle); err != nil {
			return Entry{}, err
		}
		if cycle {
			return Entry{}, lifecycleErr(CodeConflict, ReasonDependencyCycle)
		}
		seq := task.LastEntrySeq + 1
		if _, err := tx.Exec(ctx, `INSERT INTO employee_task_wait(workspace_id,agent_id,tenant_org_id,task_id,kind,ref_id,mandatory,goal_revision,opened_seq)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,true,$7,$8)`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, WaitUpstreamTask, p.UpstreamTaskID, task.GoalRevision, seq); err != nil {
			return Entry{}, err
		}
		if err := insertLink(ctx, tx, *task, p.UpstreamTaskID, RelationBlockedBy, seq); err != nil {
			return Entry{}, err
		}
		state, err := openGoalState(ctx, tx, *task)
		if err != nil {
			return Entry{}, err
		}
		task.State = state
		return Entry{Kind: "wait_opened", Body: "blocked_by " + p.UpstreamTaskID}, nil
	})
	if err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	links, err := scanLinks(tx.Query(ctx, `SELECT `+linkColumns+` FROM employee_task_link WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND relation='blocked_by' AND related_task_id=$5::uuid`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.UpstreamTaskID))
	if err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	if len(links) != 1 {
		return Task{}, Link{}, Wait{}, ErrConflict
	}
	wait, err := waitByRef(ctx, tx, scope, id, WaitUpstreamTask, p.UpstreamTaskID, false)
	if err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Task{}, Link{}, Wait{}, err
	}
	return task, links[0], wait, nil
}

// ReleaseUpstreamWaitTx applies an upstream Task's real terminal fact to every
// goal blocked by it, inside the caller's transaction (the one that recorded
// the terminal fact). It reads the upstream state from PostgreSQL, never from
// an event payload. A succeeded upstream satisfies each open dependent wait;
// a failed or stopped upstream leaves dependents waiting for a human decision.
// A non-terminal upstream is a not_ready refusal. Source identifies the
// terminal fact; each dependent is released at most once.
func ReleaseUpstreamWaitTx(ctx context.Context, tx pgx.Tx, scope Scope, upstreamID string, source Source) (UpstreamRelease, error) {
	if tx == nil || validateScope(scope) != nil || !validUUID(upstreamID) || validateSource(source) != nil {
		return UpstreamRelease{}, ErrInvalid
	}
	if err := lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return UpstreamRelease{}, err
	}
	upstream, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM employee_task WHERE `+taskWhere+` FOR SHARE`, taskArgs(scope, upstreamID)...))
	if err != nil {
		return UpstreamRelease{}, err
	}
	terminal, landed := upstreamTerminal(upstream)
	if !terminal {
		return UpstreamRelease{}, lifecycleErr(CodeNotReady, ReasonUpstreamNotTerminal)
	}
	out := UpstreamRelease{UpstreamTaskID: upstreamID, UpstreamState: upstream.State, Dependents: []ReleasedDependent{}}
	rows, err := tx.Query(ctx, `SELECT task_id::text FROM employee_task_link WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND related_task_id=$4::uuid AND relation='blocked_by' ORDER BY task_id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, upstreamID)
	if err != nil {
		return UpstreamRelease{}, err
	}
	var dependents []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return UpstreamRelease{}, err
		}
		dependents = append(dependents, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return UpstreamRelease{}, err
	}
	store := NewStore(tx)
	for _, dependent := range dependents {
		wait, err := waitByRef(ctx, tx, scope, dependent, WaitUpstreamTask, upstreamID, false)
		if errors.Is(err, ErrNotFound) {
			out.Dependents = append(out.Dependents, ReleasedDependent{dependent, ReleaseSkipped})
			continue
		}
		if err != nil {
			return UpstreamRelease{}, err
		}
		if wait.State != WaitOpen {
			out.Dependents = append(out.Dependents, ReleasedDependent{dependent, ReleaseSkipped})
			continue
		}
		if !landed {
			out.Dependents = append(out.Dependents, ReleasedDependent{dependent, ReleaseHeld})
			continue
		}
		_, _, err = store.ReadyTask(ctx, scope, dependent, ReadyParams{
			Source: Source{Namespace: source.Namespace, Key: source.Key + "/" + dependent}, Kind: WaitUpstreamTask, RefID: upstreamID, Outcome: WaitSatisfied,
			EvidenceRef: "employee_task:" + upstreamID + "/" + string(upstream.State), AuthorityRef: "employee_task_link:" + dependent + "/blocked_by/" + upstreamID,
		})
		if ErrorCodeOf(err) == CodeStopped {
			out.Dependents = append(out.Dependents, ReleasedDependent{dependent, ReleaseSkipped})
			continue
		}
		if err != nil {
			return UpstreamRelease{}, err
		}
		out.Dependents = append(out.Dependents, ReleasedDependent{dependent, ReleaseSatisfied})
	}
	return out, nil
}
