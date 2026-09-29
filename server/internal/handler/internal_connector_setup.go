package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
)

// prepareInternalConnectorCreate resolves only this deployment's capability
// links. The secret-bearing URL is never persisted or returned. Ordinary MCP
// URLs remain fixed HTTPS targets and may use no auth or an optional Bearer.
func (h *Handler) prepareInternalConnectorCreate(ctx context.Context, in *connectorInput, connectorID, workspaceID string) ([]byte, error) {
	if in.Enabled {
		return nil, errors.New("create the connector before enabling it")
	}
	if strings.Contains(in.UpstreamURL, "/api/mcp/connect/") {
		canonical, token, err := h.resolveInternalConnectorCapabilityLink(ctx, in.UpstreamURL)
		if err != nil {
			return nil, err
		}
		if in.BearerToken != "" || in.AuthMode == "none" {
			return nil, errors.New("capability link supplies its own Bearer credential")
		}
		in.UpstreamURL, in.BearerToken, in.AuthMode = canonical, token, "bearer"
	} else if in.AuthMode == "" {
		if in.AutoDiscover && in.BearerToken == "" {
			in.AuthMode = "none"
		} else {
			in.AuthMode = "bearer"
		}
	}
	if in.AuthMode == "none" && in.BearerToken != "" {
		return nil, errors.New("no-auth connector cannot have a Bearer credential")
	}
	if in.AutoDiscover && in.AuthMode == "bearer" && in.BearerToken == "" {
		return nil, errors.New("Bearer credential is required to discover tools")
	}
	if err := validateConnectorURL(in.UpstreamURL); err != nil {
		return nil, err
	}
	var sealed []byte
	if in.BearerToken != "" {
		if !validInternalConnectorBearer(in.BearerToken) || h.InternalConnectorSecretBox == nil {
			return nil, errors.New("connector credential cannot be saved")
		}
		payload, err := json.Marshal(connectorSealedCredential{WorkspaceID: workspaceID, ConnectorID: connectorID, Bearer: in.BearerToken})
		if err != nil {
			return nil, errors.New("connector credential cannot be saved")
		}
		sealed, err = h.InternalConnectorSecretBox.Seal(payload)
		if err != nil {
			return nil, errors.New("connector credential cannot be saved")
		}
	}
	if in.AutoDiscover {
		c := internalConnector{ID: connectorID, WorkspaceID: workspaceID, UpstreamURL: in.UpstreamURL, CredentialRef: connectorCredentialRef(connectorID), AuthMode: in.AuthMode, CredentialCiphertext: sealed}
		tools, err := h.discoverInternalConnectorTools(ctx, c)
		if err != nil {
			return nil, err
		}
		in.AllowedTools = tools
	}
	return sealed, nil
}

func validInternalConnectorBearer(token string) bool {
	return token != "" && len(token) <= 4096 && strings.TrimSpace(token) == token && !strings.ContainsAny(token, "\r\n\x00")
}

func (h *Handler) resolveInternalConnectorCapabilityLink(ctx context.Context, raw string) (string, string, error) {
	invalid := errors.New("invalid or expired MCP capability link for this deployment")
	base, err := normalizeAgentA2APublicBaseURL(h.currentConfig().PublicURL)
	if err != nil {
		return "", "", invalid
	}
	token, err := parseInternalConnectorCapabilityLink(raw, base)
	if err != nil {
		return "", "", invalid
	}
	credential, err := h.Queries.GetAgentA2ACredentialByTokenHash(ctx, auth.HashToken(token))
	if err != nil || !credential.AgentOwnerID.Valid || !credential.DelegatedByUserID.Valid || credential.AgentOwnerID.Bytes != credential.DelegatedByUserID.Bytes {
		return "", "", invalid
	}
	canonical, err := a2aintegration.AgentMCPURL(base, credential.PublicAgentID)
	if err != nil {
		return "", "", invalid
	}
	return canonical, token, nil
}

func parseInternalConnectorCapabilityLink(raw, base string) (string, error) {
	invalid := errors.New("invalid MCP capability link")
	baseURL, _ := url.Parse(base)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != baseURL.Scheme || !strings.EqualFold(u.Host, baseURL.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", invalid
	}
	prefix := strings.TrimRight(baseURL.Path, "/") + "/api/mcp/connect/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", invalid
	}
	token := strings.TrimPrefix(u.Path, prefix)
	if !validAgentAccessToken(token) {
		return "", invalid
	}
	return token, nil
}

// Discovery pins tool names at creation. No upstream mutation is invoked.
func (h *Handler) discoverInternalConnectorTools(ctx context.Context, c internalConnector) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	seen := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		result, _, err := h.connectorToolListWithRetry(ctx, c, cursor, false)
		if err != nil {
			return nil, errors.New("could not discover MCP tools; check the address and authentication")
		}
		list, ok := result.(map[string]any)
		if !ok {
			return nil, errors.New("invalid upstream MCP tool list")
		}
		entries, ok := list["tools"].([]map[string]any)
		if !ok {
			return nil, errors.New("invalid upstream MCP tool list")
		}
		for _, tool := range entries {
			name, _ := tool["name"].(string)
			if name == "" {
				return nil, errors.New("upstream MCP tool has no name")
			}
			annotations, _ := tool["annotations"].(map[string]any)
			if annotations["readOnlyHint"] != true {
				continue
			}
			seen[name] = true
			if len(seen) > 64 {
				return nil, errors.New("MCP server has more than 64 tools")
			}
		}
		next, _ := list["nextCursor"].(string)
		if next == "" {
			break
		}
		if seenCursors[next] || page == 7 {
			return nil, errors.New("MCP tool list has invalid or excessive pagination")
		}
		seenCursors[next] = true
		cursor = next
	}
	if len(seen) == 0 {
		return nil, errors.New("MCP server has no tools marked read-only")
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
