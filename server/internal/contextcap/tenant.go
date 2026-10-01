package contextcap

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Tenant sources. A created tenant is an agent_tenant row; the agent's
// DingTalk identity org is a tenant without one (a row for it only renames
// it).
const (
	TenantSourceCreated  = "created"
	TenantSourceIdentity = "identity"
)

// MaxTenantName is the tenant name limit in characters (code points).
const MaxTenantName = 64

var (
	// ErrTenantExists means the org is already a tenant of the agent (a row,
	// or the agent's identity org).
	ErrTenantExists = errors.New("contextcap: tenant already exists")
	// ErrIdentityTenant means the operation is not allowed on the agent's
	// identity tenant (it cannot be deleted).
	ErrIdentityTenant = errors.New("contextcap: the identity tenant cannot be deleted")
)

var tenantOrgIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidTenantOrgID accepts the DingTalk OrgId of a new tenant: 1..64
// characters of [A-Za-z0-9_-] (the agent_tenant check constraint).
func ValidTenantOrgID(orgID string) bool {
	return tenantOrgIDPattern.MatchString(orgID)
}

// NormalizeTenantName trims name and reports whether it is a valid tenant
// name: 1..MaxTenantName characters of valid UTF-8 without control
// characters.
func NormalizeTenantName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxTenantName {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

// Tenant is one enterprise of an agent (docs/context-capabilities.md §1.3).
type Tenant struct {
	OrgID string
	// Name is the row's name, else (identity tenant) the identity's
	// organization name, else the org id.
	Name string
	// Source is TenantSourceIdentity for the agent's identity org (with or
	// without a row) and TenantSourceCreated otherwise.
	Source string
	// HasRow reports a stored agent_tenant row.
	HasRow    bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AgentIdentityOrg returns the agent's DingTalk identity org and its
// organization name, both "" when the agent has no identity.
func AgentIdentityOrg(ctx context.Context, db DBTX, workspaceID, agentID string) (string, string, error) {
	var orgID, orgName string
	err := db.QueryRow(ctx, `SELECT BTRIM(org_id), BTRIM(organization_name) FROM agent_dingtalk_identity
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid`, workspaceID, agentID).Scan(&orgID, &orgName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return orgID, orgName, err
}

// AgentTenants is the only definition of "the agent's tenants": the
// agent's DingTalk identity org (source identity; named by its row, else
// its organization name, else its id) followed by the created tenants
// (agent_tenant rows), ordered by name. An agent without a DingTalk
// identity has only created tenants.
func AgentTenants(ctx context.Context, db DBTX, workspaceID, agentID string) ([]Tenant, error) {
	identityOrg, identityName, err := AgentIdentityOrg(ctx, db, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT org_id, name, COALESCE(created_by::text, ''), created_at, updated_at
		FROM agent_tenant WHERE workspace_id = $1::uuid AND agent_id = $2::uuid`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identity *Tenant
	if identityOrg != "" {
		identity = &Tenant{OrgID: identityOrg, Name: firstNonEmpty(identityName, identityOrg), Source: TenantSourceIdentity}
	}
	created := []Tenant{}
	for rows.Next() {
		t := Tenant{Source: TenantSourceCreated, HasRow: true}
		if err := rows.Scan(&t.OrgID, &t.Name, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if identity != nil && t.OrgID == identity.OrgID {
			t.Source = TenantSourceIdentity
			identity = &t
			continue
		}
		created = append(created, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(created, func(i, j int) bool {
		if a, b := strings.ToLower(created[i].Name), strings.ToLower(created[j].Name); a != b {
			return a < b
		}
		return created[i].OrgID < created[j].OrgID
	})
	out := make([]Tenant, 0, len(created)+1)
	if identity != nil {
		out = append(out, *identity)
	}
	return append(out, created...), nil
}

// FindTenant returns the tenant of orgID in tenants.
func FindTenant(tenants []Tenant, orgID string) (Tenant, bool) {
	for _, tenant := range tenants {
		if tenant.OrgID == orgID {
			return tenant, true
		}
	}
	return Tenant{}, false
}

// AgentTenant returns one tenant of the agent (AgentTenants), or
// ErrNotFound when orgID is not one.
func AgentTenant(ctx context.Context, db DBTX, workspaceID, agentID, orgID string) (Tenant, error) {
	tenants, err := AgentTenants(ctx, db, workspaceID, agentID)
	if err != nil {
		return Tenant{}, err
	}
	tenant, ok := FindTenant(tenants, orgID)
	if !ok {
		return Tenant{}, ErrNotFound
	}
	return tenant, nil
}

// TenantWrite is the input of CreateTenant and RenameTenant. ActorID may be
// empty.
type TenantWrite struct {
	WorkspaceID string
	AgentID     string
	OrgID       string
	Name        string
	ActorID     string
}

// CreateTenant creates a tenant for an org the agent does not serve yet.
// The org id must pass ValidTenantOrgID and the name NormalizeTenantName
// (ErrInvalidInput); an existing tenant (a row, or the identity org) is
// ErrTenantExists.
func CreateTenant(ctx context.Context, db DBTX, in TenantWrite) (Tenant, error) {
	name, ok := NormalizeTenantName(in.Name)
	if !ok || !ValidTenantOrgID(in.OrgID) {
		return Tenant{}, ErrInvalidInput
	}
	for _, id := range []string{in.WorkspaceID, in.AgentID} {
		if _, err := canonicalUUID(id); err != nil {
			return Tenant{}, err
		}
	}
	actor, err := optionalUUID(in.ActorID)
	if err != nil {
		return Tenant{}, err
	}
	identityOrg, _, err := AgentIdentityOrg(ctx, db, in.WorkspaceID, in.AgentID)
	if err != nil {
		return Tenant{}, err
	}
	if identityOrg == in.OrgID {
		return Tenant{}, ErrTenantExists
	}
	out := Tenant{OrgID: in.OrgID, Name: name, Source: TenantSourceCreated, HasRow: true}
	err = db.QueryRow(ctx, `INSERT INTO agent_tenant (workspace_id, agent_id, org_id, name, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid)
		ON CONFLICT (agent_id, org_id) DO NOTHING
		RETURNING COALESCE(created_by::text, ''), created_at, updated_at`,
		in.WorkspaceID, in.AgentID, in.OrgID, name, actor).Scan(&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrTenantExists
	}
	return out, err
}

// RenameTenant renames one tenant of the agent. Renaming the identity
// tenant stores a row for it (its org id must then pass ValidTenantOrgID,
// else ErrInvalidInput). ErrNotFound when orgID is not a tenant.
func RenameTenant(ctx context.Context, db DBTX, in TenantWrite) (Tenant, error) {
	name, ok := NormalizeTenantName(in.Name)
	if !ok {
		return Tenant{}, ErrInvalidInput
	}
	for _, id := range []string{in.WorkspaceID, in.AgentID} {
		if _, err := canonicalUUID(id); err != nil {
			return Tenant{}, err
		}
	}
	identityOrg, _, err := AgentIdentityOrg(ctx, db, in.WorkspaceID, in.AgentID)
	if err != nil {
		return Tenant{}, err
	}
	out := Tenant{OrgID: in.OrgID, Name: name, Source: TenantSourceCreated, HasRow: true}
	if identityOrg != "" && in.OrgID == identityOrg {
		if !ValidTenantOrgID(in.OrgID) {
			return Tenant{}, ErrInvalidInput
		}
		actor, err := optionalUUID(in.ActorID)
		if err != nil {
			return Tenant{}, err
		}
		out.Source = TenantSourceIdentity
		err = db.QueryRow(ctx, `INSERT INTO agent_tenant AS t (workspace_id, agent_id, org_id, name, created_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid)
			ON CONFLICT (agent_id, org_id) DO UPDATE SET name = EXCLUDED.name, updated_at = now()
			WHERE t.workspace_id = EXCLUDED.workspace_id
			RETURNING COALESCE(t.created_by::text, ''), t.created_at, t.updated_at`,
			in.WorkspaceID, in.AgentID, in.OrgID, name, actor).Scan(&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return Tenant{}, ErrNotFound
		}
		return out, err
	}
	err = db.QueryRow(ctx, `UPDATE agent_tenant SET name = $4, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND org_id = $3
		RETURNING COALESCE(created_by::text, ''), created_at, updated_at`,
		in.WorkspaceID, in.AgentID, in.OrgID, name).Scan(&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	return out, err
}

// DeleteTenant removes a created tenant and the org-scope configuration of
// that org (bindings, credentials, prompt components, custom MCP servers,
// pending OAuth connects). Run it inside a transaction so the tenant and
// its configuration go together. Group and person data of the org stays
// (the org then shows as unassigned). The identity tenant is
// ErrIdentityTenant; an org that is not a created tenant is ErrNotFound.
func DeleteTenant(ctx context.Context, tx DBTX, workspaceID, agentID, orgID string) error {
	for _, id := range []string{workspaceID, agentID} {
		if _, err := canonicalUUID(id); err != nil {
			return err
		}
	}
	identityOrg, _, err := AgentIdentityOrg(ctx, tx, workspaceID, agentID)
	if err != nil {
		return err
	}
	if identityOrg != "" && orgID == identityOrg {
		return ErrIdentityTenant
	}
	tag, err := tx.Exec(ctx, `DELETE FROM agent_tenant WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND org_id = $3`,
		workspaceID, agentID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	for _, statement := range []string{
		`DELETE FROM context_capability_binding WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'org' AND org_id = $3`,
		`DELETE FROM context_connector_credential WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'org' AND org_id = $3`,
		`DELETE FROM context_prompt_component WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'org' AND org_id = $3`,
		`DELETE FROM context_scope_mcp_config WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'org' AND org_id = $3`,
		`DELETE FROM connector_oauth_state WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'org' AND org_id = $3`,
	} {
		if _, err := tx.Exec(ctx, statement, workspaceID, agentID, orgID); err != nil {
			return err
		}
	}
	return nil
}

// jobOrgExpr is the org an inbound Coordinator job belongs to: the agent org
// it recorded (externalIdentity.dws.orgId), else the agent's identity org
// (bound as the named parameter), since jobs recorded before orgs were
// tracked were all dispatched under the identity.
func jobOrgExpr(identityParam string) string {
	return `COALESCE(NULLIF(BTRIM(job.command #>> '{externalIdentity,dws,orgId}'), ''), ` + identityParam + `::text)`
}

// OrgActivity counts the group scenes and the people of one org of an
// agent.
type OrgActivity struct {
	OrgID       string
	GroupCount  int
	PersonCount int
}

// ListAgentOrgActivity returns, for every non-empty org the agent's scene
// and person data mentions, how many group scenes and people it has. Data
// sources: inbound Coordinator jobs (jobs without a recorded org belong to
// identityOrgID), scene_memory, agent_scene_config, and every stored scope
// row (bindings, credentials, grants, prompt components, custom MCP
// servers). A scene counts as a group unless it is positively a 1:1 chat
// (the kind rule of ListAgentScenes); people are 1:1 chat senders and
// person scopes.
func ListAgentOrgActivity(ctx context.Context, db DBTX, workspaceID, agentID, identityOrgID string) (map[string]OrgActivity, error) {
	type sceneState struct {
		jobType   string
		hasJob    bool
		cfgKind   string
		hasConfig bool
	}
	scenes := map[string]map[string]*sceneState{}
	persons := map[string]map[string]bool{}
	scene := func(org, key string) *sceneState {
		if org == "" || !ValidOpenConversationID(key) {
			return nil
		}
		byKey, ok := scenes[org]
		if !ok {
			byKey = map[string]*sceneState{}
			scenes[org] = byKey
		}
		state, ok := byKey[key]
		if !ok {
			state = &sceneState{}
			byKey[key] = state
		}
		return state
	}
	person := func(org, staffID string) {
		if org == "" || !ValidStaffID(staffID) {
			return
		}
		if persons[org] == nil {
			persons[org] = map[string]bool{}
		}
		persons[org][staffID] = true
	}

	// The newest job of every conversation decides its kind; a 1:1 job with
	// a sender names a person.
	rows, err := db.Query(ctx, `SELECT DISTINCT ON (j.org, j.cid) j.org, j.cid, j.conversation_type, j.staff_id
		FROM (
		  SELECT `+jobOrgExpr("$3")+` AS org,
		    BTRIM(job.command #>> '{event,data,conversation,openConversationId}') AS cid,
		    BTRIM(COALESCE(job.command #>> '{event,data,conversation,type}', '')) AS conversation_type,
		    BTRIM(COALESCE(job.command #>> '{event,data,sender,staffId}', '')) AS staff_id,
		    job.created_at, job.id
		  FROM inbound_coordinator_job job
		  WHERE job.agent_id = $2::uuid AND job.workspace_id = $1::uuid
		    AND lower(COALESCE(NULLIF(BTRIM(job.command #>> '{source,platform}'), ''), 'dingtalk')) = 'dingtalk'
		    AND NULLIF(BTRIM(job.command #>> '{event,data,conversation,openConversationId}'), '') IS NOT NULL
		) j
		ORDER BY j.org, j.cid, j.created_at DESC, j.id DESC`, workspaceID, agentID, identityOrgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var org, cid, conversationType, staffID string
		if err := rows.Scan(&org, &cid, &conversationType, &staffID); err != nil {
			rows.Close()
			return nil, err
		}
		if state := scene(org, cid); state != nil {
			state.hasJob, state.jobType = true, conversationType
		}
		if IsDirectConversationType(conversationType) {
			person(org, staffID)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Every 1:1 sender, not only the newest job's.
	rows, err = db.Query(ctx, `SELECT DISTINCT `+jobOrgExpr("$3")+`, BTRIM(job.command #>> '{event,data,sender,staffId}')
		FROM inbound_coordinator_job job
		WHERE job.agent_id = $2::uuid AND job.workspace_id = $1::uuid
		  AND lower(COALESCE(NULLIF(BTRIM(job.command #>> '{source,platform}'), ''), 'dingtalk')) = 'dingtalk'
		  AND lower(BTRIM(COALESCE(job.command #>> '{event,data,conversation,type}', ''))) IN ('single', 'p2p', 'private', 'direct')
		  AND NULLIF(BTRIM(job.command #>> '{event,data,sender,staffId}'), '') IS NOT NULL`, workspaceID, agentID, identityOrgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var org, staffID string
		if err := rows.Scan(&org, &staffID); err != nil {
			rows.Close()
			return nil, err
		}
		person(org, staffID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT org_id, scene_key, scene_kind FROM agent_scene_config
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND platform = 'dingtalk'`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var org, key, kind string
		if err := rows.Scan(&org, &key, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		if state := scene(org, key); state != nil {
			state.hasConfig, state.cfgKind = true, kind
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT 'scene', org_id, scene_key FROM scene_memory
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND platform = 'dingtalk'
		UNION SELECT scope_type, org_id, scope_key FROM context_capability_binding
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type IN ('scene', 'person')
		UNION SELECT scope_type, org_id, scope_key FROM context_connector_credential
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type IN ('scene', 'person')
		UNION SELECT scope_type, org_id, scope_key FROM context_config_grant
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type IN ('scene', 'person')
		UNION SELECT scope_type, org_id, scope_key FROM context_prompt_component
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type IN ('scene', 'person')
		UNION SELECT scope_type, org_id, scope_key FROM context_scope_mcp_config
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type IN ('scene', 'person')`, workspaceID, agentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var scopeType, org, key string
		if err := rows.Scan(&scopeType, &org, &key); err != nil {
			rows.Close()
			return nil, err
		}
		if scopeType == ScopePerson {
			person(org, key)
		} else {
			scene(org, key)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]OrgActivity{}
	for org, byKey := range scenes {
		activity := OrgActivity{OrgID: org}
		for _, state := range byKey {
			kind := SceneKindGroup
			switch {
			case state.hasJob && state.jobType != "":
				kind = SceneKindForConversationType(state.jobType)
			case state.hasConfig:
				kind = state.cfgKind
			}
			if kind == SceneKindGroup {
				activity.GroupCount++
			}
		}
		out[org] = activity
	}
	for org, staff := range persons {
		activity := out[org]
		activity.OrgID = org
		activity.PersonCount = len(staff)
		out[org] = activity
	}
	return out, nil
}

// PersonSummary is one person known for an org of an agent.
type PersonSummary struct {
	StaffID string
	// Title is the newest 1:1 sender display name, else the person grant's
	// title, else the binding title snapshot.
	Title string
	// DMSceneKey is the person's 1:1 chat with the agent (the newest one
	// that names them, else the chat a personal link was redeemed from), ""
	// when unknown.
	DMSceneKey   string
	LastActiveAt time.Time
}

// ListOrgPersons returns the people known for orgID of the agent, newest
// activity first: 1:1 chat senders (the DirectScenePerson job source; jobs
// without a recorded org belong to identityOrgID), person grants and the 1:1
// chats of personal links, and every person scope with stored configuration
// (bindings, credentials, prompt components, custom MCP servers).
func ListOrgPersons(ctx context.Context, db DBTX, workspaceID, agentID, orgID, identityOrgID string) ([]PersonSummary, error) {
	if !ValidOrgID(orgID) {
		return nil, ErrInvalidInput
	}
	byStaff := map[string]*PersonSummary{}
	grantTitles := map[string]string{}
	bindingTitles := map[string]string{}
	get := func(staffID string) *PersonSummary {
		staffID = strings.TrimSpace(staffID)
		if !ValidStaffID(staffID) {
			return nil
		}
		p, ok := byStaff[staffID]
		if !ok {
			p = &PersonSummary{StaffID: staffID}
			byStaff[staffID] = p
		}
		return p
	}
	touch := func(p *PersonSummary, at time.Time) {
		if at.After(p.LastActiveAt) {
			p.LastActiveAt = at
		}
	}

	rows, err := db.Query(ctx, `SELECT DISTINCT ON (staff_id) staff_id, sender_name, cid, created_at
		FROM (
		  SELECT BTRIM(job.command #>> '{event,data,sender,staffId}') AS staff_id,
		    BTRIM(COALESCE(job.command #>> '{event,data,sender,displayName}', '')) AS sender_name,
		    BTRIM(job.command #>> '{event,data,conversation,openConversationId}') AS cid,
		    job.created_at, job.id
		  FROM inbound_coordinator_job job
		  WHERE job.agent_id = $2::uuid AND job.workspace_id = $1::uuid
		    AND lower(COALESCE(NULLIF(BTRIM(job.command #>> '{source,platform}'), ''), 'dingtalk')) = 'dingtalk'
		    AND lower(BTRIM(COALESCE(job.command #>> '{event,data,conversation,type}', ''))) IN ('single', 'p2p', 'private', 'direct')
		    AND NULLIF(BTRIM(job.command #>> '{event,data,sender,staffId}'), '') IS NOT NULL
		    AND `+jobOrgExpr("$4")+` = $3::text
		) j
		ORDER BY staff_id, created_at DESC, id DESC`, workspaceID, agentID, orgID, identityOrgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var staffID, senderName, cid string
		var at time.Time
		if err := rows.Scan(&staffID, &senderName, &cid, &at); err != nil {
			rows.Close()
			return nil, err
		}
		if p := get(staffID); p != nil {
			p.Title = senderName
			if ValidOpenConversationID(cid) {
				p.DMSceneKey = cid
			}
			touch(p, at)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT g.scope_key, g.scope_title, g.updated_at, COALESCE((
		    SELECT l.extra_scene_key FROM context_config_link l
		    WHERE l.workspace_id = g.workspace_id AND l.agent_id = g.agent_id AND l.org_id = g.org_id
		      AND l.scope_type = 'person' AND l.scope_key = g.scope_key AND l.extra_scene_key <> ''
		      AND l.consumed_by IS NOT NULL
		    ORDER BY l.created_at DESC LIMIT 1), '')
		FROM context_config_grant g
		WHERE g.workspace_id = $1::uuid AND g.agent_id = $2::uuid AND g.scope_type = 'person' AND g.org_id = $3::text
		ORDER BY g.updated_at`, workspaceID, agentID, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var staffID, title, dmKey string
		var at time.Time
		if err := rows.Scan(&staffID, &title, &at, &dmKey); err != nil {
			rows.Close()
			return nil, err
		}
		if p := get(staffID); p != nil {
			if title = strings.TrimSpace(title); title != "" {
				grantTitles[p.StaffID] = title
			}
			if p.DMSceneKey == "" && ValidOpenConversationID(dmKey) {
				p.DMSceneKey = dmKey
			}
			touch(p, at)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(ctx, `SELECT scope_key, COALESCE(max(scope_title) FILTER (WHERE scope_title <> ''), ''), max(updated_at)
		  FROM context_capability_binding
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'person' AND org_id = $3::text GROUP BY scope_key
		UNION ALL SELECT scope_key, '', max(updated_at) FROM context_connector_credential
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'person' AND org_id = $3::text GROUP BY scope_key
		UNION ALL SELECT scope_key, '', max(updated_at) FROM context_prompt_component
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'person' AND org_id = $3::text GROUP BY scope_key
		UNION ALL SELECT scope_key, '', max(updated_at) FROM context_scope_mcp_config
		  WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'person' AND org_id = $3::text GROUP BY scope_key`,
		workspaceID, agentID, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var staffID, title string
		var at time.Time
		if err := rows.Scan(&staffID, &title, &at); err != nil {
			rows.Close()
			return nil, err
		}
		if p := get(staffID); p != nil {
			if title = strings.TrimSpace(title); title != "" && bindingTitles[p.StaffID] == "" {
				bindingTitles[p.StaffID] = title
			}
			touch(p, at)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]PersonSummary, 0, len(byStaff))
	for _, p := range byStaff {
		p.Title = firstNonEmpty(p.Title, grantTitles[p.StaffID], bindingTitles[p.StaffID])
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActiveAt.Equal(out[j].LastActiveAt) {
			return out[i].LastActiveAt.After(out[j].LastActiveAt)
		}
		return out[i].StaffID < out[j].StaffID
	})
	return out, nil
}

// FindPerson returns the person of staffID in persons.
func FindPerson(persons []PersonSummary, staffID string) (PersonSummary, bool) {
	for _, p := range persons {
		if p.StaffID == staffID {
			return p, true
		}
	}
	return PersonSummary{}, false
}
