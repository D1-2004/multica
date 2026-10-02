package handler

import (
	"context"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

// The provider sender is the requester; the Multica comment operator remains
// separate and never becomes the human delegator merely because it owns a token.
func coordinatorTaskActor(command DispatchCommand) string {
	prefix := strings.TrimSpace(command.Source.Platform)
	if prefix == "" {
		prefix = "external"
	}
	sender := command.Event.Data.Sender
	if id := strings.TrimSpace(sender.UID); id != "" {
		return prefix + ":uid:" + id
	}
	if id := firstNonEmpty(sender.OpenDingTalkID, sender.SenderOpenDingTalkID); id != "" {
		return prefix + ":open_id:" + id
	}
	if id := strings.TrimSpace(sender.StaffID); id != "" {
		return prefix + ":staff_id:" + id
	}
	return "dispatch_sender:unresolved"
}

func (h *Handler) createCoordinatorIssue(ctx context.Context, command DispatchCommand, dc agentDispatchContext, key string, issue service.IssueCreateParams, options service.IssueCreateOpts) (service.IssueCreateResult, error) {
	scope := employeetask.Scope{WorkspaceID: util.UUIDToString(dc.WorkspaceID), AgentID: util.UUIDToString(dc.AgentID), Kind: employeetask.ScopeLegacyIssue}
	if command.AgentScene != nil {
		registered, err := dispatchScene(ctx, h.Queries, command, dc)
		if err != nil {
			return service.IssueCreateResult{}, err
		}
		scope.Kind = employeetask.ScopeScene
		scope.Scene = scene.RefOf(registered)
		scope.TenantOrgID = registered.TenantOrgID
	}
	intent := employeetask.CreateParams{Scope: scope, OwnerLoop: employeetask.LoopCoordinator, DispatchMode: employeetask.DispatchIssue, RequesterRef: coordinatorTaskActor(command), Definition: employeetask.Definition{Goal: issue.Title}, Input: issue.Description.String, Source: employeetask.Source{Namespace: "coordinator_dispatch:" + dispatchIdempotencyEndpointID(command, dc), Key: key}}
	return service.NewEmployeeIssueBackend(h.IssueService, h.IssueCommentService).Create(ctx, service.EmployeeIssueCreateParams{Intent: intent, Issue: issue, Options: options})
}
