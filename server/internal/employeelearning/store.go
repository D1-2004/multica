// Package employeelearning records background consumption of Employee Run evidence.
package employeelearning

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Candidate struct {
	Scope                                    employeetask.Scope
	TaskID, RunID, QueueTaskID, RequesterRef string
	TaskRevision, RunRevision                int64
	State                                    employeetask.State
	FinishedAt                               *time.Time
}
type Result struct{ LearningID, SkipReason string }

// Capture performs Host-authorized database capture; it must never call a model.
type Capture func(context.Context, pgx.Tx, Candidate) (Result, error)
type Store struct{ db DB }

func NewStore(db DB) *Store { return &Store{db: db} }

var ErrInvalid = errors.New("invalid employee learning consumption")

func (s *Store) Process(ctx context.Context, limit int, capture Capture) (int, error) {
	if s == nil || s.db == nil || capture == nil || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT t.workspace_id::text,r.id::text FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id LEFT JOIN employee_learning_consumption c ON c.run_id=r.id WHERE t.owner_loop='employee' AND r.state IN ('succeeded','failed','cancelled') AND c.run_id IS NULL ORDER BY r.finished_at,r.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type ref struct{ workspace, run string }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.workspace, &r.run); err != nil {
			rows.Close()
			return 0, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, r := range refs {
		consumed, err := s.consume(ctx, r.workspace, r.run, capture)
		if err != nil {
			return count, err
		}
		if consumed {
			count++
		}
	}
	return count, nil
}
func (s *Store) consume(ctx context.Context, workspaceID, runID string, capture Capture) (bool, error) {
	tx, err := s.db.Begin(ctx)
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
	var c Candidate
	err = tx.QueryRow(ctx, `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scope_kind,COALESCE(t.scene_id::text,''),COALESCE(t.legacy_id::text,''),t.id::text,r.id::text,r.queue_task_id::text,t.requester_ref,t.goal_revision,r.goal_revision,r.state,r.finished_at FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.workspace_id=$1::uuid AND r.id=$2::uuid AND t.owner_loop='employee' AND r.state IN ('succeeded','failed','cancelled') FOR UPDATE OF t,r`, workspaceID, runID).Scan(&c.Scope.WorkspaceID, &c.Scope.AgentID, &c.Scope.TenantOrgID, &c.Scope.Kind, &c.Scope.Scene.SceneID, &c.Scope.LegacyID, &c.TaskID, &c.RunID, &c.QueueTaskID, &c.RequesterRef, &c.TaskRevision, &c.RunRevision, &c.State, &c.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var consumed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_learning_consumption WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND run_id=$3::uuid)`, c.Scope.WorkspaceID, c.Scope.AgentID, c.RunID).Scan(&consumed); err != nil || consumed {
		return false, err
	}
	result := Result{}
	switch {
	case c.TaskRevision != c.RunRevision:
		result.SkipReason = "stale_goal_revision"
	case c.Scope.Kind != employeetask.ScopeScene:
		result.SkipReason = "unsupported_scope"
	case c.RequesterRef == "" || len(c.RequesterRef) > 256 || strings.TrimSpace(c.RequesterRef) != c.RequesterRef || !utf8.ValidString(c.RequesterRef):
		result.SkipReason = "missing_requester"
	case c.FinishedAt == nil:
		result.SkipReason = "missing_terminal_time"
	default:
		result, err = capture(ctx, tx, c)
		if err != nil {
			return false, err
		}
	}
	if (result.LearningID == "") == (result.SkipReason == "") {
		return false, ErrInvalid
	}
	state := "captured"
	if result.SkipReason != "" {
		state = "skipped"
	}
	_, err = tx.Exec(ctx, `INSERT INTO employee_learning_consumption(workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,queue_task_id,requester_ref,state,learning_id,reason) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,$9,NULLIF($10,'')::uuid,$11)`, c.Scope.WorkspaceID, c.Scope.AgentID, c.Scope.TenantOrgID, c.Scope.Scene.SceneID, c.TaskID, c.RunID, c.QueueTaskID, c.RequesterRef, state, result.LearningID, result.SkipReason)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
