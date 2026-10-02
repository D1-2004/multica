package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

var errDingTalkMessagePolicyCapability = errors.New("managed DingTalk response requires a daemon and runtime supporting dws_message_policy_v1")

// Capabilities used by host admission are stored from the authenticated
// transport header, never from a user-supplied metadata body.
func localDingTalkClientCapabilities(header string) []string {
	advertised := map[string]bool{}
	for _, candidate := range strings.Split(header, ",") {
		advertised[strings.TrimSpace(candidate)] = true
	}
	capabilities := []string{}
	for _, capability := range []string{protocol.DWSMessagePolicyCapability, protocol.DaemonCapabilityEmployeeDirectV1} {
		if advertised[capability] {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

func (h *Handler) persistLocalDingTalkClientCapabilities(ctx context.Context, runtime db.AgentRuntime, header string) error {
	if runtime.RuntimeMode != "local" {
		return nil
	}
	capabilities, _ := json.Marshal(localDingTalkClientCapabilities(header))
	var metadata map[string]json.RawMessage
	if json.Unmarshal(runtime.Metadata, &metadata) == nil && bytes.Equal(bytes.TrimSpace(metadata["client_capabilities"]), capabilities) {
		return nil
	}
	return h.Queries.UpdateLocalRuntimeDingTalkCapabilities(ctx, db.UpdateLocalRuntimeDingTalkCapabilitiesParams{
		ID:           runtime.ID,
		Capabilities: capabilities,
	})
}

type dingTalkTaskPolicyReader interface {
	GetAgentDingTalkResponsePolicy(context.Context, pgtype.UUID) (db.GetAgentDingTalkResponsePolicyRow, error)
	GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
}

func dingTalkTaskPolicyCapable(r *http.Request, runtime db.AgentRuntime) bool {
	return requestHasDaemonCapability(r, protocol.DWSMessagePolicyCapability) &&
		(runtime.RuntimeMode != "cloud" || service.CloudSandboxRuntimeHasCapability(runtime, protocol.DWSMessagePolicyCapability))
}

// resolveDingTalkTaskPolicy reads only trusted task context and agent bindings.
// It does not mint credentials, use custom_env, or consult the task's prompt.
func resolveDingTalkTaskPolicy(ctx context.Context, reader dingTalkTaskPolicyReader, task db.AgentTaskQueue, runtime db.AgentRuntime, capable bool) (*protocol.DingTalkMessagePolicy, error) {
	if service.IsA2ATaskOrigin(task.Context) {
		return nil, nil
	}
	var stored persistedDispatchContext
	if len(bytes.TrimSpace(task.Context)) > 0 {
		if err := json.Unmarshal(task.Context, &stored); err != nil {
			return nil, errors.New("invalid task context for DingTalk message policy")
		}
	}
	if stored.ResponsePolicy != nil && !stored.ResponsePolicy.Valid() {
		return nil, errors.New("invalid frozen DingTalk response policy")
	}
	managed := managedDingTalkResponse(DispatchCommand{
		Source:         stored.Source,
		Event:          DispatchEvent{Domain: stored.Domain, Type: stored.Type},
		Outbound:       stored.Outbound,
		Control:        stored.Control,
		ResponsePolicy: stored.ResponsePolicy,
	})
	if !capable {
		if managed {
			return nil, errDingTalkMessagePolicyCapability
		}
		return nil, nil
	}
	bound := false
	if stored.ExternalIdentity != nil && stored.ExternalIdentity.DWS != nil {
		identity := stored.ExternalIdentity.DWS
		if !validDispatchDWSIdentifier(identity.UID) || !validDispatchDWSIdentifier(identity.OrgID) {
			return nil, errors.New("invalid external DingTalk identity for message policy")
		}
		bound = true
	} else {
		identity, err := reader.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
			AgentID:     task.AgentID,
			WorkspaceID: runtime.WorkspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load DingTalk identity for message policy: %w", err)
		}
		bound = err == nil && validDispatchDWSIdentifier(identity.DwsUid) && validDispatchDWSIdentifier(identity.OrgID)
	}
	if !bound {
		if managed {
			return nil, errors.New("managed DingTalk response has no valid bound DWS identity")
		}
		return nil, nil
	}
	policy := &protocol.DingTalkMessagePolicy{PlatformManagedLifecycle: managed}
	if stored.ResponsePolicy != nil {
		policy.ShowAITag = stored.ResponsePolicy.ShowAITag
		applyDingTalkOriginReply(policy, stored)
		return policy, nil
	}
	current, err := reader.GetAgentDingTalkResponsePolicy(ctx, task.AgentID)
	if err != nil {
		return nil, fmt.Errorf("load DingTalk response policy for task: %w", err)
	}
	policy.ShowAITag = current.DingtalkShowAiTag
	applyDingTalkOriginReply(policy, stored)
	return policy, nil
}

func (h *Handler) failDingTalkTaskPolicyClaim(ctx context.Context, task db.AgentTaskQueue, cause error) *claimBuildFailure {
	reason := taskfailure.ReasonAgentMissingConfig
	status := http.StatusInternalServerError
	if errors.Is(cause, errDingTalkMessagePolicyCapability) {
		reason = taskfailure.ReasonAgentRuntimeVersionUnsupported
		status = http.StatusConflict
	}
	if _, err := h.TaskService.FailTask(ctx, task.ID, cause.Error(), "", "", string(reason), false, ""); err != nil {
		return &claimBuildFailure{outcome: "error_dingtalk_message_policy", status: http.StatusInternalServerError, message: "failed to terminate task after DingTalk message policy failure"}
	}
	return &claimBuildFailure{outcome: "error_dingtalk_message_policy", status: status, message: cause.Error()}
}
