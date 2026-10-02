package tag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// snapshotSchema versions the snapshot JSON so an apply can tell which keys a
// revision was captured with. Keys missing from an older snapshot leave the
// employee's current value in place; keys no longer managed are ignored.
// Schema 2 dropped the tenant-owned columns (seededAgentColumns).
const snapshotSchema = 2

// columnKind says how a snapshot value is read back into an agent column.
type columnKind int

const (
	kindText columnKind = iota
	kindBool
	kindInt
	kindUUID
	kindJSONB
	kindTextArray
)

// managedAgentColumns are the agent columns of the Tag's shared configuration:
// what the template's views edit (instructions, MCP, runtime and model). They
// are versioned in each revision and overwritten on every apply. Skills,
// connectors, offers and DSH plugins travel in the snapshot alongside them.
var managedAgentColumns = []struct {
	name string
	kind columnKind
}{
	{"instructions", kindText},
	{"mcp_config", kindJSONB},
	{"model", kindText},
	{"thinking_level", kindText},
	{"service_tier", kindText},
	{"runtime_id", kindUUID},
	{"runtime_mode", kindText},
	{"runtime_config", kindJSONB},
	{"custom_env", kindJSONB},
	{"custom_args", kindJSONB},
	{"max_concurrent_tasks", kindInt},
	{"composio_toolkit_allowlist", kindTextArray},
	{"disabled_runtime_skills", kindJSONB},
}

// seededAgentColumns belong to each tenant's employee: its profile, how it
// answers in DingTalk, its scene memory and its dispatch prompt, all edited
// on the tenant's own views. A new employee starts from the template's
// values, after which applies never touch them. Identity and ownership
// columns (name, owner, visibility, permission mode, kind, system key,
// archive state) and per-binding state (DingTalk response policy revision)
// are neither managed nor seeded.
var seededAgentColumns = []string{
	"description",
	"avatar_url",
	"dispatch_always_new_issue",
	"dispatch_prompt_overrides",
	"coordinator_contract",
	"chat_session_resume",
	"inbound_coordinator",
	"persona",
	"reply_tone",
	"scene_memory_write_enabled",
	"scene_memory_recall_enabled",
	"scene_memory_ui_enabled",
	"scene_memory_bootstrap_enabled",
	"task_finished_loop_enabled",
	"dingtalk_show_ai_tag",
	"dingtalk_response_enabled",
	"inbound_coordinator_user_decision",
	"inbound_coordinator_user_decision_names",
	"inbound_coordinator_user_decision_audience",
}

// responsePolicyColumns are the seeded columns that make up the agent's
// DingTalk response policy (versioned by dingtalk_response_policy_revision).
var responsePolicyColumns = []string{
	"inbound_coordinator",
	"inbound_coordinator_user_decision",
	"inbound_coordinator_user_decision_names",
	"inbound_coordinator_user_decision_audience",
	"dingtalk_show_ai_tag",
	"dingtalk_response_enabled",
}

// captureAgentSQL builds the agent part of a snapshot as one jsonb object.
func captureAgentSQL() string {
	parts := make([]string, 0, len(managedAgentColumns))
	for _, c := range managedAgentColumns {
		parts = append(parts, fmt.Sprintf("'%s', to_jsonb(a.%s)", c.name, c.name))
	}
	return `SELECT jsonb_build_object(` + strings.Join(parts, ", ") + `)
		FROM agent a WHERE a.id = $2::uuid AND a.workspace_id = $1::uuid AND a.archived_at IS NULL`
}

// applyAgentSQL copies the agent part of a snapshot ($1) onto an employee.
func applyAgentSQL() string {
	sets := make([]string, 0, len(managedAgentColumns)+1)
	for _, c := range managedAgentColumns {
		value := fmt.Sprintf("($1::jsonb)->'%s'", c.name)
		text := fmt.Sprintf("($1::jsonb)->>'%s'", c.name)
		var expr string
		switch c.kind {
		case kindText:
			expr = text
		case kindBool:
			expr = "(" + text + ")::boolean"
		case kindInt:
			expr = "(" + text + ")::integer"
		case kindUUID:
			expr = "(" + text + ")::uuid"
		case kindJSONB:
			expr = fmt.Sprintf("NULLIF(%s, 'null'::jsonb)", value)
		case kindTextArray:
			expr = fmt.Sprintf("CASE WHEN jsonb_typeof(%s) = 'array' THEN ARRAY(SELECT jsonb_array_elements_text(%s)) ELSE NULL END", value, value)
		}
		sets = append(sets, fmt.Sprintf("%s = CASE WHEN ($1::jsonb) ? '%s' THEN %s ELSE %s END", c.name, c.name, expr, c.name))
	}
	sets = append(sets, "updated_at = now()")
	return `UPDATE agent SET ` + strings.Join(sets, ", ") + `
		WHERE id = $3::uuid AND workspace_id = $2::uuid AND archived_at IS NULL`
}

