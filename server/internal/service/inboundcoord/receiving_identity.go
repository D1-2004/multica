package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type messageIdentityReader interface {
	GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error)
}

// Message routing and execution authorization have independent lifecycles.
// A message-only binding is a valid receiving identity even without a DWS
// execution authorization row. Never borrow the execution account's name.
func (c *Coordinator) fillReceivingIdentity(ctx context.Context, turn *Turn) {
	turn.EmployeeAccountName = ""
	workspaceID, err := util.ParseUUID(turn.WorkspaceID)
	if err != nil || turn.Source != SourceDigitalEmployee || turn.DWSUID == "" {
		return
	}
	if reader, ok := c.Queries.(messageIdentityReader); ok {
		binding, readErr := reader.GetDingTalkAccountBindingByAgent(ctx, db.GetDingTalkAccountBindingByAgentParams{WorkspaceID: workspaceID, AgentID: turn.AgentID})
		if readErr == nil {
			turn.EmployeeAccountName = receivingBindingName(*turn, binding)
			return
		}
	}
	// Older ingress may only have an execution identity. It is usable only
	// when both identifiers exactly match this trusted receiving event.
	if identity, err := c.Queries.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: workspaceID, AgentID: turn.AgentID}); err == nil && identity.DwsUid == turn.DWSUID && identity.OrgID == turn.DWSOrgID {
		turn.EmployeeAccountName = strings.TrimSpace(identity.AccountDisplayName)
	}
}

func receivingBindingName(turn Turn, binding db.ChannelInstallation) string {
	if binding.Status != "active" || binding.ChannelType != "dingtalk_account" || binding.AgentID != turn.AgentID || util.UUIDToString(binding.WorkspaceID) != turn.WorkspaceID {
		return ""
	}
	// Decode only non-secret facts from the authenticated Agent's active route.
	var facts struct {
		Name      string `json:"account_display_name"`
		AccountID string `json:"router_account_id"`
		TenantID  string `json:"router_tenant_id"`
	}
	if json.Unmarshal(binding.Config, &facts) != nil {
		return ""
	}
	if (facts.AccountID != "" || facts.TenantID != "") && (facts.AccountID != turn.DWSUID || facts.TenantID != turn.DWSOrgID) {
		return ""
	}
	// Legacy message-only bindings lack the account-key enrichment. Their
	// existing active route, pinned to the authenticated workspace + Agent,
	// owns this display name; it grants no DWS identity or execution permission.
	return strings.TrimSpace(facts.Name)
}

// A digital employee's configuration title is never an external account alias.
// Keep unavailable receiving facts unavailable in both decision and review.
func conversationAgentName(turn Turn) string {
	if turn.Source == SourceDigitalEmployee {
		return strings.TrimSpace(turn.EmployeeAccountName)
	}
	return strings.TrimSpace(turn.AgentName)
}

func receivingIdentityStatus(turn Turn) string {
	if turn.Source != SourceDigitalEmployee {
		return "not_applicable"
	}
	if strings.TrimSpace(turn.EmployeeAccountName) == "" {
		return "name_unavailable"
	}
	return "loaded"
}
