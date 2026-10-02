package employeetask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB accepts a pool or an existing transaction. Nested pgx transactions use
// savepoints so a host can commit queue writes and task records atomically.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

// Service is the task domain API; authorization and external effects stay in the host.
type Service struct{ *Store }

func NewService(store *Store) *Service { return &Service{Store: store} }

const taskColumns = `id::text, workspace_id::text, agent_id::text, tenant_org_id,
 scope_kind, COALESCE(scene_id::text,''), COALESCE(legacy_id::text,''), owner_loop,
 dispatch_mode, requester_ref, definition, goal_revision, version, state,
 last_entry_seq, COALESCE(active_run_id::text,''), COALESCE(issue_id::text,''), created_at, updated_at`
const taskScope = `workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3
 AND scope_kind=$4 AND COALESCE(scene_id,legacy_id)=$5::uuid`
const taskWhere = taskScope + ` AND id=$6::uuid`
const entryColumns = `task_id::text, seq, kind, source_namespace, source_key, actor_ref,
 goal_revision, COALESCE(run_id::text,''), body, created_at`
const runColumns = `id::text, task_id::text, queue_task_id::text, goal_revision,
 input_seq, state, result, result_ref, created_at, finished_at`

func scopeArgs(scope Scope) []any {
	id := scope.LegacyID
	if scope.Kind == ScopeScene {
		id = scope.Scene.SceneID
	}
	return []any{scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, string(scope.Kind), id}
}
func taskArgs(scope Scope, id string) []any { return append(scopeArgs(scope), id) }

func validUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func validateScope(scope Scope) error {
	if !validUUID(scope.WorkspaceID) || !validUUID(scope.AgentID) || scope.TenantOrgID != strings.TrimSpace(scope.TenantOrgID) {
		return ErrInvalid
	}
	switch scope.Kind {
	case ScopeScene:
		if !validUUID(scope.Scene.SceneID) || scope.LegacyID != "" || scope.TenantOrgID == "" {
			return ErrInvalid
		}
	case ScopeLegacyIssue, ScopeLegacyChat:
		if !validUUID(scope.LegacyID) || scope.Scene.SceneID != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func validateSource(source Source) error {
	if source.Namespace == "" || source.Key == "" || source.Namespace != strings.TrimSpace(source.Namespace) || source.Key != strings.TrimSpace(source.Key) {
		return ErrInvalid
	}
	return nil
}
func validateDefinition(def Definition) error {
	if strings.TrimSpace(def.Goal) == "" {
		return fmt.Errorf("%w: goal is required", ErrInvalid)
	}
	return nil
}
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
	}
	return err
}
func scanTask(row pgx.Row) (Task, error) {
	var task Task
	var definition []byte
	err := row.Scan(&task.ID, &task.Scope.WorkspaceID, &task.Scope.AgentID, &task.Scope.TenantOrgID, &task.Scope.Kind, &task.Scope.Scene.SceneID, &task.Scope.LegacyID, &task.OwnerLoop, &task.DispatchMode, &task.RequesterRef, &definition, &task.GoalRevision, &task.Version, &task.State, &task.LastEntrySeq, &task.ActiveRunID, &task.IssueID, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return Task{}, mapError(err)
	}
	if err = json.Unmarshal(definition, &task.Definition); err != nil {
		return Task{}, err
	}
	return task, nil
}
func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	err := row.Scan(&e.TaskID, &e.Seq, &e.Kind, &e.Source.Namespace, &e.Source.Key, &e.ActorRef, &e.GoalRevision, &e.RunID, &e.Body, &e.CreatedAt)
	return e, mapError(err)
}
func scanRun(row pgx.Row) (Run, error) {
	var run Run
	err := row.Scan(&run.ID, &run.TaskID, &run.QueueTaskID, &run.GoalRevision, &run.InputSeq, &run.State, &run.Result, &run.ResultRef, &run.CreatedAt, &run.FinishedAt)
	return run, mapError(err)
}
func loadTask(ctx context.Context, db DB, scope Scope, id string, lock bool) (Task, error) {
	if err := validateScope(scope); err != nil {
		return Task{}, err
	}
	if !validUUID(id) {
		return Task{}, ErrInvalid
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanTask(db.QueryRow(ctx, `SELECT `+taskColumns+` FROM employee_task WHERE `+taskWhere+suffix, taskArgs(scope, id)...))
}
func (s *Store) Get(ctx context.Context, scope Scope, id string) (Task, error) {
	return loadTask(ctx, s.db, scope, id, false)
}

// lockWorkspace fences task writes against DeleteWorkspace's FOR UPDATE lock.
// Always acquire this parent lock before any task/run lock. An outer transaction
// keeps it after the store's savepoint commits, until queue writes also commit.
func lockWorkspace(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	var id string
	return mapError(tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&id))
}

