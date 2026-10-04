package service

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

// PreparedDirectTask contains one immutable Host input and its resolved overlay.
type PreparedDirectTask struct {
	request     DirectTaskRequest
	contextJSON []byte
	overlay     runtimeMCPOverlayData
}

// TaskVersion is the aggregate version the prepared execution was built from.
func (p PreparedDirectTask) TaskVersion() int64 { return p.request.Task.Version }

// PrepareDirectTask resolves external dependencies before a Host transaction.
func (s *TaskService) PrepareDirectTask(ctx context.Context, request DirectTaskRequest) (PreparedDirectTask, error) {
	if s == nil || s.Queries == nil || !request.PrincipalID.Valid {
		return PreparedDirectTask{}, ErrDirectTaskAccessDenied
	}
	if strings.TrimSpace(request.Prompt) == "" || request.Source.Namespace == "" || request.Source.Key == "" {
		return PreparedDirectTask{}, employeetask.ErrInvalid
	}
	contextJSON, err := directTaskContext(request)
	if err != nil {
		return PreparedDirectTask{}, err
	}
	// Connector resolution can call external services. Recheck admission inside
	// the transaction after resolving it, without holding aggregate locks here.
	var overlay runtimeMCPOverlayData
	if s.Composio != nil {
		agent, err := directTaskAdmissionAgent(ctx, s.Queries, request)
		if err != nil {
			return PreparedDirectTask{}, err
		}
		overlay = s.buildRuntimeMCPOverlay(ctx, request.OriginatorUserID, agent)
	}
	request.Context = append([]byte(nil), request.Context...)
	request.Task.Definition.Deliverables = append([]string(nil), request.Task.Definition.Deliverables...)
	request.Task.Definition.SuccessCriteria = append([]string(nil), request.Task.Definition.SuccessCriteria...)
	request.Task.Definition.AccessNeeded = append([]string(nil), request.Task.Definition.AccessNeeded...)
	overlay.Overlay = append([]byte(nil), overlay.Overlay...)
	overlay.ConnectedApps = append([]byte(nil), overlay.ConnectedApps...)
	return PreparedDirectTask{request: request, contextJSON: contextJSON, overlay: overlay}, nil
}

// ContinueDirectTaskTx resumes a successful goal and queues its next execution.
// A savepoint protects the outer journal transaction when it records a business
// failure. The caller must commit that outer transaction before notifying.
func (s *TaskService) ContinueDirectTaskTx(ctx context.Context, tx pgx.Tx, prepared PreparedDirectTask, resume employeetask.ResumeParams) (DirectTaskResult, error) {
	var out DirectTaskResult
	if s == nil || s.Queries == nil || tx == nil || len(prepared.contextJSON) == 0 {
		return out, employeetask.ErrInvalid
	}
	tx, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	p := prepared.request
	task, err := lockDirectTaskTx(ctx, tx, p)
	if errors.Is(err, employeetask.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return out, employeetask.ErrConflict
	}
	if err != nil {
		return out, err
	}
	if task.OwnerLoop != employeetask.LoopEmployee || task.Scope.Kind != employeetask.ScopeScene || task.IssueID != "" ||
		task.RequesterRef != p.Task.RequesterRef || task.RequesterRef != resume.ActorRef || resume.ExpectedVersion != p.Task.Version {
		return out, employeetask.ErrConflict
	}
	// Compare a frozen replay before checking the current principal. A changed
	// principal is conflicting input, even if that principal has since lost access.
	if _, _, err = directTaskReplayTx(ctx, tx, prepared); err != nil {
		return out, err
	}
	if _, err = directTaskAdmissionAgent(ctx, s.Queries.WithTx(tx), p); err != nil {
		return out, err
	}
	// Resume checks its exact source payload before CAS/state, including when
	// the accepted continuation is now running or already completed.
	resumed, _, err := employeetask.NewStore(tx).Resume(ctx, task.Scope, task.ID, resume)
	if err != nil {
		return out, err
	}
	prepared.request.Task = resumed
	out, err = s.enqueuePreparedDirectTaskTx(ctx, tx, prepared)
	if err != nil {
		return DirectTaskResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DirectTaskResult{}, err
	}
	return out, nil
}

// StartPreparedDirectTaskTx queues the next execution of an idle Employee
// goal (a Host-authorized plan step) inside the caller's transaction. A
// savepoint keeps a business refusal from aborting the outer transaction; an
// exact source replay returns the same queue row. The caller notifies only
// after its outer commit.
func (s *TaskService) StartPreparedDirectTaskTx(ctx context.Context, tx pgx.Tx, prepared PreparedDirectTask) (DirectTaskResult, error) {
	var out DirectTaskResult
	if s == nil || s.Queries == nil || tx == nil || len(prepared.contextJSON) == 0 {
		return out, employeetask.ErrInvalid
	}
	p := prepared.request
	if p.Task.OwnerLoop != employeetask.LoopEmployee || p.Task.Scope.Kind != employeetask.ScopeScene {
		return out, employeetask.ErrInvalid
	}
	tx, err := tx.Begin(ctx)
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
	return out, nil
}

// NotifyDirectTaskResult publishes execution availability after the Host commits.
func (s *TaskService) NotifyDirectTaskResult(ctx context.Context, result DirectTaskResult) {
	if result.Created {
		s.NotifyTaskEnqueued(ctx, result.Task)
		return
	}
	// Recover a commit-before-notify crash without repeating queued analytics.
	// Runtime launch leases and wakeup invalidation already support replay.
	if result.Task.Status == "queued" {
		s.notifyTaskAvailable(result.Task)
		s.launchRuntimeForTaskWithContext(ctx, result.Task)
	}
}
