package contextcap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the subset of pgx shared by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	// ErrNotFound means no matching row exists (or, for links, the link is
	// unknown, expired or already consumed; for grants, none is live).
	ErrNotFound = errors.New("contextcap: not found")
	// ErrInvalidInput rejects malformed identifiers, scope types or keys.
	ErrInvalidInput = errors.New("contextcap: invalid input")
	// ErrUnknownResource means an offered id is not a connector or skill of
	// the workspace.
	ErrUnknownResource = errors.New("contextcap: resource is not in the workspace")
	// ErrNotOffered means an enabling scene/person binding targets a resource
	// that is not in the agent's enabled offer catalog.
	ErrNotOffered = errors.New("contextcap: resource is not offered by the agent")
)

// Binding is one row of context_capability_binding. IDs are canonical
// lowercase UUID strings.
type Binding struct {
	ScopeType    string
	OrgID        string
	ScopeKey     string
	ScopeTitle   string
	ResourceType string
	ResourceID   string
	Enabled      bool
}

// Offers is an agent's enabled offer catalog.
type Offers struct {
	ConnectorIDs []string
	SkillIDs     []string
}

// Contains reports whether the catalog offers resourceID of resourceType.
func (o Offers) Contains(resourceType, resourceID string) bool {
	var ids []string
	switch resourceType {
	case ResourceConnector:
		ids = o.ConnectorIDs
	case ResourceSkill:
		ids = o.SkillIDs
	default:
		return false
	}
	for _, id := range ids {
		if id == resourceID {
			return true
		}
	}
	return false
}

// ListOffers returns the agent's enabled offer catalog in creation order.
// Offers whose connector or skill no longer exists in the workspace are
// skipped: a skill deleted without the binding sweep (a source sync, or an
// older binary during a rolling deploy) must not keep an id the admin can
// neither see nor save back, and the next ReplaceOffers drops the stale row.
func ListOffers(ctx context.Context, db DBTX, workspaceID, agentID string) (Offers, error) {
	offers := Offers{ConnectorIDs: []string{}, SkillIDs: []string{}}
	rows, err := db.Query(ctx, `SELECT b.resource_type, b.resource_id::text
		FROM context_capability_binding b
		WHERE b.workspace_id = $1::uuid AND b.agent_id = $2::uuid AND b.scope_type = 'offer' AND b.enabled
		  AND ((b.resource_type = 'connector' AND EXISTS (
		          SELECT 1 FROM internal_connector c WHERE c.id = b.resource_id AND c.workspace_id = b.workspace_id))
		    OR (b.resource_type = 'skill' AND EXISTS (
		          SELECT 1 FROM skill s WHERE s.id = b.resource_id AND s.workspace_id = b.workspace_id)))
		ORDER BY b.created_at, b.resource_id`, workspaceID, agentID)
	if err != nil {
		return offers, err
	}
	defer rows.Close()
	for rows.Next() {
		var resourceType, resourceID string
		if err := rows.Scan(&resourceType, &resourceID); err != nil {
			return offers, err
		}
		switch resourceType {
		case ResourceConnector:
			offers.ConnectorIDs = append(offers.ConnectorIDs, resourceID)
		case ResourceSkill:
			offers.SkillIDs = append(offers.SkillIDs, resourceID)
		}
	}
	return offers, rows.Err()
}

// IsOffered reports whether resourceID of resourceType is in the agent's
// enabled offer catalog.
func IsOffered(ctx context.Context, db DBTX, workspaceID, agentID, resourceType, resourceID string) (bool, error) {
	if resourceType != ResourceConnector && resourceType != ResourceSkill {
		return false, ErrInvalidInput
	}
	id, err := canonicalUUID(resourceID)
	if err != nil {
		return false, err
	}
	var offered bool
	err = db.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM context_capability_binding
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'offer'
		  AND org_id = '' AND scope_key = '' AND resource_type = $3 AND resource_id = $4::uuid AND enabled)`,
		workspaceID, agentID, resourceType, id).Scan(&offered)
	return offered, err
}

// LockOffers serializes offer catalog changes of one agent until the
// surrounding transaction ends. ReplaceOffers takes it itself; a caller that
// must read the current catalog before replacing it takes it first (advisory
// locks are reentrant within a session).
func LockOffers(ctx context.Context, tx DBTX, agentID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('context_capability_offers:' || $1::text, 0))`, agentID)
	return err
}

