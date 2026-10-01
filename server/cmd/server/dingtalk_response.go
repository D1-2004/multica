package main

import (
	"context"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newDingTalkResponsePolicyWorker(queries *db.Queries, client *agentmessagerouter.Client) *agentmessagerouter.ResponsePolicySyncWorker {
	return agentmessagerouter.NewResponsePolicySyncWorker(queries, client, agentmessagerouter.ResponsePolicySyncConfig{
		RuntimeSupportsPolicy: func(ctx context.Context, agent db.Agent) (bool, error) {
			if !agent.RuntimeID.Valid {
				return false, nil
			}
			runtime, err := queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: agent.RuntimeID, WorkspaceID: agent.WorkspaceID})
			if err != nil {
				return false, err
			}
			return service.RuntimeSupportsDWSMessagePolicy(runtime), nil
		},
	})
}
