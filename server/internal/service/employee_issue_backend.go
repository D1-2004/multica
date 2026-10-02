package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const coordinatorIssueInputNamespace = "coordinator_issue_input"

type EmployeeIssueCreateParams struct {
	Intent  employeetask.CreateParams
	Issue   IssueCreateParams
	Options IssueCreateOpts
}
type EmployeeIssueContinueParams struct {
	Comment   IssueCommentCreateParams
	ActorRef  string
	Proactive bool
}

// EmployeeIssueBackend reuses the ordinary Issue services and records their
// actual executions. It never dispatches a second queue task for the same work.
type EmployeeIssueBackend struct {
	Issues   *IssueService
	Comments *IssueCommentService
}

func NewEmployeeIssueBackend(issues *IssueService, comments *IssueCommentService) *EmployeeIssueBackend {
	return &EmployeeIssueBackend{Issues: issues, Comments: comments}
}

type employeeIssueAnalytics struct{ events []analytics.Event }

func (b *employeeIssueAnalytics) Capture(event analytics.Event) { b.events = append(b.events, event) }
func (*employeeIssueAnalytics) Close()                          {}

func (b *EmployeeIssueBackend) transact(ctx context.Context, apply func(*EmployeeIssueBackend, pgx.Tx) error, queues *[]db.AgentTaskQueue) error {
	if b == nil || b.Issues == nil || b.Issues.TaskService == nil || b.Issues.TxStarter == nil {
		return errors.New("employee Issue backend is not configured")
	}
	tx, err := b.Issues.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := b.Issues.Queries.WithTx(tx)
	var buffered []events.Event
	bus := events.New()
	bus.SubscribeAll(func(event events.Event) { buffered = append(buffered, event) })
	ac := &employeeIssueAnalytics{}
	tasks := &TaskService{Queries: q, TxStarter: tx, Bus: bus, FeatureFlags: b.Issues.TaskService.FeatureFlags, Composio: b.Issues.TaskService.Composio}
	bound := NewEmployeeIssueBackend(NewIssueService(q, tx, bus, ac, tasks), NewIssueCommentService(q, bus, tasks))
	if err = apply(bound, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, event := range buffered {
		if b.Issues.Bus != nil {
			b.Issues.Bus.Publish(event)
		}
	}
	for _, event := range ac.events {
		obsmetrics.RecordEvent(b.Issues.Analytics, b.Issues.Metrics, event)
	}
	for _, queue := range *queues {
		if queue.ID.Valid && queue.Status == "queued" {
			b.Issues.TaskService.NotifySteerPredecessor(ctx, queue)
			b.Issues.TaskService.NotifyTaskEnqueued(ctx, queue)
		}
	}
	return nil
}
func (b *EmployeeIssueBackend) Create(ctx context.Context, p EmployeeIssueCreateParams) (IssueCreateResult, error) {
	var result IssueCreateResult
	queues := []db.AgentTaskQueue{}
	err := b.transact(ctx, func(bound *EmployeeIssueBackend, tx pgx.Tx) error {
		var err error
		result, err = bound.CreateInTx(ctx, tx, p)
		if result.EnqueuedTask != nil {
			queues = append(queues, *result.EnqueuedTask)
		}
		return err
	}, &queues)
	return result, err
}
func (b *EmployeeIssueBackend) Continue(ctx context.Context, p EmployeeIssueContinueParams) (IssueCommentCreateResult, error) {
	var result IssueCommentCreateResult
	queues := []db.AgentTaskQueue{}
	err := b.transact(ctx, func(bound *EmployeeIssueBackend, tx pgx.Tx) error {
		var err error
		result, err = bound.ContinueInTx(ctx, tx, p)
		queues = append(queues, result.Task)
		return err
	}, &queues)
	return result, err
}
func lockEmployeeIssueWorkspace(ctx context.Context, tx pgx.Tx, id pgtype.UUID) error {
	var found pgtype.UUID
	return tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, id).Scan(&found)
}

