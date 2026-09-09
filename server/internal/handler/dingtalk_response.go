package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var routerResponseReceiptPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/response-receipt$`)

func managedDingTalkResponse(c DispatchCommand) bool {
	return c.ResponsePolicy.Managed() && c.Source.Type == "digital_employee" &&
		c.Event.Domain == "channel" && c.Event.Type == "message.created" &&
		c.Outbound.Mode == protocol.DispatchOutboundModeDWS &&
		(c.Control == nil || c.Control.Action != "cancel")
}

func sameDingTalkResponsePolicy(a, b *protocol.DingTalkResponsePolicy) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func dingTalkPolicyInstruction(text string, policy *protocol.DingTalkResponsePolicy) string {
	if !policy.Managed() {
		return text
	}
	text = strings.ReplaceAll(text, "as the current-user identity with `--ai-tag=false`.", "as the current-user identity. The managed DWS wrapper applies the employee's AI badge setting.")
	text = strings.ReplaceAll(text, "Current-user sends must pass `--ai-tag=false`. After a user-visible reply, remove 处理中 / 已完成 / 思考中 / 🤔思考中 from the inbound target message; never add 已完成.", "The managed DWS wrapper applies the employee's AI badge setting and records send receipts. The platform owns read receipts and lifecycle reactions; do not add or remove them yourself.")
	text = strings.ReplaceAll(text, "After a successful send — or when you send nothing because the scene was already told — remove processing/complete emotions (处理中, 已完成, 思考中, 🤔思考中) from the inbound target message; never add 已完成. ", "The platform owns lifecycle reactions and their cleanup; do not add or remove them yourself. ")
	return text
}

func (h *Handler) registerDingTalkResponseRoute(ctx context.Context, tx db.DBTX, c DispatchCommand, scope agentDispatchContext) error {
	if !managedDingTalkResponse(c) {
		return nil
	}
	if h.DingTalkResponses == nil || c.CompletionCallback == nil || c.ExternalIdentity.DWS == nil {
		return errors.New("managed DingTalk response service is unavailable")
	}
	sender := c.Event.Data.Sender.OpenDingTalkID
	if sender == "" {
		sender = c.Event.Data.Sender.SenderOpenDingTalkID
	}
	in := dingtalkresponse.ActionInput{
		WorkspaceID: uuidToString(scope.WorkspaceID), AgentID: uuidToString(scope.AgentID),
		DWSUID: c.ExternalIdentity.DWS.UID, DWSOrgID: c.ExternalIdentity.DWS.OrgID,
		ConversationID:       c.Event.Data.Conversation.OpenConversationID,
		SenderOpenDingTalkID: sender,
		IsGroup:              strings.EqualFold(c.Event.Data.Conversation.Type, "group"),
		ShowAITag:            c.ResponsePolicy.ShowAITag,
		CallbackURL:          c.CompletionCallback.ResponseURL, CallbackTarget: c.CompletionCallback.Target,
	}
	// Register both supplied callback paths; never reconstruct one from another.
	for _, callback := range []string{c.CompletionCallback.URL, c.CompletionCallback.UpdateURL} {
		if callback == "" {
			continue
		}
		if err := h.DingTalkResponses.RegisterRoute(ctx, tx, dingtalkresponse.Route{CallbackURL: callback, Input: in}); err != nil {
			return err
		}
	}
	return nil
}

// PrepareExecutionResult preserves the execution callback and independently
// persists its user-visible response before allowing that callback to drain.
func (h *Handler) PrepareExecutionResult(ctx context.Context, callback string, result agentmessagerouter.ExecutionResultRequest) (bool, error) {
	if h == nil || h.DingTalkResponses == nil {
		return false, nil
	}
	route, err := h.DingTalkResponses.FindRoute(ctx, callback)
	if err != nil || route == nil {
		return false, err
	}
	if route.Input.AgentID != result.AgentID {
		return true, errors.New("response callback agent does not match frozen route")
	}
	in := responseInputForResult(route.Input, result)
	in = h.fillDingTalkResponseOrigin(ctx, in)
	if in.Text != "" && in.TaskID != "" {
		state, checkErr := h.DingTalkResponses.SandboxResponseState(ctx, in.WorkspaceID, in.AgentID, "", in.TaskID, in.ConversationID)
		if checkErr != nil {
			return true, checkErr
		}
		switch state {
		case "pending", "provider_accepted":
			return true, errors.New("sandbox send receipt is still being verified")
		case "unknown":
			in.Text, in.CloseState = "", "unknown"
		case "delivered":
			in.Text, in.CloseState = "", "silent"
		}
	}
	_, err = h.DingTalkResponses.Submit(ctx, in)
	return true, err
}

func responseInputForResult(in dingtalkresponse.ActionInput, result agentmessagerouter.ExecutionResultRequest) dingtalkresponse.ActionInput {
	in.RequestID = result.RequestID
	in.TaskID = result.ExternalRunID
	if in.TaskID == "" {
		in.TaskID = result.ExternalTaskID
	}
	switch result.ExecutionStatus {
	case "cancelled", "canceled":
		in.CloseState = "cancelled"
	case "failed":
		in.CloseState = "failed"
	default:
		in.Text = stripReplyDecisionLeak(result.ResultMessage)
		if (result.ShouldReply != nil && !*result.ShouldReply) || in.Text == "" {
			in.Text = ""
			in.CloseState = "silent"
		}
	}
	return in
}

func (h *Handler) PrepareExecutionUpdate(ctx context.Context, callback string, update agentmessagerouter.ExecutionUpdateRequest) (bool, error) {
	if h == nil || h.DingTalkResponses == nil {
		return false, nil
	}
	route, err := h.DingTalkResponses.FindRoute(ctx, callback)
	if err != nil || route == nil {
		return false, err
	}
	if route.Input.AgentID != update.AgentID {
		return true, errors.New("response update agent does not match frozen route")
	}
	if update.UpdateType != "delegated_to_issue" || strings.TrimSpace(update.ResultMessage) == "" {
		return true, nil
	}
	in := route.Input
	in.RequestID = update.RequestID
	in.TaskID = update.ExternalTaskID
	in.IssueID = update.Extension.IssueID
	in.Text = stripReplyDecisionLeak(update.ResultMessage)
	if in.Text == "" {
		in.CloseState = "silent"
	}
	in = h.fillDingTalkResponseOrigin(ctx, in)
	_, err = h.DingTalkResponses.Submit(ctx, in)
	return true, err
}

func (h *Handler) fillDingTalkResponseOrigin(ctx context.Context, in dingtalkresponse.ActionInput) dingtalkresponse.ActionInput {
	if h == nil || h.Queries == nil {
		return in
	}
	var task db.AgentTaskQueue
	var issue db.Issue
	if taskID, err := util.ParseUUID(in.TaskID); err == nil {
		if loaded, loadErr := h.Queries.GetAgentTask(ctx, taskID); loadErr == nil {
			task = loaded
			if !task.IssueID.Valid && in.IssueID != "" {
				if issueID, parseErr := util.ParseUUID(in.IssueID); parseErr == nil {
					task.IssueID = issueID
				}
			}
		}
	}
	issueID := in.IssueID
	if issueID == "" && task.IssueID.Valid {
		issueID = uuidToString(task.IssueID)
	}
	if parsedIssue, err := util.ParseUUID(issueID); err == nil {
		workspaceID, wsErr := util.ParseUUID(in.WorkspaceID)
		if wsErr != nil && task.ID.Valid {
			if agent, agentErr := h.Queries.GetAgent(ctx, task.AgentID); agentErr == nil {
				workspaceID = agent.WorkspaceID
			}
		}
		if workspaceID.Valid {
			if loaded, loadErr := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: parsedIssue, WorkspaceID: workspaceID}); loadErr == nil {
				issue = loaded
			}
		}
	}
	return fillDingTalkOriginReply(in, task, issue)
}

// RouterResponseReceiptSender pins pending receipts to their original Router.
type RouterResponseReceiptSender struct {
	Client  *agentmessagerouter.Client
	Handler *Handler
}

func (s RouterResponseReceiptSender) SendResponseReceipt(ctx context.Context, callback, target string, receipt protocol.DingTalkResponseReceipt) error {
	if s.Client == nil || s.Client.TargetIdentity() != target {
		return errors.New("response receipt Router target changed")
	}
	if err := s.Client.SubmitResponseReceipt(ctx, callback, receipt); err != nil {
		return err
	}
	if s.Handler == nil {
		return nil
	}
	return s.Handler.closeDeliveredCoordinatorWindow(ctx, callback, target, receipt)
}

func (h *Handler) closeDeliveredCoordinatorWindow(ctx context.Context, callback, target string, receipt protocol.DingTalkResponseReceipt) error {
	agentID, err := util.ParseUUID(receipt.AgentID)
	if err != nil {
		return err
	}
	agent, err := h.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	raw, err := h.Queries.GetCoordinatorResponseCommand(ctx, db.GetCoordinatorResponseCommandParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, ResponseCallbackUrl: callback,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var command DispatchCommand
	if err := json.Unmarshal(raw, &command); err != nil {
		return err
	}
	for _, cb := range command.ExtraCompletionCallbacks {
		if !routerCompletionCallbackPattern.MatchString(cb.URL) {
			return errors.New("invalid collected completion callback")
		}
		if err := h.TaskService.EnqueueSynchronousSilence(ctx, cb.URL, target, agentID); err != nil {
			return err
		}
	}
	return nil
}

// BindVerifiedDingTalkSend records the actual outbound scene after provider
// confirmation, including DM targets that the wrapper could not resolve.
func (h *Handler) BindVerifiedDingTalkSend(ctx context.Context, in dingtalkresponse.ActionInput, cid, messageID string) error {
	if h.Assoc == nil {
		return errors.New("association store is unavailable")
	}
	title := ""
	if in.IssueID != "" {
		issueID, err := util.ParseUUID(in.IssueID)
		if err != nil {
			return err
		}
		workspaceID, err := util.ParseUUID(in.WorkspaceID)
		if err != nil {
			return err
		}
		issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
		if err != nil {
			return err
		}
		title = issue.Title
	}
	_, err := h.Assoc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID: in.WorkspaceID, AgentID: in.AgentID, IssueID: in.IssueID, IssueTitle: title,
		RunID: in.TaskID, ConversationID: cid, EvidenceID: messageID,
	})
	return err
}
