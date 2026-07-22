package handler

import (
	"errors"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agentDispatchExecutionPlan keeps persistence surface, authorization identity,
// prompt projection, and outbound ownership independent after wire validation.
type agentDispatchExecutionPlan struct {
	SurfaceType            string
	Identity               engine.ResolvedIdentity
	Prompt                 DispatchPrompt
	SuppressServerOutbound bool
	DisableControlCommands bool
}

func buildAgentDispatchExecutionPlan(command DispatchCommand, dispatchContext agentDispatchContext) (agentDispatchExecutionPlan, error) {
	if !dispatchContext.UserID.Valid {
		return agentDispatchExecutionPlan{}, errors.New("agent dispatch endpoint has no actor")
	}
	prompt, err := BuildDispatchPrompt(command)
	if err != nil {
		return agentDispatchExecutionPlan{}, err
	}
	return agentDispatchExecutionPlan{
		SurfaceType: command.Surface.Type,
		Identity: engine.ResolvedIdentity{
			PrincipalUserID: dispatchContext.UserID,
		},
		Prompt:                 prompt,
		SuppressServerOutbound: command.Outbound.Mode == protocol.DispatchOutboundModeDWS,
		DisableControlCommands: true,
	}, nil
}

func (p agentDispatchExecutionPlan) channelHandleOptions() engine.HandleOptions {
	identity := p.Identity
	return engine.HandleOptions{
		IdentityOverride:       &identity,
		SuppressServerOutbound: p.SuppressServerOutbound,
		DisableControlCommands: p.DisableControlCommands,
	}
}
