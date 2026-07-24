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
	InstallationOverride   *engine.ResolvedInstallation
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
	var installationOverride *engine.ResolvedInstallation
	if command.Source.Type == "digital_employee" &&
		command.Surface.Type == protocol.DispatchSurfaceTypeChat &&
		command.Outbound.Mode == protocol.DispatchOutboundModeDWS {
		if !dispatchContext.EndpointNamespaceID.Valid ||
			!dispatchContext.WorkspaceID.Valid ||
			!dispatchContext.AgentID.Valid {
			return agentDispatchExecutionPlan{}, errors.New("digital employee chat dispatch endpoint has no durable namespace")
		}
		installationOverride = &engine.ResolvedInstallation{
			ID:              dispatchContext.EndpointNamespaceID,
			WorkspaceID:     dispatchContext.WorkspaceID,
			AgentID:         dispatchContext.AgentID,
			InstallerUserID: dispatchContext.UserID,
			Active:          true,
		}
	}
	return agentDispatchExecutionPlan{
		SurfaceType:          command.Surface.Type,
		InstallationOverride: installationOverride,
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
	var installation *engine.ResolvedInstallation
	if p.InstallationOverride != nil {
		copied := *p.InstallationOverride
		installation = &copied
	}
	return engine.HandleOptions{
		InstallationOverride:   installation,
		IdentityOverride:       &identity,
		SuppressServerOutbound: p.SuppressServerOutbound,
		DisableControlCommands: p.DisableControlCommands,
	}
}