// Create saves the snapshot and original request together. Source identity is
// scoped to the source scene/legacy locator, allowing one event to contain many actions.
func (s *Store) Create(ctx context.Context, p CreateParams) (Task, error) {
	if err := validateScope(p.Scope); err != nil {
		return Task{}, err
	}
	if err := validateSource(p.Source); err != nil {
		return Task{}, err
	}
	if err := validateDefinition(p.Definition); err != nil {
		return Task{}, err
	}
	if (p.OwnerLoop != LoopEmployee && p.OwnerLoop != LoopCoordinator) || (p.DispatchMode != DispatchDirect && p.DispatchMode != DispatchIssue) || (p.OwnerLoop == LoopEmployee && p.Scope.Kind != ScopeScene) {
		return Task{}, ErrInvalid
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return Task{}, err
	}
	definition, err := json.Marshal(p.Definition)
	if err != nil {
		return Task{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspace(ctx, tx, p.Scope.WorkspaceID); err != nil {
		return Task{}, err
	}
	if p.Scope.Kind == ScopeScene {
		// The directory is the only scene authority; never mint one here.
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_scene WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND id=$4::uuid)`, p.Scope.WorkspaceID, p.Scope.AgentID, p.Scope.TenantOrgID, p.Scope.Scene.SceneID).Scan(&exists)
		if err != nil {
			return Task{}, err
		}
		if !exists {
			return Task{}, ErrNotFound
		}
	}
	task, err := scanTask(tx.QueryRow(ctx, `INSERT INTO employee_task
 (workspace_id,agent_id,tenant_org_id,scope_kind,scene_id,legacy_id,owner_loop,dispatch_mode,requester_ref,definition,source_namespace,source_key,create_payload)
 VALUES($1::uuid,$2::uuid,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8,$9,$10::jsonb,$11,$12,$13::jsonb)
 ON CONFLICT DO NOTHING RETURNING `+taskColumns, p.Scope.WorkspaceID, p.Scope.AgentID, p.Scope.TenantOrgID, p.Scope.Kind, p.Scope.Scene.SceneID, p.Scope.LegacyID, p.OwnerLoop, p.DispatchMode, p.RequesterRef, definition, p.Source.Namespace, p.Source.Key, payload))
	if errors.Is(err, ErrNotFound) {
		args := append(scopeArgs(p.Scope), p.Source.Namespace, p.Source.Key, payload)
		task, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskColumns+` FROM employee_task WHERE `+taskScope+` AND source_namespace=$6 AND source_key=$7 AND create_payload=$8::jsonb`, args...))
		if errors.Is(err, ErrNotFound) {
			return Task{}, ErrConflict
		}
		if err != nil {
			return Task{}, err
		}
	} else if err != nil {
		return Task{}, err
	} else {
		_, err = insertEntry(ctx, tx, task, Entry{TaskID: task.ID, Seq: 1, Kind: "request", Source: p.Source, ActorRef: p.RequesterRef, GoalRevision: 1, Body: p.Input}, payload)
		if err != nil {
			return Task{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return task, nil
}

func insertEntry(ctx context.Context, tx pgx.Tx, task Task, e Entry, payload []byte) (Entry, error) {
	return scanEntry(tx.QueryRow(ctx, `INSERT INTO employee_task_entry
 (workspace_id,agent_id,tenant_org_id,task_id,seq,kind,source_namespace,source_key,actor_ref,goal_revision,run_id,body,payload)
 VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,$12,$13::jsonb) RETURNING `+entryColumns,
		task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID, e.Seq, e.Kind, e.Source.Namespace, e.Source.Key, e.ActorRef, e.GoalRevision, e.RunID, e.Body, payload))
}

// mutate serializes one aggregate, checks a replay before CAS, and persists the
// changed snapshot and entry in one transaction. It never invokes external code.
func (s *Store) mutate(ctx context.Context, scope Scope, id string, source Source, payload any, expected int64, apply func(pgx.Tx, *Task) (Entry, error)) (Task, Entry, error) {
	if err := validateScope(scope); err != nil {
		return Task{}, Entry{}, err
	}
	if !validUUID(id) {
		return Task{}, Entry{}, ErrInvalid
	}
	if err := validateSource(source); err != nil {
		return Task{}, Entry{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Task{}, Entry{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Task{}, Entry{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspace(ctx, tx, scope.WorkspaceID); err != nil {
		return Task{}, Entry{}, err
	}
	task, err := loadTask(ctx, tx, scope, id, true)
	if err != nil {
		return Task{}, Entry{}, err
	}
	var stored []byte
	e, err := scanReplay(tx.QueryRow(ctx, `SELECT `+entryColumns+`,payload FROM employee_task_entry
 WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND source_namespace=$5 AND source_key=$6`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, source.Namespace, source.Key), &stored)
	if err == nil {
		var equal bool
		if err = tx.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, stored, data).Scan(&equal); err != nil {
			return Task{}, Entry{}, err
		}
		if !equal {
			return Task{}, Entry{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return Task{}, Entry{}, err
		}
		return task, e, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Task{}, Entry{}, err
	}
	if expected > 0 && task.Version != expected {
		return Task{}, Entry{}, ErrConflict
	}
	previous := task.Version
	e, err = apply(tx, &task)
	if err != nil {
		return Task{}, Entry{}, mapError(err)
	}
	task.Version++
	task.LastEntrySeq++
	e.TaskID = task.ID
	e.Seq = task.LastEntrySeq
	e.Source = source
	if e.GoalRevision == 0 {
		e.GoalRevision = task.GoalRevision
	}
	definition, err := json.Marshal(task.Definition)
	if err != nil {
		return Task{}, Entry{}, err
	}
	args := append(taskArgs(scope, id), definition, task.GoalRevision, task.Version, task.State, task.LastEntrySeq, task.ActiveRunID, task.IssueID, previous)
	task, err = scanTask(tx.QueryRow(ctx, `UPDATE employee_task SET definition=$7::jsonb,goal_revision=$8,version=$9,state=$10,last_entry_seq=$11,active_run_id=NULLIF($12,'')::uuid,issue_id=NULLIF($13,'')::uuid,updated_at=now() WHERE `+taskWhere+` AND version=$14 RETURNING `+taskColumns, args...))
	if err != nil {
		return Task{}, Entry{}, err
	}
	e, err = insertEntry(ctx, tx, task, e, data)
	if err != nil {
		return Task{}, Entry{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Task{}, Entry{}, err
	}
	return task, e, nil
}
func scanReplay(row pgx.Row, payload *[]byte) (Entry, error) {
	var e Entry
	err := row.Scan(&e.TaskID, &e.Seq, &e.Kind, &e.Source.Namespace, &e.Source.Key, &e.ActorRef, &e.GoalRevision, &e.RunID, &e.Body, &e.CreatedAt, payload)
	return e, mapError(err)
}

// AppendInput distinguishes an authenticated correction from ordinary input.
// Merely asking for progress never advances the goal or restarts execution.
func (s *Store) AppendInput(ctx context.Context, scope Scope, id string, p InputParams) (Task, Entry, error) {
	if p.ExpectedVersion <= 0 || strings.TrimSpace(p.Body) == "" {
		return Task{}, Entry{}, ErrInvalid
	}
	if p.Correction != nil {
		if strings.TrimSpace(p.ActorRef) == "" {
			return Task{}, Entry{}, ErrInvalid
		}
		if err := validateDefinition(*p.Correction); err != nil {
			return Task{}, Entry{}, err
		}
	}
	return s.mutate(ctx, scope, id, p.Source, p, p.ExpectedVersion, func(_ pgx.Tx, task *Task) (Entry, error) {
		e := Entry{Kind: "input", ActorRef: p.ActorRef, Body: p.Body}
		if p.Correction != nil {
			current, _ := json.Marshal(task.Definition)
			next, _ := json.Marshal(p.Correction)
			if string(current) == string(next) {
				return Entry{}, ErrInvalid
			}
			task.Definition = *p.Correction
			task.GoalRevision++
			e.Kind = "amendment"
			// A correction does not claim that an existing worker has stopped, nor
			// does it override a cancellation fence.
			if task.ActiveRunID == "" && task.State != StateCancelled {
				task.State = StateReady
			}
		}
		return e, nil
	})
}

func (s *Store) getRun(ctx context.Context, scope Scope, taskID, runID string) (Run, error) {
	return scanRun(s.db.QueryRow(ctx, `SELECT `+runColumns+` FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, runID))
}

