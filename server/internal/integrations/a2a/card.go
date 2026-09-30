package a2aintegration

import (
	"crypto/sha256"
	"encoding/hex"
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
	BaseURL                string
	PublicAgentID          string
	Name                   string
	Description            string
	Version                string
	Skills                 []a2a.AgentSkill
	Streaming              bool
	PushNotifications      bool
	AgentIdentityExtension bool
	// DingTalkEventExtension declares the DEAP DingTalk event extension as
	// optional so DEAP attaches conversation, sender and thread context.
	DingTalkEventExtension bool
	InputModes             []string
	OutputModes            []string
}

// ConfiguredAgentSkill is the public metadata subset of a Skill enabled on a
// Multica Agent. The database identifier is used only to derive a stable,
// opaque Agent Card skill ID.
type ConfiguredAgentSkill struct {
	ID          string
	Name        string
	Description string
}

// MergeConfiguredAgentSkills adds the Agent's enabled Multica Skills to any
// explicitly declared Agent Card capabilities. It never exposes a database
// UUID and keeps the resulting identifiers stable across card requests.
func MergeConfiguredAgentSkills(declared []a2a.AgentSkill, configured []ConfiguredAgentSkill) ([]a2a.AgentSkill, error) {
	result := make([]a2a.AgentSkill, len(declared), len(declared)+len(configured))
	copy(result, declared)
	seen := make(map[string]struct{}, len(result)+len(configured))
	for _, skill := range result {
		seen[skill.ID] = struct{}{}
	}

	for _, skill := range configured {
		internalID := strings.TrimSpace(skill.ID)
		name := strings.TrimSpace(skill.Name)
		if internalID == "" || name == "" {
			return nil, fmt.Errorf("configured Multica Agent Skill requires id and name")
		}
		digest := sha256.Sum256([]byte(internalID))
		publicID := "multica-skill-" + hex.EncodeToString(digest[:])
		if _, exists := seen[publicID]; exists {
			continue
		}
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = fmt.Sprintf("Provides the configured Multica skill %q.", name)
		}
		result = append(result, a2a.AgentSkill{
			ID:          publicID,
			Name:        name,
			Description: description,
			Tags:        []string{"multica", "configured-skill"},
		})
		seen[publicID] = struct{}{}
	}
	return result, nil
}

func baseAgentTaskSkill() a2a.AgentSkill {
	return a2a.AgentSkill{
		ID:          "multica-agent-task",
		Name:        "Multica Agent task",
		Description: "Runs a durable multi-turn Agent task with text, structured data, and file input or artifacts.",
		Tags:        []string{"multica", "multi-turn", "multimodal", "structured-data"},
	}
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
	if len(skills) == 0 {
		skills = append(skills, baseAgentTaskSkill())
	}
	for i := range skills {
		if skills[i].Tags == nil {
			skills[i].Tags = []string{}
		}
	}

	inputModes := append([]string(nil), config.InputModes...)
	if len(inputModes) == 0 {
		inputModes = []string{textMIMEType}
	}
	outputModes := append([]string(nil), config.OutputModes...)
	if len(outputModes) == 0 {
		outputModes = []string{textMIMEType}
	}
	capabilities := a2a.AgentCapabilities{
		Streaming:         config.Streaming,
		PushNotifications: config.PushNotifications,
		ExtendedAgentCard: false,
	}
	if config.AgentIdentityExtension {
		capabilities.Extensions = append(capabilities.Extensions, a2a.AgentExtension{
			URI:         AgentIdentityExtensionURI,
			Required:    false,
			Description: "Accepts an optional external Multica Agent Identity ContextToken for one task turn.",
		})
	}
	if config.DingTalkEventExtension {
		capabilities.Extensions = append(capabilities.Extensions, a2a.AgentExtension{
			URI:         DingTalkEventExtensionURI,
			Required:    false,
			Description: "DingTalk event context",
		})
	}

	return &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			{
				URL:             rpcURL,
				ProtocolBinding: a2a.TransportProtocolJSONRPC,
				ProtocolVersion: a2a.Version,
			},
		},
		Capabilities:       capabilities,
		DefaultInputModes:  inputModes,
		DefaultOutputModes: outputModes,
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
