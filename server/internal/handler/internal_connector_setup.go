package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Capability links from any approved host keep their token encrypted. The
// target deployment authenticates the credential, not the local database.
func (h *Handler) prepareInternalConnectorCreate(ctx context.Context, in *connectorInput, connectorID, workspaceID string) ([]byte, error) {
	if in.Enabled {
		return nil, errors.New("create the connector before enabling it")
	}
	if strings.Contains(in.UpstreamURL, "/api/mcp/connect/") {
		canonical, token, err := parseInternalConnectorCapabilityLink(in.UpstreamURL)
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
	// Official app URLs bypass the host allowlist only for connectors the
	// server creates from the catalog, never for a user-supplied URL.
	if connectorURLIsCatalogTemplate(in.UpstreamURL) {
		return nil, errors.New("add this official app from the connector catalog")
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

func parseInternalConnectorCapabilityLink(raw string) (string, string, error) {
	if err := validateConnectorURL(raw); err != nil {
		return "", "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("invalid MCP capability link")
	}
	const marker = "/api/mcp/connect/"
	index := strings.Index(u.Path, marker)
	if index < 0 {
		return "", "", errors.New("invalid MCP capability link")
	}
	token := u.Path[index+len(marker):]
	if !validInternalConnectorBearer(token) || strings.Contains(token, "/") {
		return "", "", errors.New("invalid MCP capability link")
	}
	u.Path = u.Path[:index] + strings.TrimSuffix(marker, "/")
	u.RawPath = ""
	return u.String(), token, nil
}

// Discovery records all tool names as metadata. No upstream mutation is invoked.
func (h *Handler) discoverInternalConnectorTools(ctx context.Context, c internalConnector) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	seen := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		result, _, err := h.connectorToolListWithRetry(ctx, c, cursor)
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
			seen[name] = true
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
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
