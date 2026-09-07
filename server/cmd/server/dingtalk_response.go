package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newDingTalkResponsePolicyWorker(queries *db.Queries, client *agentmessagerouter.Client, cfg *appRuntimeConfig) *agentmessagerouter.ResponsePolicySyncWorker {
	enabled := func() bool {
		value, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("MULTICA_DINGTALK_RESPONSE_POLICY_ENABLED")))
		return value
	}
	revision := func() int64 {
		value, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv("MULTICA_DINGTALK_RESPONSE_POLICY_REVISION")), 10, 64)
		return max(value, 1)
	}
	if cfg != nil {
		enabled = cfg.dingtalkResponsePolicyEnabled
		revision = cfg.dingtalkResponsePolicyRevision
	}
	return agentmessagerouter.NewResponsePolicySyncWorker(queries, client, agentmessagerouter.ResponsePolicySyncConfig{
		Enabled: enabled, RolloutRevision: revision,
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
