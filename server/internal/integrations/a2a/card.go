package a2aintegration

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

const (
	// BearerSecuritySchemeName is the security scheme referenced by generated Agent Cards.
	BearerSecuritySchemeName a2a.SecuritySchemeName = "bearerAuth"
	textMIMEType                                    = "text/plain"
)

// CardConfig contains the public, non-secret fields needed to disclose one hosted agent.
type CardConfig struct {
	BaseURL       string
	PublicAgentID string
	Name          string
	Description   string
	Version       string
	Skills        []a2a.AgentSkill
}

// AgentCardURL returns the direct discovery URL for one hosted agent.
func AgentCardURL(baseURL, publicAgentID string) (string, error) {
	return agentURL(baseURL, publicAgentID, ".well-known/agent-card.json")
}

// AgentRPCURL returns the JSON-RPC v1 endpoint for one hosted agent.
func AgentRPCURL(baseURL, publicAgentID string) (string, error) {
	return agentURL(baseURL, publicAgentID, "v1")
}

// AgentMCPURL returns the canonical header-authenticated Streamable HTTP MCP
// endpoint backed by the same hosted Agent and credential as A2A.
func AgentMCPURL(baseURL, publicAgentID string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("MCP public base URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse MCP public base URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("MCP public base URL must be an absolute HTTP(S) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("MCP public base URL must not contain a query or fragment")
	}
	publicAgentID = strings.TrimSpace(publicAgentID)
	if publicAgentID == "" || strings.ContainsAny(publicAgentID, "/\\?#") || publicAgentID == "." || publicAgentID == ".." {
		return "", fmt.Errorf("MCP public agent ID is not path safe")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/mcp/agents/" + publicAgentID
	return parsed.String(), nil
}

func agentURL(baseURL, publicAgentID, suffix string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("A2A public base URL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse A2A public base URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("A2A public base URL must be an absolute HTTP(S) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("A2A public base URL must not contain a query or fragment")
	}

	publicAgentID = strings.TrimSpace(publicAgentID)
	if publicAgentID == "" {
		return "", fmt.Errorf("A2A public agent ID is required")
	}
	if strings.ContainsAny(publicAgentID, "/\\?#") || publicAgentID == "." || publicAgentID == ".." {
		return "", fmt.Errorf("A2A public agent ID is not path safe")
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/a2a/agents/" + publicAgentID + "/" + suffix
	return parsed.String(), nil
}

// BuildAgentCard builds the canonical A2A v1 Agent Card for one hosted agent.
func BuildAgentCard(config CardConfig) (*a2a.AgentCard, error) {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		return nil, fmt.Errorf("A2A Agent Card name is required")
	}
	version := strings.TrimSpace(config.Version)
	if version == "" {
		return nil, fmt.Errorf("A2A Agent Card version is required")
	}

	rpcURL, err := AgentRPCURL(config.BaseURL, config.PublicAgentID)
	if err != nil {
		return nil, err
	}

	skills := make([]a2a.AgentSkill, len(config.Skills))
	copy(skills, config.Skills)
	for i := range skills {
		if skills[i].Tags == nil {
			skills[i].Tags = []string{}
		}
	}

	return &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			{
				URL:             rpcURL,
				ProtocolBinding: a2a.TransportProtocolJSONRPC,
				ProtocolVersion: a2a.Version,
			},
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming:         false,
			PushNotifications: false,
			ExtendedAgentCard: false,
		},
		DefaultInputModes:  []string{textMIMEType},
		DefaultOutputModes: []string{textMIMEType},
		Description:        config.Description,
		Name:               name,
		SecurityRequirements: a2a.SecurityRequirementsOptions{
			{
				BearerSecuritySchemeName: a2a.SecuritySchemeScopes{},
			},
		},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			BearerSecuritySchemeName: a2a.HTTPAuthSecurityScheme{
				Scheme:       "bearer",
				BearerFormat: "opaque",
				Description:  "Opaque Multica A2A access token.",
			},
		},
		Skills:  skills,
		Version: version,
	}, nil
}
