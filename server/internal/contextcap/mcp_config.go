package contextcap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// MaxScopeMCPConfigBytes bounds one scope's custom MCP server configuration.
const MaxScopeMCPConfigBytes = 64 << 10

// ScopeMCPConfig is one row of context_scope_mcp_config: the custom MCP
// servers of one org, scene or person scope, in the agent mcp_config
// format. MergeContext merges them across layers, nearest layer wins by
// server name.
type ScopeMCPConfig struct {
	ScopeType string
	OrgID     string
	ScopeKey  string
	MCPConfig json.RawMessage
	// UpdatedBy is the user who last wrote it ("" when unknown).
	UpdatedBy string
	UpdatedAt time.Time
}

// ScopeMCPConfigWrite is the input of PutScopeMCPConfig. ActorID may be
// empty.
type ScopeMCPConfigWrite struct {
	WorkspaceID string
	AgentID     string
	ScopeType   string
	OrgID       string
	ScopeKey    string
	MCPConfig   json.RawMessage
	ActorID     string
}

// NormalizeScopeMCPConfig validates a custom MCP server configuration the way
// the agent's own mcp_config is accepted (any JSON object of the agent
// mcp_config format) and returns it compacted. A JSON null, an empty body or
// an empty object mean "no custom servers" and return nil. Anything else that
// is not a JSON object, or is larger than MaxScopeMCPConfigBytes, is
// ErrInvalidInput.
func NormalizeScopeMCPConfig(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if len(trimmed) > MaxScopeMCPConfigBytes || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, ErrInvalidInput
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil, ErrInvalidInput
	}
	if len(object) == 0 {
		return nil, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, ErrInvalidInput
	}
	return json.RawMessage(compact.Bytes()), nil
}

// Remote custom MCP servers (the configure page, docs/context-capabilities.md
// §6): a server is reached over HTTP(S) at its url; local commands are never
// accepted there.
const (
	// MaxMCPServerName bounds a custom MCP server name, in characters.
	MaxMCPServerName = 64
	// maxRemoteMCPURL bounds a remote server's url, in bytes.
	maxRemoteMCPURL = 2048
)

// remoteMCPServerTypes are the transport types a remote server may name.
var remoteMCPServerTypes = map[string]bool{"http": true, "sse": true, "streamable-http": true}

// mcpHeaderNamePattern is an HTTP header field name (RFC 9110 token).
var mcpHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// InvalidMCPConfigError is a configuration NormalizeRemoteMCPConfig refused;
// Reason says why (English, safe to return to the caller). It matches
// ErrInvalidInput.
type InvalidMCPConfigError struct {
	Reason string
}

func (e *InvalidMCPConfigError) Error() string { return "contextcap: invalid MCP config: " + e.Reason }

// Is makes errors.Is(err, ErrInvalidInput) hold.
func (e *InvalidMCPConfigError) Is(target error) bool { return target == ErrInvalidInput }

func invalidMCPConfig(format string, args ...any) error {
	return &InvalidMCPConfigError{Reason: fmt.Sprintf(format, args...)}
}

