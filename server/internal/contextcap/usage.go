package contextcap

import (
	"context"
)

// ConnectorScopeUse is one scene or person scope of an agent that has a
// binding row or a stored credential for a connector. It backs the agent's
// connected-apps view (docs/context-capabilities.md §6 "Connected apps").
type ConnectorScopeUse struct {
	ConnectorID string
	ScopeType   string
	ScopeKey    string
	// ScopeTitle is the binding's title snapshot ("" when the scope has no
	// binding row or the row has no title; see GrantTitles).
	ScopeTitle string
	// Enabled reports an enabled binding row. It does not look at the offer
	// catalog: callers that report effective use combine it with Offers.
	Enabled bool
	// ShareInGroups is the personal connector opt-in (person bindings only).
	ShareInGroups bool
	// Connected reports a stored credential for the scope (an OAuth account
	// or a pasted token), whether or not a binding is on; Hint is its
	// write-only display hint.
	Connected bool
	Hint      string
}

// ListConnectorScopeUses returns every scene and person scope of the agent
// under orgID (the agent's current DingTalk org) that has a binding row or a
// credential for one of connectorIDs, ordered by connector, scope type and
// scope key. Both tables are read through their (agent_id, ...) unique
// indexes.
func ListConnectorScopeUses(ctx context.Context, db DBTX, workspaceID, agentID, orgID string, connectorIDs []string) ([]ConnectorScopeUse, error) {
	ids, err := canonicalUUIDSet(connectorIDs)
	if err != nil {
		return nil, err
	}
	out := []ConnectorScopeUse{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `WITH b AS (
		  SELECT resource_id AS connector_id, scope_type, scope_key, scope_title, enabled, share_in_groups
		  FROM context_capability_binding
		  WHERE agent_id = $2::uuid AND workspace_id = $1::uuid AND org_id = $3::text
		    AND scope_type IN ('scene', 'person') AND resource_type = 'connector' AND resource_id = ANY($4::uuid[])
		), c AS (
		  SELECT connector_id, scope_type, scope_key, hint
		  FROM context_connector_credential
		  WHERE agent_id = $2::uuid AND workspace_id = $1::uuid AND org_id = $3::text
		    AND connector_id = ANY($4::uuid[])
		)
		SELECT COALESCE(b.connector_id, c.connector_id)::text, COALESCE(b.scope_type, c.scope_type), COALESCE(b.scope_key, c.scope_key),
		  COALESCE(b.scope_title, ''), COALESCE(b.enabled, FALSE), COALESCE(b.share_in_groups, FALSE),
		  c.connector_id IS NOT NULL, COALESCE(c.hint, '')
		FROM b FULL JOIN c ON c.connector_id = b.connector_id AND c.scope_type = b.scope_type AND c.scope_key = b.scope_key
		ORDER BY 1, 2, 3`, workspaceID, agentID, orgID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var use ConnectorScopeUse
		if err := rows.Scan(&use.ConnectorID, &use.ScopeType, &use.ScopeKey, &use.ScopeTitle, &use.Enabled, &use.ShareInGroups,
			&use.Connected, &use.Hint); err != nil {
			return nil, err
		}
		out = append(out, use)
	}
	return out, rows.Err()
}

// ScopeRef names one scene or person scope.
type ScopeRef struct {
	ScopeType string
	ScopeKey  string
}

// GrantTitles returns the newest non-empty grant title of every scene and
// person scope of the agent under orgID: the group title or the person's
// display name recorded when a configuration link was minted.
func GrantTitles(ctx context.Context, db DBTX, workspaceID, agentID, orgID string) (map[ScopeRef]string, error) {
	out := map[ScopeRef]string{}
	if err := eachNewestGrantTitle(ctx, db, workspaceID, agentID, &orgID, func(scopeType, _, scopeKey, title string) {
		out[ScopeRef{ScopeType: scopeType, ScopeKey: scopeKey}] = title
	}); err != nil {
		return nil, err
	}
	return out, nil
}
