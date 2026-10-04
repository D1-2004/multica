package handler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type webCoordinatorAnalytics struct{ events []analytics.Event }

func (b *webCoordinatorAnalytics) Capture(event analytics.Event) { b.events = append(b.events, event) }
func (b *webCoordinatorAnalytics) Close()                        {}

// persistWebCoordinatorPlan commits every new/continued item and the visible
// chat transcript together. The Web send protocol has no stable client send
// identifier, so a partial commit cannot safely be retried as a fresh request.
func (h *Handler) persistWebCoordinatorPlan(ctx context.Context, session db.ChatSession, userID pgtype.UUID, content string, decision inboundcoord.Decision, trace chattrace.Trace) (*service.CoordinatorChatTurn, inboundcoord.Decision, error) {
	if h == nil || h.Queries == nil || h.TaskService == nil || h.TaskService.TxStarter == nil {
		return nil, decision, errors.New("coordinator chat transaction is not configured")
	}
	if decision.Action != inboundcoord.ActionReply && decision.Action != inboundcoord.ActionIssue {
		return nil, decision, errors.New("coordinator decision does not authorize a chat commit")
	}
	if decision.Action == inboundcoord.ActionIssue && (len(decision.Items) == 0 || decision.PlanVersion != "window-plan-v1") {
		return nil, decision, errors.New("coordinator work requires a complete window plan")
	}
	if decision.Action == inboundcoord.ActionReply && len(decision.Items) > 0 {
		return nil, decision, errors.New("a reply does not authorize work items")
	}
	tx, err := h.TaskService.TxStarter.Begin(ctx)
	if err != nil {
		return nil, decision, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockWorkspaceForChatSessionCreate(ctx, session.WorkspaceID); err != nil {
		return nil, decision, err
	}
	if _, err := qtx.LockChatSessionForRuntimeBind(ctx, session.ID); err != nil {
		return nil, decision, fmt.Errorf("lock coordinator chat session: %w", err)
	}
	currentSession, err := qtx.GetChatSession(ctx, session.ID)
	if err != nil || currentSession.Status != "active" || currentSession.AgentID != session.AgentID || currentSession.WorkspaceID != session.WorkspaceID {
		return nil, decision, errors.New("chat session changed before coordinator commit")
	}
	var bufferedEvents []events.Event
	bufferedBus := events.New()
	bufferedBus.SubscribeAll(func(event events.Event) { bufferedEvents = append(bufferedEvents, event) })
	analyticsBuffer := &webCoordinatorAnalytics{}
	// pgx transactions implement Begin with savepoints. Existing services keep
	// their normal invariants while the outer transaction owns visibility.
	txTasks := &service.TaskService{Queries: qtx, TxStarter: tx, Bus: bufferedBus, FeatureFlags: h.TaskService.FeatureFlags, Composio: h.TaskService.Composio}
	txIssues := service.NewIssueService(qtx, tx, bufferedBus, analyticsBuffer, txTasks)
	txComments := service.NewIssueCommentService(qtx, bufferedBus, txTasks)
	issueBackend := service.NewEmployeeIssueBackend(txIssues, txComments)
	committed := decision
	committed.IssueResults = nil
	committed.CompletedActionKeys = nil
	var tasks []db.AgentTaskQueue
	prefix := h.getIssuePrefix(ctx, session.WorkspaceID)
	for i, item := range decision.Items {
		if item.ActionKey == "" {
			return nil, decision, fmt.Errorf("window item %d has no stable action key", i+1)
		}
		itemDecision := decision.ForWindowItem(item)
		itemContent := item.Content
		if itemContent == "" {
			itemContent = content
		}
		raw := inboundcoord.StampCoordinatorTrace(nil, decision, trace.Channel, time.UnixMilli(trace.StartedAtUnixMS))
		var outcome protocol.ChatCoordinatorIssueResult
		var task db.AgentTaskQueue
		if item.IssueID != "" {
			issueID, err := util.ParseUUID(item.IssueID)
			if err != nil {
				return nil, decision, fmt.Errorf("invalid continuation issue: %w", err)
			}
			issue, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: session.WorkspaceID})
			if err != nil || issue.AssigneeID != session.AgentID || issue.AssigneeType.String != "agent" {
				return nil, decision, errors.New("continuation target is not owned by this chat agent")
			}
			result, err := issueBackend.ContinueInTx(ctx, tx, service.EmployeeIssueContinueParams{Comment: service.IssueCommentCreateParams{
				Issue: issue, AuthorID: userID, Content: inboundcoord.ContinuationContent(item), DispatchContext: raw,
				IdempotencyKey: "web:" + util.UUIDToString(session.ID) + ":" + trace.TraceID + ":" + item.ActionKey,
			}, ActorRef: "member:" + util.UUIDToString(userID)})
			if err != nil {
				return nil, decision, err
			}
			task = result.Task
			outcome = protocol.ChatCoordinatorIssueResult{Action: "issue_commented", IssueID: util.UUIDToString(issue.ID), IssueIdentifier: service.IssueIdentifier(prefix, issue.Number), IssueTitle: issue.Title, CommentID: util.UUIDToString(result.Comment.ID), TaskID: util.UUIDToString(task.ID)}
		} else {
			result, err := issueBackend.CreateInTx(ctx, tx, service.EmployeeIssueCreateParams{Intent: employeetask.CreateParams{Scope: employeetask.Scope{WorkspaceID: util.UUIDToString(session.WorkspaceID), AgentID: util.UUIDToString(session.AgentID), Kind: employeetask.ScopeLegacyChat, LegacyID: util.UUIDToString(session.ID)}, OwnerLoop: employeetask.LoopCoordinator, DispatchMode: employeetask.DispatchIssue, RequesterRef: "member:" + util.UUIDToString(userID), Definition: employeetask.Definition{Goal: inboundcoord.IssueTitle(itemDecision, itemContent)}, Input: itemContent, Source: employeetask.Source{Namespace: "coordinator_web", Key: trace.TraceID + ":" + item.ActionKey}}, Issue: service.IssueCreateParams{
				WorkspaceID: session.WorkspaceID, Title: inboundcoord.IssueTitle(itemDecision, itemContent),
				Description: pgtype.Text{String: inboundcoord.IssueDescription(itemDecision, itemContent), Valid: true},
				Status:      "todo", Priority: "none", AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: session.AgentID,
				CreatorType: "member", CreatorID: userID, AllowDuplicate: true, DispatchContext: raw,
			}, Options: service.IssueCreateOpts{ActorID: util.UUIDToString(userID), AnalyticsAgentID: util.UUIDToString(session.AgentID), Platform: "web"}})
			if err != nil {
				return nil, decision, err
			}
			if result.EnqueuedTask == nil || !result.EnqueuedTask.ID.Valid {
				return nil, decision, errors.New("coordinator issue did not enqueue its assigned work")
			}
			task = *result.EnqueuedTask
			outcome = protocol.ChatCoordinatorIssueResult{Action: "issue_created", IssueID: util.UUIDToString(result.Issue.ID), IssueIdentifier: service.IssueIdentifier(prefix, result.Issue.Number), IssueTitle: result.Issue.Title, TaskID: util.UUIDToString(task.ID)}
		}
		tasks = append(tasks, task)
		committed.IssueResults = append(committed.IssueResults, outcome)
		committed.CompletedActionKeys = append(committed.CompletedActionKeys, item.ActionKey)
	}
	turn, err := txTasks.PersistCoordinatorChatTurn(ctx, session, content, committed.UserText, committed.ElapsedMs, committed.TraceJSON())
	if err != nil {
		return nil, decision, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, decision, fmt.Errorf("commit coordinator chat plan: %w", err)
	}
	for _, event := range bufferedEvents {
		if h.Bus != nil {
			h.Bus.Publish(event)
		}
	}
	for _, event := range analyticsBuffer.events {
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, event)
	}
	for _, task := range tasks {
		if task.Status == "queued" {
			h.TaskService.NotifySteerPredecessor(ctx, task)
			h.TaskService.NotifyTaskEnqueued(ctx, task)
		}
	}
	inboundcoord.RecordDecision(ctx, committed)
	return turn, committed, nil
}