// SeedAgent copies the tenant-owned columns (seededAgentColumns) from one
// agent to another: the template's defaults for a new employee, or a source
// agent's for a new template.
func SeedAgent(ctx context.Context, tx DBTX, workspaceID, fromAgentID, toAgentID string) error {
	sets := make([]string, 0, len(seededAgentColumns)+1)
	for _, c := range seededAgentColumns {
		sets = append(sets, fmt.Sprintf("%s = s.%s", c, c))
	}
	// The DingTalk response policy is synced to the Router by revision: a
	// seed that changes it bumps the revision like UpdateAgentDingTalkResponsePolicy.
	policyChanged := make([]string, 0, len(responsePolicyColumns))
	for _, c := range responsePolicyColumns {
		policyChanged = append(policyChanged, fmt.Sprintf("t.%s IS DISTINCT FROM s.%s", c, c))
	}
	sets = append(sets, "dingtalk_response_policy_revision = t.dingtalk_response_policy_revision + CASE WHEN "+
		strings.Join(policyChanged, " OR ")+" THEN 1 ELSE 0 END")
	sets = append(sets, "updated_at = now()")
	updated, err := tx.Exec(ctx, `UPDATE agent t SET `+strings.Join(sets, ", ")+`
		FROM agent s
		WHERE s.id = $2::uuid AND s.workspace_id = $1::uuid AND s.archived_at IS NULL
		  AND t.id = $3::uuid AND t.workspace_id = $1::uuid AND t.archived_at IS NULL`,
		workspaceID, fromAgentID, toAgentID)
	if err != nil {
		return fmt.Errorf("tag: seed agent: %w", err)
	}
	if updated.RowsAffected() == 0 {
		return ErrAgentUnavailable
	}
	return nil
}

// CaptureSnapshot reads the template agent's shared configuration. The
// arrays are ordered so equal configurations produce equal JSON.
func CaptureSnapshot(ctx context.Context, db DBTX, workspaceID, templateAgentID string) (json.RawMessage, error) {
	var snapshot json.RawMessage
	err := db.QueryRow(ctx, `SELECT jsonb_build_object(
			'schema', $3::int,
			'agent', (`+captureAgentSQL()+`),
			'skills', COALESCE((SELECT jsonb_agg(jsonb_build_object('skill_id', s.skill_id, 'enabled', s.enabled) ORDER BY s.skill_id)
				FROM agent_skill s WHERE s.agent_id = $2::uuid), '[]'::jsonb),
			'connector_ids', COALESCE((SELECT jsonb_agg(g.connector_id ORDER BY g.connector_id)
				FROM internal_connector_agent g WHERE g.agent_id = $2::uuid AND g.workspace_id = $1::uuid), '[]'::jsonb),
			'offers', COALESCE((SELECT jsonb_agg(jsonb_build_object('resource_type', b.resource_type, 'resource_id', b.resource_id, 'enabled', b.enabled)
					ORDER BY b.resource_type, b.resource_id)
				FROM context_capability_binding b
				WHERE b.workspace_id = $1::uuid AND b.agent_id = $2::uuid AND b.scope_type = 'offer'), '[]'::jsonb),
			'dsh_plugins', COALESCE((SELECT jsonb_agg(jsonb_build_object('dsh_plugin_id', p.dsh_plugin_id, 'enabled', p.enabled, 'config_override', p.config_override)
					ORDER BY p.dsh_plugin_id)
				FROM agent_dsh_plugin p WHERE p.agent_id = $2::uuid), '[]'::jsonb))
		WHERE EXISTS (SELECT 1 FROM agent WHERE id = $2::uuid AND workspace_id = $1::uuid AND archived_at IS NULL)`,
		workspaceID, templateAgentID, snapshotSchema).Scan(&snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAgentUnavailable
	}
	return snapshot, err
}