// CreateInTx requires transaction-bound services and a caller that buffers their
// events/analytics and only wakes the queue after committing the outer transaction.
func (b *EmployeeIssueBackend) CreateInTx(ctx context.Context, tx pgx.Tx, p EmployeeIssueCreateParams) (IssueCreateResult, error) {
	if p.Issue.AssigneeType.String != "agent" || p.Intent.DispatchMode != employeetask.DispatchIssue || p.Intent.Scope.WorkspaceID != util.UUIDToString(p.Issue.WorkspaceID) || p.Intent.Scope.AgentID != util.UUIDToString(p.Issue.AssigneeID) {
		return IssueCreateResult{}, employeetask.ErrInvalid
	}
	if err := lockEmployeeIssueWorkspace(ctx, tx, p.Issue.WorkspaceID); err != nil {
		return IssueCreateResult{}, err
	}
	q := b.Issues.Queries
	lockParts, _ := json.Marshal([]string{p.Intent.Scope.WorkspaceID, p.Intent.Scope.AgentID, p.Intent.Scope.TenantOrgID, string(p.Intent.Scope.Kind), p.Intent.Scope.Scene.SceneID, p.Intent.Scope.LegacyID, p.Intent.Source.Namespace, p.Intent.Source.Key})
	if err := q.LockExternalIssueFollowUp(ctx, "employee-issue-create:"+string(lockParts)); err != nil {
		return IssueCreateResult{}, err
	}
	store := employeetask.NewStore(tx)
	// A legacy dispatch with no registered scene acquires its real Issue locator
	// after creation. It never fabricates a SceneRef or a synthetic Issue UUID.
	legacyNew := p.Intent.Scope.Kind == employeetask.ScopeLegacyIssue && p.Intent.Scope.LegacyID == ""
	var task employeetask.Task
	if legacyNew {
		var locator string
		err := tx.QueryRow(ctx, `SELECT legacy_id::text FROM employee_task WHERE workspace_id=$1 AND agent_id=$2 AND tenant_org_id=$3 AND scope_kind='legacy_issue' AND source_namespace=$4 AND source_key=$5`, p.Issue.WorkspaceID, p.Issue.AssigneeID, p.Intent.Scope.TenantOrgID, p.Intent.Source.Namespace, p.Intent.Source.Key).Scan(&locator)
		if err == nil {
			p.Intent.Scope.LegacyID = locator
			legacyNew = false
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return IssueCreateResult{}, err
		}
	}
	if !legacyNew {
		var err error
		task, err = store.Create(ctx, p.Intent)
		if err != nil {
			return IssueCreateResult{}, err
		}
		if task.IssueID != "" {
			return b.recoverCreate(ctx, tx, task)
		}
	}
	result, err := b.Issues.Create(ctx, p.Issue, p.Options)
	if err != nil {
		return result, err
	}
	if result.EnqueuedTask == nil || !result.EnqueuedTask.ID.Valid {
		return IssueCreateResult{}, errors.New("employee Issue did not enqueue its assigned work")
	}
	if legacyNew {
		p.Intent.Scope.LegacyID = util.UUIDToString(result.Issue.ID)
		task, err = store.Create(ctx, p.Intent)
		if err != nil {
			return IssueCreateResult{}, err
		}
	}
	task, err = store.BindIssue(ctx, task.Scope, task.ID, employeetask.BindIssueParams{Source: employeetask.Source{Namespace: "issue_binding", Key: util.UUIDToString(result.Issue.ID)}, IssueID: util.UUIDToString(result.Issue.ID), ExpectedVersion: task.Version})
	if err != nil {
		return IssueCreateResult{}, err
	}
	if err = mapEmployeeIssueQueue(ctx, tx, task, *result.EnqueuedTask, p.Intent.RequesterRef, p.Intent.Input, task.LastEntrySeq); err != nil {
		return IssueCreateResult{}, err
	}
	return result, nil
}
func (b *EmployeeIssueBackend) recoverCreate(ctx context.Context, tx pgx.Tx, task employeetask.Task) (IssueCreateResult, error) {
	id, err := util.ParseUUID(task.IssueID)
	if err != nil {
		return IssueCreateResult{}, err
	}
	ws, err := util.ParseUUID(task.Scope.WorkspaceID)
	if err != nil {
		return IssueCreateResult{}, err
	}
	issue, err := b.Issues.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return IssueCreateResult{}, err
	}
	var queueID pgtype.UUID
	err = tx.QueryRow(ctx, `SELECT queue_task_id FROM employee_task_run WHERE workspace_id=$1 AND agent_id=$2::uuid AND task_id=$3::uuid ORDER BY created_at,id LIMIT 1`, ws, task.Scope.AgentID, task.ID).Scan(&queueID)
	if err != nil {
		return IssueCreateResult{}, err
	}
	queue, err := b.Issues.Queries.GetAgentTask(ctx, queueID)
	return IssueCreateResult{Issue: issue, EnqueuedTask: &queue, AssignedTaskID: queue.ID}, err
}
func employeeTaskByIssue(ctx context.Context, tx pgx.Tx, issue db.Issue) (employeetask.Task, error) {
	var id string
	var scope employeetask.Scope
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,agent_id::text,tenant_org_id,scope_kind,COALESCE(scene_id::text,''),COALESCE(legacy_id::text,'') FROM employee_task WHERE workspace_id=$1 AND agent_id=$2 AND issue_id=$3 AND dispatch_mode='issue'`, issue.WorkspaceID, issue.AssigneeID, issue.ID).Scan(&id, &scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Kind, &scope.Scene.SceneID, &scope.LegacyID)
	if err != nil {
		return employeetask.Task{}, err
	}
	return employeetask.NewStore(tx).Get(ctx, scope, id)
}

// ContinueInTx adopts historical Issues without re-enqueuing their old request.
// The original creator is provenance, not an assertion about today's human author.
func (b *EmployeeIssueBackend) ContinueInTx(ctx context.Context, tx pgx.Tx, p EmployeeIssueContinueParams) (IssueCommentCreateResult, error) {
	if strings.TrimSpace(p.ActorRef) == "" || strings.TrimSpace(p.Comment.IdempotencyKey) == "" {
		return IssueCommentCreateResult{}, employeetask.ErrInvalid
	}
	issue := p.Comment.Issue
	if err := lockEmployeeIssueWorkspace(ctx, tx, issue.WorkspaceID); err != nil {
		return IssueCommentCreateResult{}, err
	}
	if _, err := b.Issues.Queries.LockIssueForExternalFollowUp(ctx, db.LockIssueForExternalFollowUpParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		return IssueCommentCreateResult{}, err
	}
	current, err := b.Issues.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	if current.AssigneeID != issue.AssigneeID || current.AssigneeType.String != "agent" {
		return IssueCommentCreateResult{}, errors.New("continuation assignee changed")
	}
	p.Comment.Issue = current
	store := employeetask.NewStore(tx)
	task, err := employeeTaskByIssue(ctx, tx, current)
	if errors.Is(err, pgx.ErrNoRows) {
		scope := employeetask.Scope{WorkspaceID: util.UUIDToString(current.WorkspaceID), AgentID: util.UUIDToString(current.AssigneeID), Kind: employeetask.ScopeLegacyIssue, LegacyID: util.UUIDToString(current.ID)}
		task, err = store.Create(ctx, employeetask.CreateParams{Scope: scope, OwnerLoop: employeetask.LoopCoordinator, DispatchMode: employeetask.DispatchIssue, RequesterRef: "issue_creator:" + current.CreatorType + ":" + util.UUIDToString(current.CreatorID), Definition: employeetask.Definition{Goal: current.Title}, Source: employeetask.Source{Namespace: "legacy_issue_adoption", Key: util.UUIDToString(current.ID)}})
		if err != nil {
			return IssueCommentCreateResult{}, err
		}
		task, err = store.BindIssue(ctx, task.Scope, task.ID, employeetask.BindIssueParams{Source: employeetask.Source{Namespace: "issue_binding", Key: util.UUIDToString(current.ID)}, IssueID: util.UUIDToString(current.ID), ExpectedVersion: task.Version})
	}
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	task, err = refreshEmployeeIssueTerminal(ctx, tx, task)
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	var replay bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_entry WHERE task_id=$1::uuid AND workspace_id=$2 AND source_namespace=$3 AND source_key=$4)`, task.ID, current.WorkspaceID, coordinatorIssueInputNamespace, p.Comment.IdempotencyKey).Scan(&replay); err != nil {
		return IssueCommentCreateResult{}, err
	}
	if !replay && task.OwnerLoop != employeetask.LoopCoordinator && (task.State == employeetask.StateFailed || task.State == employeetask.StateCancelled) {
		return IssueCommentCreateResult{}, employeetask.ErrRunNotReady
	}
	var input employeetask.Entry
	task, input, err = store.AppendInput(ctx, task.Scope, task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: coordinatorIssueInputNamespace, Key: p.Comment.IdempotencyKey}, ActorRef: p.ActorRef, Body: p.Comment.Content, ExpectedVersion: task.Version})
	if err != nil {
		return IssueCommentCreateResult{}, err
	}
	var result IssueCommentCreateResult
	if p.Proactive {
		result, err = b.Comments.QueueCoordinatorFollowUp(ctx, p.Comment)
	} else {
		result, err = b.Comments.CreateExternalFollowUp(ctx, p.Comment, IssueCommentCreateOpts{})
	}
	if err != nil {
		return result, err
	}
	if result.Task.ID.Valid {
		err = mapEmployeeIssueQueue(ctx, tx, task, result.Task, p.ActorRef, p.Comment.Content, input.Seq)
	}
	return result, err
}