// Resume permits an explicit continuation only after successful completion.
// Failure/cancellation alone is not evidence that the old external writer exited.
func (s *Store) Resume(ctx context.Context, scope Scope, id string, p ResumeParams) (Task, Entry, error) {
	if p.ExpectedVersion <= 0 || strings.TrimSpace(p.ActorRef) == "" || strings.TrimSpace(p.Body) == "" {
		return Task{}, Entry{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, p.Source, p, p.ExpectedVersion, func(_ pgx.Tx, task *Task) (Entry, error) {
		if task.ActiveRunID != "" {
			return Entry{}, ErrActiveRun
		}
		if task.State == StateFailed || task.State == StateCancelled {
			return Entry{}, ErrRunNotReady
		}
		if task.State != StateSucceeded {
			return Entry{}, ErrConflict
		}
		task.State = StateReady
		return Entry{Kind: "resumed", ActorRef: p.ActorRef, Body: p.Body}, nil
	})
}

// StartRun records a queue mapping supplied by the host. Passing NewStore(tx)
// lets the host insert the existing queue row in the same outer transaction.
func (s *Store) StartRun(ctx context.Context, scope Scope, id string, p StartRunParams) (Run, error) {
	if p.ExpectedVersion <= 0 || !validUUID(p.QueueTaskID) || p.InputSeq < 0 {
		return Run{}, ErrInvalid
	}
	_, e, err := s.mutate(ctx, scope, id, p.Source, p, p.ExpectedVersion, func(tx pgx.Tx, task *Task) (Entry, error) {
		if task.DispatchMode == DispatchDirect {
			// A correction or late result can change the goal snapshot to ready,
			// but neither proves that a failed/cancelled external writer exited.
			// Until explicit termination evidence exists, the durable Run history
			// is the final fence for every new Direct execution.
			var unresolvedWriter bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND state IN ('failed','cancelled'))`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id).Scan(&unresolvedWriter)
			if err != nil {
				return Entry{}, err
			}
			if unresolvedWriter {
				return Entry{}, ErrRunNotReady
			}
		}
		if task.ActiveRunID != "" {
			return Entry{}, ErrActiveRun
		}
		if task.State != StateReady {
			return Entry{}, ErrConflict
		}
		inputSeq := p.InputSeq
		if inputSeq == 0 {
			inputSeq = task.LastEntrySeq
		}
		if inputSeq > task.LastEntrySeq {
			return Entry{}, ErrInvalid
		}
		run, err := scanRun(tx.QueryRow(ctx, `INSERT INTO employee_task_run(workspace_id,agent_id,tenant_org_id,task_id,queue_task_id,goal_revision,input_seq)
	  VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7) RETURNING `+runColumns, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.QueueTaskID, task.GoalRevision, inputSeq))
		if err != nil {
			return Entry{}, err
		}
		task.ActiveRunID = run.ID
		task.State = StateRunning
		return Entry{Kind: "run_started", RunID: run.ID}, nil
	})
	if err != nil {
		return Run{}, err
	}
	return s.getRun(ctx, scope, id, e.RunID)
}

// ObserveIssueRun preserves actual legacy execution facts, including retries.
// It cannot dispatch, cannot apply to Direct/Employee-owned work, and verifies
// the queue's authoritative Issue/agent/workspace binding before recording it.
func (s *Store) ObserveIssueRun(ctx context.Context, scope Scope, id string, p ObserveIssueRunParams) (Run, error) {
	if p.ExpectedVersion <= 0 || !validUUID(p.QueueTaskID) || p.InputSeq <= 0 || p.GoalRevision <= 0 {
		return Run{}, ErrInvalid
	}
	current, err := s.Get(ctx, scope, id)
	if err != nil {
		return Run{}, err
	}
	if current.OwnerLoop != LoopCoordinator || current.DispatchMode != DispatchIssue || current.IssueID == "" {
		return Run{}, ErrInvalid
	}
	_, entry, err := s.mutate(ctx, scope, id, p.Source, p, p.ExpectedVersion, func(tx pgx.Tx, task *Task) (Entry, error) {
		if task.ActiveRunID != "" {
			return Entry{}, ErrActiveRun
		}
		if p.InputSeq > task.LastEntrySeq || p.GoalRevision > task.GoalRevision {
			return Entry{}, ErrInvalid
		}
		var accepted bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue q JOIN issue i ON i.id=q.issue_id WHERE q.id=$1::uuid AND q.agent_id=$2::uuid AND q.issue_id=$3::uuid AND i.workspace_id=$4::uuid)`, p.QueueTaskID, scope.AgentID, task.IssueID, scope.WorkspaceID).Scan(&accepted)
		if err != nil {
			return Entry{}, err
		}
		if !accepted {
			return Entry{}, ErrNotFound
		}
		run, err := scanRun(tx.QueryRow(ctx, `INSERT INTO employee_task_run(workspace_id,agent_id,tenant_org_id,task_id,queue_task_id,goal_revision,input_seq) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6,$7) RETURNING `+runColumns, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.QueueTaskID, p.GoalRevision, p.InputSeq))
		if err != nil {
			return Entry{}, err
		}
		task.ActiveRunID = run.ID
		task.State = StateRunning
		return Entry{Kind: "run_started", RunID: run.ID, GoalRevision: p.GoalRevision}, nil
	})
	if err != nil {
		return Run{}, err
	}
	return s.getRun(ctx, scope, id, entry.RunID)
}

