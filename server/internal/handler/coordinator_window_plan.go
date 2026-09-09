package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// The checkpoint is read only from a claimed persisted job. DispatchCommand
// deliberately has no wire field for it, so callers cannot supply a plan.
func coordinatorJobCheckpoint(raw []byte) (*inboundcoord.Decision, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope["_coordinator_plan"]) == 0 {
		return nil, nil
	}
	var plan inboundcoord.Decision
	if err := json.Unmarshal(envelope["_coordinator_plan"], &plan); err != nil {
		return nil, err
	}
	if plan.PlanVersion != "window-plan-v1" {
		return nil, fmt.Errorf("unsupported coordinator plan version")
	}
	return &plan, nil
}

func (h *Handler) coordinatorCheckpointContext(ctx context.Context, job db.InboundCoordinatorJob) (context.Context, error) {
	plan, err := coordinatorJobCheckpoint(job.Command)
	if err != nil {
		return ctx, err
	}
	ctx = inboundcoord.ContextWithHistoryBefore(ctx, job.CreatedAt.Time)
	return inboundcoord.ContextWithPlanCheckpoint(ctx, plan, func(d inboundcoord.Decision) error {
		if d.PlanVersion != "window-plan-v1" {
			return nil
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		n, err := h.Queries.SaveCoordinatorWindowPlan(ctx, db.SaveCoordinatorWindowPlanParams{Plan: raw, ID: job.ID, LeaseToken: job.LeaseToken})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("coordinator plan lease lost")
		}
		return nil
	}), nil
}

func planItemCompleted(d inboundcoord.Decision, key string) bool {
	for _, k := range d.CompletedActionKeys {
		if k == key {
			return true
		}
	}
	return false
}

func recordPlanItem(ctx context.Context, d *inboundcoord.Decision, item inboundcoord.WindowItem, result protocol.ChatCoordinatorIssueResult) error {
	if !planItemCompleted(*d, item.ActionKey) {
		d.CompletedActionKeys = append(d.CompletedActionKeys, item.ActionKey)
		d.IssueResults = append(d.IssueResults, result)
	}
	inboundcoord.RecordDecision(ctx, *d)
	return inboundcoord.SavePlan(ctx, *d)
}

func (h *Handler) materializeWindowContinuation(ctx context.Context, c DispatchCommand, dc agentDispatchContext, item inboundcoord.WindowItem, key string, decision inboundcoord.Decision) (db.Issue, db.AgentTaskQueue, protocol.ChatCoordinatorIssueResult, error) {
	id, err := util.ParseUUID(item.IssueID)
	if err != nil {
		return db.Issue{}, db.AgentTaskQueue{}, protocol.ChatCoordinatorIssueResult{}, err
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: dc.WorkspaceID})
	if err != nil {
		return issue, db.AgentTaskQueue{}, protocol.ChatCoordinatorIssueResult{}, err
	}
	if !issue.AssigneeID.Valid || issue.AssigneeID != dc.AgentID || issue.AssigneeType.String != "agent" {
		return issue, db.AgentTaskQueue{}, protocol.ChatCoordinatorIssueResult{}, fmt.Errorf("continuation target is not owned by this agent")
	}
	raw, err := inboundcoord.IndependentIssueTaskContext(dispatchRuntimeContext(c, key), inboundcoord.CoordinatorIssueTriggerComment)
	if err != nil {
		return issue, db.AgentTaskQueue{}, protocol.ChatCoordinatorIssueResult{}, err
	}
	raw = inboundcoord.StampCoordinatorTrace(raw, decision, "dingtalk", time.Now())
	if h.IssueCommentService == nil {
		return issue, db.AgentTaskQueue{}, protocol.ChatCoordinatorIssueResult{}, fmt.Errorf("comment service unavailable")
	}
	params := service.IssueCommentCreateParams{Issue: issue, AuthorID: dc.UserID, Content: inboundcoord.ContinuationContent(item), AgentIdentityContextToken: c.ExternalIdentity.ContextToken, DispatchContext: raw, IdempotencyKey: key}
	var result service.IssueCommentCreateResult
	if c.ProactiveConversation {
		result, err = h.IssueCommentService.QueueCoordinatorFollowUp(ctx, params)
	} else {
		result, err = h.IssueCommentService.CreateExternalFollowUp(ctx, params, service.IssueCommentCreateOpts{})
	}
	if err != nil {
		return issue, result.Task, protocol.ChatCoordinatorIssueResult{}, err
	}
	action := "issue_commented"
	if !result.Task.ID.Valid {
		action = "issue_follow_up_queued"
	}
	return issue, result.Task, protocol.ChatCoordinatorIssueResult{Action: action, IssueID: util.UUIDToString(issue.ID), IssueIdentifier: service.IssueIdentifier(h.getIssuePrefix(ctx, dc.WorkspaceID), issue.Number), IssueTitle: issue.Title, CommentID: util.UUIDToString(result.Comment.ID), TaskID: util.UUIDToString(result.Task.ID)}, nil
}

func windowItemCommand(command DispatchCommand, item inboundcoord.WindowItem) DispatchCommand {
	allowed := map[string]bool{}
	for _, ref := range item.SourceRefs {
		allowed[ref] = true
	}
	var selected []DispatchMessage
	index := 0
	for _, message := range command.Event.Data.Messages {
		if message.Reaction != nil || strings.TrimSpace(message.Text) == "" {
			continue
		}
		index++
		if allowed[fmt.Sprintf("u%d", index)] {
			selected = append(selected, message)
		}
	}
	if len(selected) > 0 {
		command.Event.Data.Messages = selected
	}
	command = overlayDispatchSender(command, item.Delegator)
	return command
}

// A stable per-item key survives retries even after earlier items committed.
func windowItemKey(base string, item inboundcoord.WindowItem, index int) string {
	if item.ActionKey != "" {
		return base + ":coordinator:" + item.ActionKey
	}
	if index == 0 {
		return base
	}
	return fmt.Sprintf("%s:window-item:%d", base, index)
}
