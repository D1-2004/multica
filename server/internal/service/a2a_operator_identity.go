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

// A2AOperatorIdentityReader reads the operator-only A2A settings of one Agent.
type A2AOperatorIdentityReader interface {
	GetAgentA2AOperatorConfig(context.Context, db.GetAgentA2AOperatorConfigParams) (db.AgentA2aOperatorConfig, error)
}

// loadA2AOperatorDWSIdentity returns the DEAP employee identity an operator
// bound to the Agent. A missing row, a cleared binding or a read failure all
// mean "no bound identity"; the A2A turn then runs without a DWS identity
// instead of failing.
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
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("load A2A operator identity failed",
				"workspace_id", util.UUIDToString(workspaceID),
				"agent_id", util.UUIDToString(agentID),
				"error", err,
			)
		}
		return a2aDingTalkBoundIdentity{}
	}
	if !config.DwsUid.Valid || !config.DwsOrgID.Valid || config.DwsUid.String == "" || config.DwsOrgID.String == "" {
		return a2aDingTalkBoundIdentity{}
	}
	bound := a2aDingTalkBoundIdentity{UID: config.DwsUid.String, OrgID: config.DwsOrgID.String}
	if config.DeapAgentUuid.Valid {
		bound.DEAPAgentUUID = config.DeapAgentUuid.String
	}
	return bound
}