// RecordResult retains results against the definition used by that run. A late
// result cannot complete a subsequently amended goal.
func (s *Store) RecordResult(ctx context.Context, scope Scope, id string, p ResultParams) (Task, Run, error) {
	if !validUUID(p.RunID) || (p.State != StateSucceeded && p.State != StateFailed && p.State != StateCancelled) {
		return Task{}, Run{}, ErrInvalid
	}
	task, e, err := s.mutate(ctx, scope, id, p.Source, p, 0, func(tx pgx.Tx, task *Task) (Entry, error) {
		run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid FOR UPDATE`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.RunID))
		if err != nil {
			return Entry{}, err
		}
		if run.State != StateRunning {
			return Entry{}, ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE employee_task_run SET state=$6,result=$7,result_ref=$8,finished_at=now() WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, p.RunID, p.State, p.Result, p.ResultRef)
		if err != nil {
			return Entry{}, err
		}
		if task.ActiveRunID == run.ID {
			task.ActiveRunID = ""
			if task.State != StateCancelled {
				if task.GoalRevision == run.GoalRevision {
					task.State = p.State
				} else {
					task.State = StateReady
				}
			}
		}
		return Entry{Kind: "result", RunID: run.ID, GoalRevision: run.GoalRevision, Body: p.Result}, nil
	})
	if err != nil {
		return Task{}, Run{}, err
	}
	run, err := s.getRun(ctx, scope, id, e.RunID)
	return task, run, err
}

// BindIssue attaches an already-authorized issue; it never dispatches a task or
// changes its audience. The host verifies that the issue belongs to this scope.
func (s *Store) BindIssue(ctx context.Context, scope Scope, id string, p BindIssueParams) (Task, error) {
	if !validUUID(p.IssueID) || p.ExpectedVersion <= 0 {
		return Task{}, ErrInvalid
	}
	task, _, err := s.mutate(ctx, scope, id, p.Source, p, p.ExpectedVersion, func(_ pgx.Tx, task *Task) (Entry, error) {
		if task.IssueID != "" && task.IssueID != p.IssueID {
			return Entry{}, ErrConflict
		}
		task.IssueID = p.IssueID
		return Entry{Kind: "issue_bound", Body: p.IssueID}, nil
	})
	return task, err
}

func (s *Store) ReadEntries(ctx context.Context, scope Scope, id string, afterSeq int64, limit int) ([]Entry, error) {
	if afterSeq < 0 || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	if _, err := s.Get(ctx, scope, id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+entryColumns+` FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND seq>$5 ORDER BY seq LIMIT $6`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, id, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