// NormalizeRemoteMCPConfig validates a custom MCP server configuration that
// may only name remote servers and returns it compacted
// (NormalizeScopeMCPConfig), or nil when it names no server (null, {} and
// {"mcpServers": {}} all clear the scope). The document holds only
// mcpServers; every server is a JSON object with a url (http or https, with
// a host) and otherwise only type ("http", "sse" or "streamable-http"),
// headers (a map of header names to string values without CR, LF or NUL)
// and disabled (a boolean, MCPServerDisabledKey). command, args, env, cwd
// and any other key are refused, as are server names that are empty, longer
// than MaxMCPServerName characters, padded with spaces or that contain
// control characters. Refusals are *InvalidMCPConfigError.
func NormalizeRemoteMCPConfig(raw json.RawMessage) (json.RawMessage, error) {
	config, err := NormalizeScopeMCPConfig(raw)
	if err != nil {
		return nil, invalidMCPConfig("mcp_config must be a JSON object of at most 64 KiB")
	}
	if config == nil {
		return nil, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(config, &document); err != nil {
		return nil, invalidMCPConfig("mcp_config must be a JSON object")
	}
	for key := range document {
		if key != "mcpServers" {
			return nil, invalidMCPConfig("mcp_config may only hold mcpServers, not %q", key)
		}
	}
	servers, err := MCPServers(config)
	if err != nil {
		return nil, invalidMCPConfig("mcpServers must be an object of servers")
	}
	if len(servers) == 0 {
		return nil, nil
	}
	for name, server := range servers {
		if !validMCPServerName(name) {
			return nil, invalidMCPConfig("server name %q must be 1-%d characters without surrounding spaces or control characters", name, MaxMCPServerName)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(server, &fields); err != nil || fields == nil {
			return nil, invalidMCPConfig("server %q must be an object", name)
		}
		if _, ok := fields["url"]; !ok {
			return nil, invalidMCPConfig("server %q needs a url: only remote servers are allowed", name)
		}
		for key, value := range fields {
			if err := validateRemoteMCPField(name, key, value); err != nil {
				return nil, err
			}
		}
	}
	return config, nil
}

// validateRemoteMCPField checks one key of a remote server object.
func validateRemoteMCPField(name, key string, value json.RawMessage) error {
	switch key {
	case "url":
		var raw string
		if err := json.Unmarshal(value, &raw); err != nil || !validRemoteMCPURL(raw) {
			return invalidMCPConfig("server %q: url must be an http or https URL with a host", name)
		}
	case "type":
		var kind string
		if err := json.Unmarshal(value, &kind); err != nil || !remoteMCPServerTypes[kind] {
			return invalidMCPConfig(`server %q: type must be "http", "sse" or "streamable-http"`, name)
		}
	case "headers":
		var headers map[string]string
		if err := json.Unmarshal(value, &headers); err != nil {
			return invalidMCPConfig("server %q: headers must map header names to strings", name)
		}
		for header, headerValue := range headers {
			if !mcpHeaderNamePattern.MatchString(header) || strings.ContainsAny(headerValue, "\r\n\x00") {
				return invalidMCPConfig("server %q: header %q is not a valid HTTP header", name, header)
			}
		}
	case MCPServerDisabledKey:
		if trimmed := bytes.TrimSpace(value); !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return invalidMCPConfig("server %q: disabled must be true or false", name)
		}
	default:
		return invalidMCPConfig("server %q: %q is not allowed; only remote servers (url, type, headers, disabled) can be added here", name, key)
	}
	return nil
}

// validRemoteMCPURL reports an absolute http or https URL with a host.
func validRemoteMCPURL(raw string) bool {
	if raw == "" || len(raw) > maxRemoteMCPURL || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme == "http" || scheme == "https"
}

// validMCPServerName reports a custom MCP server name of 1..MaxMCPServerName
// characters without surrounding spaces or control characters.
func validMCPServerName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxMCPServerName {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// GetScopeMCPConfig returns the custom MCP servers of one org, scene or
// person scope, or ErrNotFound when the scope has none.
func GetScopeMCPConfig(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string) (ScopeMCPConfig, error) {
	if !ValidConfigScope(scopeType, orgID, scopeKey) {
		return ScopeMCPConfig{}, ErrInvalidInput
	}
	out := ScopeMCPConfig{}
	var raw []byte
	err := db.QueryRow(ctx, `SELECT scope_type, org_id, scope_key, mcp_config, COALESCE(updated_by::text, ''), updated_at
		FROM context_scope_mcp_config
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = $3 AND org_id = $4 AND scope_key = $5`,
		workspaceID, agentID, scopeType, orgID, scopeKey,
	).Scan(&out.ScopeType, &out.OrgID, &out.ScopeKey, &raw, &out.UpdatedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScopeMCPConfig{}, ErrNotFound
	}
	if err != nil {
		return ScopeMCPConfig{}, err
	}
	out.MCPConfig = json.RawMessage(raw)
	return out, nil
}

// PutScopeMCPConfig stores the custom MCP servers of one org, scene or person
// scope. The configuration goes through NormalizeScopeMCPConfig; one that
// normalizes to nothing deletes the row instead (the returned config then
// has a nil MCPConfig). A row of another workspace is never overwritten
// (ErrNotFound).
func PutScopeMCPConfig(ctx context.Context, db DBTX, in ScopeMCPConfigWrite) (ScopeMCPConfig, error) {
	if !ValidConfigScope(in.ScopeType, in.OrgID, in.ScopeKey) {
		return ScopeMCPConfig{}, ErrInvalidInput
	}
	for _, id := range []string{in.WorkspaceID, in.AgentID} {
		if _, err := canonicalUUID(id); err != nil {
			return ScopeMCPConfig{}, err
		}
	}
	config, err := NormalizeScopeMCPConfig(in.MCPConfig)
	if err != nil {
		return ScopeMCPConfig{}, err
	}
	actor, err := optionalUUID(in.ActorID)
	if err != nil {
		return ScopeMCPConfig{}, err
	}
	if config == nil {
		if _, err := db.Exec(ctx, `DELETE FROM context_scope_mcp_config
			WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = $3 AND org_id = $4 AND scope_key = $5`,
			in.WorkspaceID, in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey); err != nil {
			return ScopeMCPConfig{}, err
		}
		return ScopeMCPConfig{ScopeType: in.ScopeType, OrgID: in.OrgID, ScopeKey: in.ScopeKey}, nil
	}
	out := ScopeMCPConfig{}
	var raw []byte
	err = db.QueryRow(ctx, `INSERT INTO context_scope_mcp_config AS m
		(workspace_id, agent_id, scope_type, org_id, scope_key, mcp_config, updated_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7::uuid)
		ON CONFLICT (agent_id, scope_type, org_id, scope_key)
		DO UPDATE SET mcp_config = EXCLUDED.mcp_config, updated_by = EXCLUDED.updated_by, updated_at = now()
		WHERE m.workspace_id = EXCLUDED.workspace_id
		RETURNING m.scope_type, m.org_id, m.scope_key, m.mcp_config, COALESCE(m.updated_by::text, ''), m.updated_at`,
		in.WorkspaceID, in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey, string(config), actor,
	).Scan(&out.ScopeType, &out.OrgID, &out.ScopeKey, &raw, &out.UpdatedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScopeMCPConfig{}, ErrNotFound
	}
	if err != nil {
		return ScopeMCPConfig{}, err
	}
	out.MCPConfig = json.RawMessage(raw)
	return out, nil
}
