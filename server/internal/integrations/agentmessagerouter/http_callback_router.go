package agentmessagerouter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util"
)

// HTTPCallbackRouterService adapts DingTalk registration to Router's trusted
// robot-source API. Dispatch endpoints are Agent-scoped and persisted by
// DispatchEndpointService, so robot and digital-employee sources bound to the
// same Agent always reuse one Multica callback URL.
type HTTPCallbackRouterService struct {
	client    *Client
	endpoints *DispatchEndpointService
}

func NewHTTPCallbackRouterService(
	client *Client,
	endpoints *DispatchEndpointService,
) (*HTTPCallbackRouterService, error) {
	if client == nil || endpoints == nil {
		return nil, errors.New("HTTP callback router dependencies are required")
	}
	return &HTTPCallbackRouterService{client: client, endpoints: endpoints}, nil
}

func (s *HTTPCallbackRouterService) PrepareEndpoint(
	ctx context.Context,
	workspaceID, agentID, actorUserID pgtype.UUID,
) (dingtalk.HTTPCallbackEndpoint, error) {
	endpoint, err := s.endpoints.Ensure(ctx, workspaceID, agentID, actorUserID)
	if err != nil {
		return dingtalk.HTTPCallbackEndpoint{}, fmt.Errorf("ensure agent dispatch endpoint: %w", err)
	}
	return dingtalk.HTTPCallbackEndpoint{
		EndpointID:  endpoint.EndpointID,
		DispatchURL: endpoint.DispatchURL,
	}, nil
}

func (s *HTTPCallbackRouterService) Register(
	ctx context.Context,
	endpoint dingtalk.HTTPCallbackEndpoint,
	agentID pgtype.UUID,
	robotCode, clientID, clientSecret string,
) (string, error) {
	robotCode = strings.TrimSpace(robotCode)
	if robotCode == "" {
		return "", errors.New("register robot source: robot_code is required")
	}
	dispatchPath, err := dispatchPathForEndpointID(endpoint.EndpointID)
	if err != nil {
		return "", fmt.Errorf("register robot source: %w", err)
	}
	subscription, err := s.client.RegisterRobot(ctx, RobotRegistration{
		RobotCode:              robotCode,
		ClientID:               clientID,
		ClientSecret:           clientSecret,
		AgentID:                util.UUIDToString(agentID),
		DispatchURL:            dispatchPath,
		Surface:                SubscriptionSurface{Type: "chat"},
		Outbound:               SubscriptionOutbound{Mode: "robot_sdk", ReplyTo: "latest_message"},
		ReplaceExistingBinding: true,
	})
	if err != nil {
		return "", fmt.Errorf("register robot source: %w", err)
	}
	return subscription.SourceID, nil
}
