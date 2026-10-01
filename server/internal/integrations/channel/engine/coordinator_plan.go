package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The first committed task retains the entire window receipt. This closes the
// interval between accepting work and AppendMessage marking ingress complete.
// The key identifies ingress, not model output, so model retries cannot change
// or duplicate work accepted before a lost response or process crash.
func channelCoordinatorWindowKey(inst ResolvedInstallation, msg channel.InboundMessage) (string, error) {
	if strings.TrimSpace(msg.MessageID) == "" {
		return "", errors.New("coordinator work plan requires a stable message id")
	}
	raw, _ := json.Marshal([]string{uuidString(inst.ID), string(msg.Source.ChannelType), msg.Source.ChatID, msg.MessageID})
	return fmt.Sprintf("channel-coordinator-v1:%x", sha256.Sum256(raw)), nil
}
func readChannelCoordinatorPlan(ctx context.Context, tx pgx.Tx, inst ResolvedInstallation, key string) (inboundcoord.Decision, bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT t.context->'channel_coordinator_plan' FROM agent_task_queue t JOIN issue i ON i.id=t.issue_id WHERE t.agent_id=$1 AND i.workspace_id=$2 AND t.context->>'channel_coordinator_window_key'=$3 ORDER BY t.created_at LIMIT 1`, inst.AgentID, inst.WorkspaceID, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return inboundcoord.Decision{}, false, nil
	}
	if err != nil {
		return inboundcoord.Decision{}, false, fmt.Errorf("read native coordinator receipt: %w", err)
	}
	var d inboundcoord.Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, false, fmt.Errorf("decode native coordinator receipt: %w", err)
	}
	if d.PlanVersion != "window-plan-v1" || d.Action != inboundcoord.ActionIssue || len(d.Items) == 0 || len(d.CompletedActionKeys) != len(d.Items) || len(d.IssueResults) != len(d.Items) {
		return d, false, errors.New("native coordinator receipt is incomplete")
	}
	return d, true, nil
}
func (r *Router) coordinatorTaskService() *service.TaskService {
	if r.issueComments != nil && r.issueComments.TaskService != nil {
		return r.issueComments.TaskService
	}
	tasks, _ := r.tasks.(*service.TaskService)
	return tasks
}
func (r *Router) restoreChannelCoordinatorPlan(ctx context.Context, inst ResolvedInstallation, msg channel.InboundMessage) (inboundcoord.Decision, bool, error) {
	tasks := r.coordinatorTaskService()
	// Read-only decisions need no transactional services. A work plan requires
	// them and fails closed in materializeChannelCoordinatorPlan.
	if tasks == nil || tasks.TxStarter == nil {
		return inboundcoord.Decision{}, false, nil
	}
	key, err := channelCoordinatorWindowKey(inst, msg)
	if err != nil {
		return inboundcoord.Decision{}, false, nil
	}
	tx, err := tasks.TxStarter.Begin(ctx)
	if err != nil {
		return inboundcoord.Decision{}, false, err
	}
	defer tx.Rollback(ctx)
	d, found, err := readChannelCoordinatorPlan(ctx, tx, inst, key)
	if err != nil {
		return d, false, err
	}
	return d, found, tx.Commit(ctx)
}

// Every item and the complete receipt share one outer transaction. Normal
// services use its savepoints; their events are buffered and runtime wakeups
// are disabled until the outer commit succeeds.
func (r *Router) materializeChannelCoordinatorPlan(ctx context.Context, inst ResolvedInstallation, identity ResolvedIdentity, originType string, msg channel.InboundMessage, taskContext []byte, d *inboundcoord.Decision) error {
	if d.PlanVersion != "window-plan-v1" || len(d.Items) == 0 || len(d.Items) > inboundcoord.WindowPlanMaxItems {
		return errors.New("coordinator returned an invalid work plan")
	}
	key, err := channelCoordinatorWindowKey(inst, msg)
	if err != nil {
		return err
	}
	tasks := r.coordinatorTaskService()
	issues, ok := r.issues.(*service.IssueService)
	if !ok || issues == nil || tasks == nil || tasks.Queries == nil || tasks.TxStarter == nil {
		return errors.New("native coordinator requires transactional issue and task services")
	}
	tx, err := tasks.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return fmt.Errorf("lock native coordinator window: %w", err)
	}
	if saved, found, err := readChannelCoordinatorPlan(ctx, tx, inst, key); err != nil {
		return err
	} else if found {
		*d = saved
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		r.notifyChannelCoordinatorTasks(ctx, tasks, *d)
		inboundcoord.RecordDecision(ctx, *d)
		return inboundcoord.SavePlan(ctx, *d)
	}
	if len(d.CompletedActionKeys) != 0 || len(d.IssueResults) != 0 {
		return errors.New("native coordinator completed plan has no durable receipt")
	}
	qtx := tasks.Queries.WithTx(tx)
	buffer := events.New()
	var queuedEvents []events.Event
	buffer.SubscribeAll(func(e events.Event) { queuedEvents = append(queuedEvents, e) })
	txTasks := &service.TaskService{Queries: qtx, TxStarter: tx, Bus: buffer, FeatureFlags: tasks.FeatureFlags, Composio: tasks.Composio}
	txIssues := service.NewIssueService(qtx, tx, buffer, nil, txTasks)
	txComments := service.NewIssueCommentService(qtx, buffer, txTasks)
	prefix := r.issuePrefix(ctx, inst.WorkspaceID)
	work := *d
	work.CompletedActionKeys = nil
	work.IssueResults = nil
	seen := map[string]bool{}
	var materialized []db.Issue
	var origins []string
	for _, item := range work.Items {
		if strings.TrimSpace(item.ActionKey) == "" || seen[item.ActionKey] {
			return errors.New("native coordinator item has missing or repeated action key")
		}
		seen[item.ActionKey] = true
		itemKey := key + ":" + item.ActionKey
		trigger := inboundcoord.CoordinatorIssueTriggerCreate
		if item.IssueID != "" {
			trigger = inboundcoord.CoordinatorIssueTriggerComment
		}
		itemContext, err := coordinatorItemTaskContext(taskContext, msg, item)
		if err != nil {
			return err
		}
		origin := coordinatorItemOriginOpenMsgID(itemContext, msg.MessageID)
		raw, err := inboundcoord.IndependentIssueTaskContext(itemContext, trigger)
		if err != nil {
			return err
		}
		raw = inboundcoord.StampCoordinatorTrace(raw, work, string(msg.Source.ChannelType), time.Now())
		token, err := taskIdentityContextToken(raw)
		if err != nil {
			return err
		}
		var issue db.Issue
		var task db.AgentTaskQueue
		var commentID pgtype.UUID
		action := "issue_created"
		if item.IssueID != "" {
			id, err := util.ParseUUID(item.IssueID)
			if err != nil {
				return err
			}
			issue, err = qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: inst.WorkspaceID})
			if err != nil {
				return err
			}
			if issue.AssigneeType.String != "agent" || !issue.AssigneeType.Valid || issue.AssigneeID != inst.AgentID {
				return errors.New("native coordinator continuation target belongs to another agent")
			}
			if strings.TrimSpace(item.Content) == "" {
				return errors.New("native coordinator continuation has no content")
			}
			result, err := txComments.CreateExternalFollowUp(ctx, service.IssueCommentCreateParams{Issue: issue, AuthorID: identity.PrincipalUserID, Content: inboundcoord.ContinuationContent(item), AgentIdentityContextToken: token, DispatchContext: raw, IdempotencyKey: itemKey}, service.IssueCommentCreateOpts{})
			if err != nil {
				return err
			}
			task, commentID, action = result.Task, result.Comment.ID, "issue_commented"
		} else {
			itemDecision := inboundcoord.Decision{Purpose: item.Purpose, Intent: item.Intent, LookInto: item.LookInto, UserText: work.UserText}
			source := item.Content
			if strings.TrimSpace(source) == "" {
				source = msg.Text
			}
			source += "\n\n本任务只处理这一项交付：" + item.Purpose
			result, err := txIssues.Create(ctx, service.IssueCreateParams{
				WorkspaceID: inst.WorkspaceID, Title: inboundcoord.IssueTitle(itemDecision, msg.Text), Description: pgtype.Text{String: inboundcoord.IssueDescription(itemDecision, source), Valid: true},
				Status: "todo", Priority: "none", AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: inst.AgentID, CreatorType: "member", CreatorID: identity.PrincipalUserID,
				OriginType: pgtype.Text{String: originType, Valid: originType != ""}, OriginID: durableIssueCommandOriginID(inst.ID, itemKey), AllowDuplicate: true, AgentIdentityContextToken: token, DispatchContext: raw,
				Metadata: dingTalkOriginMetadata(origin),
			}, service.IssueCreateOpts{BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
				return map[string]any{"issue": service.IssueToMap(issue, prefix)}
			}})
			if err != nil {
				return err
			}
			issue = result.Issue
			if result.EnqueuedTask != nil {
				task = *result.EnqueuedTask
			}
		}
		if !issue.ID.Valid || !task.ID.Valid {
			return errors.New("native coordinator item did not enqueue a durable task")
		}
		materialized = append(materialized, issue)
		origins = append(origins, origin)
		work.CompletedActionKeys = append(work.CompletedActionKeys, item.ActionKey)
		work.IssueResults = append(work.IssueResults, protocol.ChatCoordinatorIssueResult{Action: action, IssueID: uuidString(issue.ID), IssueIdentifier: service.IssueIdentifier(prefix, issue.Number), IssueTitle: issue.Title, CommentID: uuidString(commentID), TaskID: uuidString(task.ID)})
	}
	// The stored receipt is readable beyond the reply that delivers a
	// capability answer's configuration link, so it keeps a placeholder; a
	// redelivery of this window answers without the link.
	encoded, err := json.Marshal(work.WithoutConfigLinks())
	if err != nil {
		return err
	}
	firstTaskID, err := util.ParseUUID(work.IssueResults[0].TaskID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET context=COALESCE(context,'{}'::jsonb)||jsonb_build_object('channel_coordinator_window_key',$2::text,'channel_coordinator_plan',$3::jsonb) WHERE id=$1`, firstTaskID, key, encoded); err != nil {
		return fmt.Errorf("save native coordinator receipt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit native coordinator window: %w", err)
	}
	*d = work
	bus := r.bus
	if bus == nil {
		bus = issues.Bus
	}
	if bus != nil {
		for _, event := range queuedEvents {
			bus.Publish(event)
		}
	}
	r.notifyChannelCoordinatorTasks(ctx, tasks, work)
	for i, issue := range materialized {
		taskID, _ := util.ParseUUID(work.IssueResults[i].TaskID)
		itemMsg := msg
		if i < len(origins) && origins[i] != "" {
			itemMsg.MessageID = origins[i]
		}
		r.associateIssueConversation(ctx, inst, itemMsg, issue, taskID)
	}
	inboundcoord.RecordDecision(ctx, work)
	return inboundcoord.SavePlan(ctx, work)
}
func (r *Router) notifyChannelCoordinatorTasks(ctx context.Context, tasks *service.TaskService, d inboundcoord.Decision) {
	for _, result := range d.IssueResults {
		id, err := util.ParseUUID(result.TaskID)
		if err != nil {
			continue
		}
		task, err := tasks.Queries.GetAgentTask(ctx, id)
		if err == nil && task.Status == "queued" {
			tasks.NotifyTaskEnqueued(ctx, task)
		}
	}
}
func applyChannelCoordinatorResult(res *Result, d inboundcoord.Decision) error {
	if len(d.IssueResults) == 0 || len(d.IssueResults) != len(d.Items) {
		return errors.New("native coordinator outcome is incomplete")
	}
	first := d.IssueResults[0]
	issueID, err := util.ParseUUID(first.IssueID)
	if err != nil {
		return err
	}
	taskID, err := util.ParseUUID(first.TaskID)
	if err != nil {
		return err
	}
	res.Outcome = OutcomeIngested
	res.CoordinatorIssue = true
	res.IssueID, res.TaskID = issueID, taskID
	res.IssueIdentifier, res.IssueTitle = first.IssueIdentifier, first.IssueTitle
	res.ReplyText = d.UserText
	res.runScheduled = false
	return nil
}
