package contextcap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxScopeMCPConfigBytes bounds one scope's custom MCP server configuration.
const MaxScopeMCPConfigBytes = 64 << 10

// ScopeMCPConfig is one row of context_scope_mcp_config: the custom MCP
// servers of one scene or person scope, in the agent mcp_config format.
// Configuration only: stored and shown, not applied at runtime yet.
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

func validScopeMCPConfigScope(scopeType, scopeKey string) bool {
	return (scopeType == ScopeScene || scopeType == ScopePerson) && ValidScopeKey(scopeType, scopeKey)
}

// GetScopeMCPConfig returns the custom MCP servers of one scene or person
// scope, or ErrNotFound when the scope has none.
func GetScopeMCPConfig(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string) (ScopeMCPConfig, error) {
	if !validScopeMCPConfigScope(scopeType, scopeKey) {
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

// PutScopeMCPConfig stores the custom MCP servers of one scene or person
// scope. The configuration goes through NormalizeScopeMCPConfig; one that
// normalizes to nothing deletes the row instead (the returned config then
// has a nil MCPConfig). A row of another workspace is never overwritten
// (ErrNotFound).
func PutScopeMCPConfig(ctx context.Context, db DBTX, in ScopeMCPConfigWrite) (ScopeMCPConfig, error) {
	if !validScopeMCPConfigScope(in.ScopeType, in.ScopeKey) {
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
