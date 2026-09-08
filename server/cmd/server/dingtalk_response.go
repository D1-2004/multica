package main

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
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
			if service.IsCloudSandboxRuntime(runtime) {
				return service.CloudSandboxRuntimeHasCapability(runtime, protocol.DWSMessagePolicyCapability), nil
			}
			var metadata struct {
				ClientCapabilities []string `json:"client_capabilities"`
			}
			if json.Unmarshal(runtime.Metadata, &metadata) != nil {
				return false, nil
			}
			return slices.Contains(metadata.ClientCapabilities, protocol.DWSMessagePolicyCapability), nil
		},
	})
}