// Revision is a published snapshot of the Tag's shared configuration.
type Revision struct {
	Revision  int32
	Snapshot  json.RawMessage
	Note      string
	CreatedBy string
	CreatedAt time.Time
}

// LatestRevision returns the newest revision of the template or ErrNotFound.
func LatestRevision(ctx context.Context, db DBTX, workspaceID, templateAgentID string) (Revision, error) {
	var r Revision
	err := db.QueryRow(ctx, `SELECT revision, snapshot, note, created_by::text, created_at
		FROM tag_config_revision WHERE workspace_id = $1::uuid AND tag_agent_id = $2::uuid
		ORDER BY revision DESC LIMIT 1`, workspaceID, templateAgentID).
		Scan(&r.Revision, &r.Snapshot, &r.Note, &r.CreatedBy, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	return r, err
}

// HasUnpublishedChanges reports whether the template differs from its latest
// revision (true when nothing has been published yet).
func HasUnpublishedChanges(ctx context.Context, db DBTX, workspaceID, templateAgentID string) (bool, error) {
	current, err := CaptureSnapshot(ctx, db, workspaceID, templateAgentID)
	if err != nil {
		return false, err
	}
	var changed bool
	err = db.QueryRow(ctx, `SELECT NOT EXISTS (
			SELECT 1 FROM tag_config_revision
			WHERE workspace_id = $1::uuid AND tag_agent_id = $2::uuid AND snapshot = $3::jsonb
			  AND revision = (SELECT max(revision) FROM tag_config_revision WHERE workspace_id = $1::uuid AND tag_agent_id = $2::uuid))`,
		workspaceID, templateAgentID, current).Scan(&changed)
	return changed, err
}

// lockConfig serializes publishes and applies of one Tag.
func lockConfig(ctx context.Context, tx DBTX, templateAgentID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tag_config:' || $1::text, 0))`, templateAgentID)
	return err
}

// PublishIfChanged returns the latest revision, first publishing the
// template's current configuration as a new revision when it differs.
// created reports whether a new revision was written.
func PublishIfChanged(ctx context.Context, tx DBTX, workspaceID, templateAgentID, actor, note string) (Revision, bool, error) {
	if err := lockConfig(ctx, tx, templateAgentID); err != nil {
		return Revision{}, false, err
	}
	current, err := CaptureSnapshot(ctx, tx, workspaceID, templateAgentID)
	if err != nil {
		return Revision{}, false, err
	}
	latest, err := LatestRevision(ctx, tx, workspaceID, templateAgentID)
	switch {
	case err == nil:
		var same bool
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb = $2::jsonb`, latest.Snapshot, current).Scan(&same); err != nil {
			return Revision{}, false, err
		}
		if same {
			return latest, false, nil
		}
	case !errors.Is(err, ErrNotFound):
		return Revision{}, false, err
	}
	var r Revision
	err = tx.QueryRow(ctx, `INSERT INTO tag_config_revision (workspace_id, tag_agent_id, revision, snapshot, note, created_by)
		VALUES ($1::uuid, $2::uuid,
			COALESCE((SELECT max(revision) FROM tag_config_revision WHERE workspace_id = $1::uuid AND tag_agent_id = $2::uuid), 0) + 1,
			$3::jsonb, $4, $5::uuid)
		RETURNING revision, snapshot, note, created_by::text, created_at`,
		workspaceID, templateAgentID, current, strings.TrimSpace(note), actor).
		Scan(&r.Revision, &r.Snapshot, &r.Note, &r.CreatedBy, &r.CreatedAt)
	if err != nil {
		return Revision{}, false, err
	}
	return r, true, nil
}

// ApplyResult reports what an apply could not copy.
type ApplyResult struct {
	Revision int32
	// SkippedSkillIDs are skills that no longer exist in the workspace or
	// are source-managed (a source-managed skill belongs to one agent only).
	SkippedSkillIDs []string
	// SkippedConnectorIDs and SkippedPluginIDs no longer exist in the
	// workspace.
	SkippedConnectorIDs []string
	SkippedPluginIDs    []string
	// SkippedOfferIDs are offered skills or connectors the employee may not
	// offer: gone from the workspace, or a skill another agent's Git source
	// manages (the rule contextcap applies to offer edits).
	SkippedOfferIDs []string
}

// Apply copies revision rev onto the tenant's employee agent and records it
// as the tenant's applied revision. The caller runs it in a transaction;
// Apply takes the Tag config lock and the tenant row lock itself.
func Apply(ctx context.Context, tx DBTX, workspaceID string, tenant Tenant, rev Revision, actor string) (ApplyResult, error) {
	if err := lockConfig(ctx, tx, tenant.TagAgentID); err != nil {
		return ApplyResult{}, err
	}
	if err := lockTenant(ctx, tx, workspaceID, tenant.ID); err != nil {
		return ApplyResult{}, err
	}
	result, err := copySnapshot(ctx, tx, workspaceID, tenant.EmployeeAgentID, rev.Snapshot, actor)
	if err != nil {
		return ApplyResult{}, err
	}
	result.Revision = rev.Revision
	if _, err := tx.Exec(ctx, `UPDATE tag_tenant SET applied_revision = $3, applied_at = now(), applied_by = $4::uuid, updated_at = now()
		WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID, tenant.ID, rev.Revision, actor); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

// CopyConfig copies one agent's whole configuration (managed and seeded)
// onto another, used to start a new Tag template from an existing agent.
func CopyConfig(ctx context.Context, tx DBTX, workspaceID, fromAgentID, toAgentID, actor string) (ApplyResult, error) {
	snapshot, err := CaptureSnapshot(ctx, tx, workspaceID, fromAgentID)
	if err != nil {
		return ApplyResult{}, err
	}
	result, err := copySnapshot(ctx, tx, workspaceID, toAgentID, snapshot, actor)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := SeedAgent(ctx, tx, workspaceID, fromAgentID, toAgentID); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

// copySnapshot replaces agentID's managed configuration with the snapshot's.
func copySnapshot(ctx context.Context, tx DBTX, workspaceID, agentID string, raw json.RawMessage, actor string) (ApplyResult, error) {
	var snapshot struct {
		Agent  json.RawMessage `json:"agent"`
		Skills []struct {
			SkillID string `json:"skill_id"`
			Enabled bool   `json:"enabled"`
		} `json:"skills"`
		ConnectorIDs []string `json:"connector_ids"`
		Offers       []struct {
			ResourceType string `json:"resource_type"`
			ResourceID   string `json:"resource_id"`
			Enabled      bool   `json:"enabled"`
		} `json:"offers"`
		DSHPlugins []struct {
			DSHPluginID    string          `json:"dsh_plugin_id"`
			Enabled        bool            `json:"enabled"`
			ConfigOverride json.RawMessage `json:"config_override"`
		} `json:"dsh_plugins"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return ApplyResult{}, fmt.Errorf("tag: decode snapshot: %w", err)
	}
	employee := agentID
	result := ApplyResult{SkippedSkillIDs: []string{}, SkippedConnectorIDs: []string{}, SkippedPluginIDs: []string{}, SkippedOfferIDs: []string{}}

	if len(snapshot.Agent) > 0 && string(snapshot.Agent) != "null" {
		updated, err := tx.Exec(ctx, applyAgentSQL(), []byte(snapshot.Agent), workspaceID, employee)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("tag: apply agent config: %w", err)
		}
		if updated.RowsAffected() == 0 {
			return ApplyResult{}, ErrAgentUnavailable
		}
	}

	// Skills: replace the employee's set with the revision's, skipping skills
	// that vanished or are owned by an agent source.
	if _, err := tx.Exec(ctx, `DELETE FROM agent_skill WHERE agent_id = $1::uuid`, employee); err != nil {
		return ApplyResult{}, err
	}
	for _, s := range snapshot.Skills {
		var inserted bool
		err := tx.QueryRow(ctx, `WITH ins AS (
				INSERT INTO agent_skill (agent_id, skill_id, enabled)
				SELECT $1::uuid, sk.id, $3
				FROM skill sk
				WHERE sk.id = $2::uuid AND sk.workspace_id = $4::uuid
				  AND NOT EXISTS (SELECT 1 FROM agent_source_skill ss WHERE ss.skill_id = sk.id)
				ON CONFLICT DO NOTHING
				RETURNING 1)
			SELECT EXISTS (SELECT 1 FROM ins)`, employee, s.SkillID, s.Enabled, workspaceID).Scan(&inserted)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("tag: apply skill %s: %w", s.SkillID, err)
		}
		if !inserted {
			result.SkippedSkillIDs = append(result.SkippedSkillIDs, s.SkillID)
		}
	}

	// Granted connectors (Aone FaaS connectors and connected apps).
	if _, err := tx.Exec(ctx, `DELETE FROM internal_connector_agent WHERE agent_id = $1::uuid AND workspace_id = $2::uuid`,
		employee, workspaceID); err != nil {
		return ApplyResult{}, err
	}
	for _, id := range snapshot.ConnectorIDs {
		tag, err := tx.Exec(ctx, `INSERT INTO internal_connector_agent (connector_id, workspace_id, agent_id)
			SELECT c.id, c.workspace_id, $3::uuid FROM internal_connector c
			WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid
			ON CONFLICT DO NOTHING`, id, workspaceID, employee)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("tag: apply connector %s: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			result.SkippedConnectorIDs = append(result.SkippedConnectorIDs, id)
		}
	}

	// Offer catalog (公开给场域). Same lock as contextcap.LockOffers so the
	// replacement does not interleave with an offer edit on the employee.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('context_capability_offers:' || $1::text, 0))`, employee); err != nil {
		return ApplyResult{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM context_capability_binding
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'offer'`, workspaceID, employee); err != nil {
		return ApplyResult{}, err
	}
	for _, o := range snapshot.Offers {
		inserted, err := tx.Exec(ctx, `INSERT INTO context_capability_binding
			(workspace_id, agent_id, scope_type, org_id, scope_key, resource_type, resource_id, enabled, created_by, updated_by)
			SELECT $1::uuid, $2::uuid, 'offer', '', '', $3::text, $4::uuid, $5, $6::uuid, $6::uuid
			WHERE ($3::text = 'skill' AND EXISTS (SELECT 1 FROM skill sk WHERE sk.id = $4::uuid AND sk.workspace_id = $1::uuid)
			        AND NOT EXISTS (SELECT 1 FROM agent_source_skill ass
			            WHERE ass.skill_id = $4::uuid
			              AND NOT EXISTS (SELECT 1 FROM agent_source src WHERE src.id = ass.agent_source_id AND src.agent_id = $2::uuid)))
			   OR ($3::text = 'connector' AND EXISTS (SELECT 1 FROM internal_connector c WHERE c.id = $4::uuid AND c.workspace_id = $1::uuid))
			ON CONFLICT (agent_id, scope_type, org_id, scope_key, resource_type, resource_id)
			DO UPDATE SET enabled = EXCLUDED.enabled, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			workspaceID, employee, o.ResourceType, o.ResourceID, o.Enabled, actor)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("tag: apply offer %s: %w", o.ResourceID, err)
		}
		if inserted.RowsAffected() == 0 {
			result.SkippedOfferIDs = append(result.SkippedOfferIDs, o.ResourceID)
		}
	}

	// DSH plugins the runtime boots with.
	if _, err := tx.Exec(ctx, `DELETE FROM agent_dsh_plugin WHERE agent_id = $1::uuid`, employee); err != nil {
		return ApplyResult{}, err
	}
	for _, p := range snapshot.DSHPlugins {
		var override any
		if len(p.ConfigOverride) > 0 && string(p.ConfigOverride) != "null" {
			override = []byte(p.ConfigOverride)
		}
		tag, err := tx.Exec(ctx, `INSERT INTO agent_dsh_plugin (agent_id, dsh_plugin_id, enabled, config_override)
			SELECT $1::uuid, d.id, $3, $4::jsonb FROM dsh_plugin d
			WHERE d.id = $2::uuid AND d.workspace_id = $5::uuid`, employee, p.DSHPluginID, p.Enabled, override, workspaceID)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("tag: apply dsh plugin %s: %w", p.DSHPluginID, err)
		}
		if tag.RowsAffected() == 0 {
			result.SkippedPluginIDs = append(result.SkippedPluginIDs, p.DSHPluginID)
		}
	}

	return result, nil
}
