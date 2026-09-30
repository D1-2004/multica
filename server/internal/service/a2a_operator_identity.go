package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A2AOperatorIdentityReader reads what an A2A turn needs to run as the Agent's
// DingTalk identity: the operator switch that lets A2A use it, and the identity
// itself, which is the same agent_dingtalk_identity row the Integrations
// DingTalk binding writes.
type A2AOperatorIdentityReader interface {
	GetAgentA2AOperatorConfig(context.Context, db.GetAgentA2AOperatorConfigParams) (db.GetAgentA2AOperatorConfigRow, error)
	GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error)
}

// loadA2AOperatorDWSIdentity returns the Agent's DingTalk identity when an
// operator enabled it for A2A. A missing switch, a missing identity or a read
// failure all mean "no identity"; the A2A turn then runs without a DWS
// identity instead of failing. Agents whose identity was bound only through
// Integrations keep A2A identity-free until an operator opts them in.
func loadA2AOperatorDWSIdentity(
	ctx context.Context,
	reader A2AOperatorIdentityReader,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
) a2aDingTalkBoundIdentity {
	if reader == nil || !workspaceID.Valid || !agentID.Valid {
		return a2aDingTalkBoundIdentity{}
	}
	config, err := reader.GetAgentA2AOperatorConfig(ctx, db.GetAgentA2AOperatorConfigParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil {
		logA2AOperatorIdentityReadError(err, workspaceID, agentID)
		return a2aDingTalkBoundIdentity{}
	}
	if !config.A2aIdentityEnabled {
		return a2aDingTalkBoundIdentity{}
	}
	identity, err := reader.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil {
		logA2AOperatorIdentityReadError(err, workspaceID, agentID)
		return a2aDingTalkBoundIdentity{}
	}
	if identity.DwsUid == "" || identity.OrgID == "" {
		return a2aDingTalkBoundIdentity{}
	}
	bound := a2aDingTalkBoundIdentity{UID: identity.DwsUid, OrgID: identity.OrgID}
	if config.DeapAgentUuid.Valid {
		bound.DEAPAgentUUID = config.DeapAgentUuid.String
	}
	return bound
}

func logA2AOperatorIdentityReadError(err error, workspaceID, agentID pgtype.UUID) {
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	slog.Warn("load A2A operator identity failed",
		"workspace_id", util.UUIDToString(workspaceID),
		"agent_id", util.UUIDToString(agentID),
		"error", err,
	)
}

// A2AOperatorIdentity returns the Agent's DingTalk identity when an operator
// enabled it for A2A; the production forwarder matches registrations on it.
func A2AOperatorIdentity(ctx context.Context, reader A2AOperatorIdentityReader, workspaceID, agentID pgtype.UUID) (string, string, bool) {
	bound := loadA2AOperatorDWSIdentity(ctx, reader, workspaceID, agentID)
	return bound.UID, bound.OrgID, bound.UID != "" && bound.OrgID != ""
}
