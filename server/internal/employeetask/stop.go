package employeetask

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// StopParams identifies the exact execution selected by a Host-authorized read.
// Empty execution IDs mean that the Task has never admitted a Run.
type StopParams struct {
	Source          Source `json:"source"`
	ActorRef        string `json:"actor_ref"`
	Body            string `json:"body"`
	RunID           string `json:"run_id,omitempty"`
	QueueTaskID     string `json:"queue_task_id,omitempty"`
	ExpectedVersion int64  `json:"-"`
}

type stopPayload struct {
	Operation string `json:"operation"`
	StopParams
}

// Stop closes the goal without claiming a process exited or rewriting a Run's
// facts. The Host must cancel its exact queue and record that terminal fact in
// the same outer transaction. A source replay never selects a newer execution.
func (s *Store) Stop(ctx context.Context, scope Scope, id string, p StopParams) (Task, Entry, error) {
	if p.ExpectedVersion <= 0 || strings.TrimSpace(p.ActorRef) == "" || strings.TrimSpace(p.Body) == "" || (p.RunID == "") != (p.QueueTaskID == "") || p.RunID != "" && (!validUUID(p.RunID) || !validUUID(p.QueueTaskID)) {
		return Task{}, Entry{}, ErrInvalid
	}
	return s.mutate(ctx, scope, id, p.Source, stopPayload{Operation: "stop", StopParams: p}, p.ExpectedVersion, func(tx pgx.Tx, task *Task) (Entry, error) {
		if task.OwnerLoop != LoopEmployee || task.DispatchMode != DispatchDirect || task.Scope.Kind != ScopeScene || task.IssueID != "" || task.RequesterRef != p.ActorRef {
			return Entry{}, ErrInvalid
		}
		latest, err := NewStore(tx).LatestRun(ctx, scope, id)
		if p.RunID == "" {
			if !errors.Is(err, ErrNotFound) {
				if err != nil {
					return Entry{}, err
				}
				return Entry{}, ErrConflict
			}
		} else {
			if err != nil {
				return Entry{}, err
			}
			if latest.ID != p.RunID || latest.QueueTaskID != p.QueueTaskID {
				return Entry{}, ErrConflict
			}
		}
		if task.ActiveRunID != "" && task.ActiveRunID != p.RunID {
			return Entry{}, ErrConflict
		}
		task.State, task.ActiveRunID = StateCancelled, ""
		return Entry{Kind: "input", ActorRef: p.ActorRef, Body: p.Body, RunID: p.RunID}, nil
	})
}