// ReplaceOffers replaces the agent's offer catalog with exactly connectorIDs
// and skillIDs. Run it inside a transaction. It verifies that the agent and
// every id belong to the workspace (ErrNotFound / ErrUnknownResource) and
// serializes concurrent replacements for the same agent. Removing an offer
// leaves scene/person binding rows in place; TaskBindings ignores them until
// the resource is offered again. actorID may be empty.
func ReplaceOffers(ctx context.Context, tx DBTX, workspaceID, agentID string, connectorIDs, skillIDs []string, actorID string) error {
	connectors, err := canonicalUUIDSet(connectorIDs)
	if err != nil {
		return err
	}
	skills, err := canonicalUUIDSet(skillIDs)
	if err != nil {
		return err
	}
	actor, err := optionalUUID(actorID)
	if err != nil {
		return err
	}
	if err := LockOffers(ctx, tx, agentID); err != nil {
		return err
	}
	var agentExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent WHERE id = $1::uuid AND workspace_id = $2::uuid)`, agentID, workspaceID).Scan(&agentExists); err != nil {
		return err
	}
	if !agentExists {
		return ErrNotFound
	}
	for _, check := range []struct {
		query string
		ids   []string
	}{
		{`SELECT count(*) FROM internal_connector WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])`, connectors},
		{`SELECT count(*) FROM skill WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])`, skills},
	} {
		if len(check.ids) == 0 {
			continue
		}
		var found int
		if err := tx.QueryRow(ctx, check.query, workspaceID, check.ids).Scan(&found); err != nil {
			return err
		}
		if found != len(check.ids) {
			return ErrUnknownResource
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM context_capability_binding
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'offer'
		  AND NOT ((resource_type = 'connector' AND resource_id = ANY($3::uuid[]))
		        OR (resource_type = 'skill' AND resource_id = ANY($4::uuid[])))`,
		workspaceID, agentID, connectors, skills); err != nil {
		return err
	}
	for _, group := range []struct {
		resourceType string
		ids          []string
	}{{ResourceConnector, connectors}, {ResourceSkill, skills}} {
		if len(group.ids) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO context_capability_binding
			(workspace_id, agent_id, scope_type, org_id, scope_key, resource_type, resource_id, enabled, created_by, updated_by)
			SELECT $1::uuid, $2::uuid, 'offer', '', '', $3::text, resource_id, TRUE, $5::uuid, $5::uuid
			FROM unnest($4::uuid[]) AS resource_id
			ON CONFLICT (agent_id, scope_type, org_id, scope_key, resource_type, resource_id)
			DO UPDATE SET enabled = TRUE, updated_by = EXCLUDED.updated_by, updated_at = now()
			WHERE context_capability_binding.workspace_id = EXCLUDED.workspace_id
			  AND NOT context_capability_binding.enabled`,
			workspaceID, agentID, group.resourceType, group.ids, actor); err != nil {
			return err
		}
	}
	return nil
}

// TaskBindings returns the effective scene and personal bindings of one task:
// enabled rows under scope.OrgID whose scene key equals scope.SceneKey (when
// HasScene) or whose person key equals scope.PersonKey (when HasPerson), and
// whose resource is still in the agent's enabled offer catalog. A scope with
// neither layer returns nil without querying.
func TaskBindings(ctx context.Context, db DBTX, workspaceID, agentID string, scope Scope) ([]Binding, error) {
	if !scope.HasScene() && !scope.HasPerson() {
		return nil, nil
	}
	rows, err := db.Query(ctx, `SELECT b.scope_type, b.org_id, b.scope_key, b.scope_title, b.resource_type, b.resource_id::text, b.enabled
		FROM context_capability_binding b
		WHERE b.workspace_id = $1::uuid AND b.agent_id = $2::uuid AND b.enabled AND b.org_id = $3
		  AND ((b.scope_type = 'scene' AND $4::text <> '' AND b.scope_key = $4::text)
		    OR (b.scope_type = 'person' AND $5::text <> '' AND b.scope_key = $5::text))
		  AND EXISTS (
		    SELECT 1 FROM context_capability_binding o
		    WHERE o.workspace_id = b.workspace_id AND o.agent_id = b.agent_id AND o.scope_type = 'offer'
		      AND o.org_id = '' AND o.scope_key = '' AND o.resource_type = b.resource_type
		      AND o.resource_id = b.resource_id AND o.enabled)
		ORDER BY b.resource_type, b.resource_id, b.scope_type`,
		workspaceID, agentID, scope.OrgID, scope.SceneKey, scope.PersonKey)
	if err != nil {
		return nil, err
	}
	return collectBindings(rows)
}

// ListScopeBindings returns every binding row (enabled or not) of one scene or
// person scope. Rows are not filtered by the offer catalog; callers that
// render them should cross-check ListOffers.
func ListScopeBindings(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string) ([]Binding, error) {
	if scopeType != ScopeScene && scopeType != ScopePerson {
		return nil, ErrInvalidInput
	}
	rows, err := db.Query(ctx, `SELECT scope_type, org_id, scope_key, scope_title, resource_type, resource_id::text, enabled
		FROM context_capability_binding
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = $3 AND org_id = $4 AND scope_key = $5
		ORDER BY resource_type, created_at, resource_id`,
		workspaceID, agentID, scopeType, orgID, scopeKey)
	if err != nil {
		return nil, err
	}
	return collectBindings(rows)
}

// BindingWrite is the input of UpsertBinding. ActorID may be empty.
type BindingWrite struct {
	WorkspaceID  string
	AgentID      string
	ScopeType    string
	OrgID        string
	ScopeKey     string
	ScopeTitle   string
	ResourceType string
	ResourceID   string
	Enabled      bool
	ActorID      string
}

// UpsertBinding creates or updates one scene or person binding. Enabling a
// resource that is not in the agent's enabled offer catalog returns
// ErrNotOffered (checked atomically with the write); disabling is always
// allowed. A non-empty ScopeTitle refreshes the stored title snapshot.
func UpsertBinding(ctx context.Context, db DBTX, in BindingWrite) (Binding, error) {
	if (in.ScopeType != ScopeScene && in.ScopeType != ScopePerson) || !ValidScopeKey(in.ScopeType, in.ScopeKey) ||
		(in.ResourceType != ResourceConnector && in.ResourceType != ResourceSkill) {
		return Binding{}, ErrInvalidInput
	}
	resourceID, err := canonicalUUID(in.ResourceID)
	if err != nil {
		return Binding{}, err
	}
	actor, err := optionalUUID(in.ActorID)
	if err != nil {
		return Binding{}, err
	}
	var out Binding
	err = db.QueryRow(ctx, `INSERT INTO context_capability_binding
		(workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, resource_type, resource_id, enabled, created_by, updated_by)
		SELECT $1::uuid, $2::uuid, $3::text, $4::text, $5::text, $6::text, $7::text, $8::uuid, $9::boolean, $10::uuid, $10::uuid
		WHERE NOT $9::boolean OR EXISTS (
		  SELECT 1 FROM context_capability_binding o
		  WHERE o.workspace_id = $1::uuid AND o.agent_id = $2::uuid AND o.scope_type = 'offer'
		    AND o.org_id = '' AND o.scope_key = '' AND o.resource_type = $7::text AND o.resource_id = $8::uuid AND o.enabled)
		ON CONFLICT (agent_id, scope_type, org_id, scope_key, resource_type, resource_id)
		DO UPDATE SET enabled = EXCLUDED.enabled,
		  scope_title = CASE WHEN EXCLUDED.scope_title <> '' THEN EXCLUDED.scope_title ELSE context_capability_binding.scope_title END,
		  updated_by = EXCLUDED.updated_by,
		  updated_at = now()
		WHERE context_capability_binding.workspace_id = EXCLUDED.workspace_id
		RETURNING scope_type, org_id, scope_key, scope_title, resource_type, resource_id::text, enabled`,
		in.WorkspaceID, in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey, in.ScopeTitle, in.ResourceType, resourceID, in.Enabled, actor,
	).Scan(&out.ScopeType, &out.OrgID, &out.ScopeKey, &out.ScopeTitle, &out.ResourceType, &out.ResourceID, &out.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.Enabled {
			return Binding{}, ErrNotOffered
		}
		return Binding{}, ErrNotFound
	}
	return out, err
}

// ScopeSummary aggregates one scene or person scope for the admin view.
type ScopeSummary struct {
	ScopeType       string
	OrgID           string
	ScopeKey        string
	ScopeTitle      string
	Bindings        []Binding
	CredentialCount int
}

// ListScopeSummaries returns every scene and person scope of the agent that
// has a binding or a credential, with its bindings and credential count.
// Titles come from the newest binding snapshot, falling back to grants.
func ListScopeSummaries(ctx context.Context, db DBTX, workspaceID, agentID string) ([]ScopeSummary, error) {
	type scopeID struct{ scopeType, orgID, key string }
	byScope := map[scopeID]*ScopeSummary{}
	get := func(id scopeID) *ScopeSummary {
		summary, ok := byScope[id]
		if !ok {
			summary = &ScopeSummary{ScopeType: id.scopeType, OrgID: id.orgID, ScopeKey: id.key, Bindings: []Binding{}}
			byScope[id] = summary
		}
		return summary
	}

	rows, err := db.Query(ctx, `SELECT scope_type, org_id, scope_key, scope_title, resource_type, resource_id::text, enabled
		FROM context_capability_binding
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type IN ('scene', 'person')
		ORDER BY updated_at DESC, resource_type, resource_id`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	bindings, err := collectBindings(rows)
	if err != nil {
		return nil, err
	}
	for _, binding := range bindings {
		summary := get(scopeID{binding.ScopeType, binding.OrgID, binding.ScopeKey})
		if summary.ScopeTitle == "" {
			summary.ScopeTitle = binding.ScopeTitle
		}
		summary.Bindings = append(summary.Bindings, binding)
	}

	rows, err = db.Query(ctx, `SELECT scope_type, org_id, scope_key, count(*)
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid
		GROUP BY scope_type, org_id, scope_key`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id scopeID
		var count int
		if err := rows.Scan(&id.scopeType, &id.orgID, &id.key, &count); err != nil {
			rows.Close()
			return nil, err
		}
		get(id).CredentialCount = count
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT DISTINCT ON (scope_type, org_id, scope_key) scope_type, org_id, scope_key, scope_title
		FROM context_config_grant
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_title <> ''
		ORDER BY scope_type, org_id, scope_key, updated_at DESC`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id scopeID
		var title string
		if err := rows.Scan(&id.scopeType, &id.orgID, &id.key, &title); err != nil {
			rows.Close()
			return nil, err
		}
		if summary, ok := byScope[id]; ok && summary.ScopeTitle == "" {
			summary.ScopeTitle = title
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]ScopeSummary, 0, len(byScope))
	for _, summary := range byScope {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScopeType != out[j].ScopeType {
			return out[i].ScopeType < out[j].ScopeType
		}
		if out[i].OrgID != out[j].OrgID {
			return out[i].OrgID < out[j].OrgID
		}
		return out[i].ScopeKey < out[j].ScopeKey
	})
	return out, nil
}

// Credential is one row of context_connector_credential. Ciphertext is nil in
// listing results; only GetCredential and TaskCredentials load it.
type Credential struct {
	CredentialBinding
	Ciphertext []byte
	Hint       string
	UpdatedAt  time.Time
}

// GetCredential loads one scene or person credential, or ErrNotFound.
func GetCredential(ctx context.Context, db DBTX, key CredentialBinding) (Credential, error) {
	key, err := key.normalized()
	if err != nil {
		return Credential{}, ErrInvalidInput
	}
	out := Credential{}
	err = db.QueryRow(ctx, `SELECT workspace_id::text, agent_id::text, connector_id::text, scope_type, org_id, scope_key, ciphertext, hint, updated_at
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND connector_id = $3::uuid
		  AND scope_type = $4 AND org_id = $5 AND scope_key = $6`,
		key.WorkspaceID, key.AgentID, key.ConnectorID, key.ScopeType, key.OrgID, key.ScopeKey,
	).Scan(&out.WorkspaceID, &out.AgentID, &out.ConnectorID, &out.ScopeType, &out.OrgID, &out.ScopeKey, &out.Ciphertext, &out.Hint, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return out, err
}

// UpsertCredential stores a sealed credential (see SealCredential) and its
// display hint for key. It never receives or stores the plaintext Bearer.
// actorID may be empty. The returned Credential carries no ciphertext.
func UpsertCredential(ctx context.Context, db DBTX, key CredentialBinding, ciphertext []byte, hint, actorID string) (Credential, error) {
	normalized, err := key.normalized()
	if err != nil {
		return Credential{}, ErrInvalidInput
	}
	if !ValidScopeKey(normalized.ScopeType, normalized.ScopeKey) || len(ciphertext) == 0 {
		return Credential{}, ErrInvalidInput
	}
	actor, err := optionalUUID(actorID)
	if err != nil {
		return Credential{}, err
	}
	out := Credential{CredentialBinding: normalized}
	err = db.QueryRow(ctx, `INSERT INTO context_connector_credential
		(workspace_id, agent_id, connector_id, scope_type, org_id, scope_key, ciphertext, hint, updated_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9::uuid)
		ON CONFLICT (agent_id, connector_id, scope_type, org_id, scope_key)
		DO UPDATE SET ciphertext = EXCLUDED.ciphertext, hint = EXCLUDED.hint, updated_by = EXCLUDED.updated_by, updated_at = now()
		WHERE context_connector_credential.workspace_id = EXCLUDED.workspace_id
		RETURNING hint, updated_at`,
		normalized.WorkspaceID, normalized.AgentID, normalized.ConnectorID, normalized.ScopeType, normalized.OrgID, normalized.ScopeKey,
		ciphertext, hint, actor,
	).Scan(&out.Hint, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return out, err
}

// DeleteCredential removes one scene or person credential and reports
// whether a row existed.
func DeleteCredential(ctx context.Context, db DBTX, key CredentialBinding) (bool, error) {
	key, err := key.normalized()
	if err != nil {
		return false, ErrInvalidInput
	}
	tag, err := db.Exec(ctx, `DELETE FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND connector_id = $3::uuid
		  AND scope_type = $4 AND org_id = $5 AND scope_key = $6`,
		key.WorkspaceID, key.AgentID, key.ConnectorID, key.ScopeType, key.OrgID, key.ScopeKey)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListScopeCredentials returns the credential hints of one scene or person
// scope, without ciphertext.
func ListScopeCredentials(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string) ([]Credential, error) {
	rows, err := db.Query(ctx, `SELECT workspace_id::text, agent_id::text, connector_id::text, scope_type, org_id, scope_key, hint, updated_at
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = $3 AND org_id = $4 AND scope_key = $5
		ORDER BY connector_id`, workspaceID, agentID, scopeType, orgID, scopeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.WorkspaceID, &c.AgentID, &c.ConnectorID, &c.ScopeType, &c.OrgID, &c.ScopeKey, &c.Hint, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TaskCredentials returns, with ciphertext, every scene credential of
// scope.SceneKey and person credential of scope.PersonKey under scope.OrgID.
// A scope with neither layer returns nil without querying. Callers open each
// with OpenCredential and apply person > scene > workspace precedence.
func TaskCredentials(ctx context.Context, db DBTX, workspaceID, agentID string, scope Scope) ([]Credential, error) {
	if !scope.HasScene() && !scope.HasPerson() {
		return nil, nil
	}
	rows, err := db.Query(ctx, `SELECT workspace_id::text, agent_id::text, connector_id::text, scope_type, org_id, scope_key, ciphertext, hint, updated_at
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND org_id = $3
		  AND ((scope_type = 'scene' AND $4::text <> '' AND scope_key = $4::text)
		    OR (scope_type = 'person' AND $5::text <> '' AND scope_key = $5::text))
		ORDER BY connector_id, scope_type`,
		workspaceID, agentID, scope.OrgID, scope.SceneKey, scope.PersonKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.WorkspaceID, &c.AgentID, &c.ConnectorID, &c.ScopeType, &c.OrgID, &c.ScopeKey, &c.Ciphertext, &c.Hint, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Grant is one row of context_config_grant.
type Grant struct {
	UserID      string
	WorkspaceID string
	AgentID     string
	ScopeType   string
	OrgID       string
	ScopeKey    string
	ScopeTitle  string
	Source      string
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

const grantColumns = `user_id::text, workspace_id::text, agent_id::text, scope_type, org_id, scope_key, scope_title, source, expires_at, updated_at`

func scanGrant(row pgx.Row) (Grant, error) {
	var g Grant
	err := row.Scan(&g.UserID, &g.WorkspaceID, &g.AgentID, &g.ScopeType, &g.OrgID, &g.ScopeKey, &g.ScopeTitle, &g.Source, &g.ExpiresAt, &g.UpdatedAt)
	return g, err
}

// UpsertGrant grants g.UserID authority over one scene or person scope for
// ttl from now. A renewal never shortens an existing expiry. g.ExpiresAt and
// g.UpdatedAt are ignored; the stored row is returned.
func UpsertGrant(ctx context.Context, db DBTX, g Grant, ttl time.Duration) (Grant, error) {
	if (g.ScopeType != ScopeScene && g.ScopeType != ScopePerson) || !ValidScopeKey(g.ScopeType, g.ScopeKey) ||
		(g.Source != GrantSourceAgentLink && g.Source != GrantSourceJSAPI) || ttl <= 0 {
		return Grant{}, ErrInvalidInput
	}
	for _, id := range []string{g.UserID, g.WorkspaceID, g.AgentID} {
		if _, err := canonicalUUID(id); err != nil {
			return Grant{}, err
		}
	}
	out, err := scanGrant(db.QueryRow(ctx, `INSERT INTO context_config_grant
		(user_id, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, source, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, now() + make_interval(secs => $9::double precision))
		ON CONFLICT (user_id, agent_id, scope_type, org_id, scope_key)
		DO UPDATE SET
		  scope_title = CASE WHEN EXCLUDED.scope_title <> '' THEN EXCLUDED.scope_title ELSE context_config_grant.scope_title END,
		  source = EXCLUDED.source,
		  expires_at = GREATEST(context_config_grant.expires_at, EXCLUDED.expires_at),
		  updated_at = now()
		WHERE context_config_grant.workspace_id = EXCLUDED.workspace_id
		RETURNING `+grantColumns,
		g.UserID, g.WorkspaceID, g.AgentID, g.ScopeType, g.OrgID, g.ScopeKey, g.ScopeTitle, g.Source, ttl.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	return out, err
}

// GetLiveGrant returns the caller's unexpired grant for one scope, or
// ErrNotFound.
func GetLiveGrant(ctx context.Context, db DBTX, userID, agentID, scopeType, orgID, scopeKey string) (Grant, error) {
	out, err := scanGrant(db.QueryRow(ctx, `SELECT `+grantColumns+`
		FROM context_config_grant
		WHERE user_id = $1::uuid AND agent_id = $2::uuid AND scope_type = $3 AND org_id = $4 AND scope_key = $5
		  AND expires_at > now()`, userID, agentID, scopeType, orgID, scopeKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrNotFound
	}
	return out, err
}

// PersonHeldByOther reports whether a user other than userID holds a live
// person grant for (agent, org, staffId). A personal link proves only that it
// was delivered in the person's 1:1 chat; once that person has redeemed one,
// a forwarded or leaked link must not hand their scope to someone else. The
// check takes a transaction-scoped advisory lock on the scope, so concurrent
// redemptions of two links for the same person serialize; tx must be a
// transaction that also writes the grant.
func PersonHeldByOther(ctx context.Context, tx DBTX, userID, agentID, orgID, staffID string) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('context_config_person:' || $1 || ':' || $2 || ':' || $3, 0))`,
		agentID, orgID, staffID); err != nil {
		return false, err
	}
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM context_config_grant
		WHERE agent_id = $1::uuid AND scope_type = $2 AND org_id = $3 AND scope_key = $4
		  AND user_id <> $5::uuid AND expires_at > now())`,
		agentID, ScopePerson, orgID, staffID, userID).Scan(&held)
	return held, err
}

// ListLiveGrantsForUser returns the user's unexpired grants, newest first.
// An empty agentID lists grants for every agent.
func ListLiveGrantsForUser(ctx context.Context, db DBTX, userID, agentID string) ([]Grant, error) {
	rows, err := db.Query(ctx, `SELECT `+grantColumns+`
		FROM context_config_grant
		WHERE user_id = $1::uuid AND ($2::text = '' OR agent_id = NULLIF($2::text, '')::uuid) AND expires_at > now()
		ORDER BY updated_at DESC, agent_id, scope_type, scope_key`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Link is one row of context_config_link. TokenHash is HashLinkToken(token);
// the token itself is never stored.
type Link struct {
	TokenHash    string
	WorkspaceID  string
	AgentID      string
	ScopeType    string
	OrgID        string
	ScopeKey     string
	ScopeTitle   string
	SourceTaskID string
	ExpiresAt    time.Time
}

const linkColumns = `token_hash, workspace_id::text, agent_id::text, scope_type, org_id, scope_key, scope_title, COALESCE(source_task_id::text, ''), expires_at`

func scanLink(row pgx.Row) (Link, error) {
	var l Link
	err := row.Scan(&l.TokenHash, &l.WorkspaceID, &l.AgentID, &l.ScopeType, &l.OrgID, &l.ScopeKey, &l.ScopeTitle, &l.SourceTaskID, &l.ExpiresAt)
	return l, err
}

// InsertLink stores a configuration link that expires ttl from now (use
// LinkTTL(l.ScopeType)). l.ExpiresAt is ignored; the stored row is returned.
func InsertLink(ctx context.Context, db DBTX, l Link, ttl time.Duration) (Link, error) {
	if (l.ScopeType != ScopeScene && l.ScopeType != ScopePerson) || !ValidScopeKey(l.ScopeType, l.ScopeKey) ||
		len(l.TokenHash) != 64 || ttl <= 0 {
		return Link{}, ErrInvalidInput
	}
	sourceTask, err := optionalUUID(l.SourceTaskID)
	if err != nil {
		return Link{}, err
	}
	return scanLink(db.QueryRow(ctx, `INSERT INTO context_config_link
		(token_hash, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, source_task_id, expires_at)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::uuid, now() + make_interval(secs => $9::double precision))
		RETURNING `+linkColumns,
		l.TokenHash, l.WorkspaceID, l.AgentID, l.ScopeType, l.OrgID, l.ScopeKey, l.ScopeTitle, sourceTask, ttl.Seconds()))
}

// RedeemLink atomically redeems a link by token hash. Scene links stay
// reusable until they expire; person links are consumed by the first
// redemption (recording userID). Unknown, expired or consumed links return
// ErrNotFound. The caller then upserts the grant.
func RedeemLink(ctx context.Context, db DBTX, tokenHash, userID string) (Link, error) {
	user, err := optionalUUID(userID)
	if err != nil {
		return Link{}, err
	}
	out, err := scanLink(db.QueryRow(ctx, `UPDATE context_config_link
		SET consumed_at = CASE WHEN scope_type = 'person' THEN now() ELSE consumed_at END,
		    consumed_by = CASE WHEN scope_type = 'person' THEN $2::uuid ELSE consumed_by END
		WHERE token_hash = $1 AND expires_at > now() AND (scope_type = 'scene' OR consumed_at IS NULL)
		RETURNING `+linkColumns, tokenHash, user))
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, ErrNotFound
	}
	return out, err
}

func collectBindings(rows pgx.Rows) ([]Binding, error) {
	defer rows.Close()
	out := []Binding{}
	for rows.Next() {
		var b Binding
		if err := rows.Scan(&b.ScopeType, &b.OrgID, &b.ScopeKey, &b.ScopeTitle, &b.ResourceType, &b.ResourceID, &b.Enabled); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func canonicalUUID(raw string) (string, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed == uuid.Nil {
		return "", fmt.Errorf("%w: invalid id", ErrInvalidInput)
	}
	return parsed.String(), nil
}

// optionalUUID maps "" to SQL NULL and validates anything else.
func optionalUUID(raw string) (any, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := canonicalUUID(raw)
	if err != nil {
		return nil, err
	}
	return id, nil
}

// canonicalUUIDSet validates, canonicalizes and deduplicates ids. It always
// returns a non-nil slice so SQL = ANY($n) never sees NULL.
func canonicalUUIDSet(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id, err := canonicalUUID(raw)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}
