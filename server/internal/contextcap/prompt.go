package contextcap

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Prompt component limits: at most MaxPromptComponents per scope, names of
// 1..MaxPromptComponentName characters, texts of 1..MaxScenePrompt
// characters.
const (
	MaxPromptComponents    = 20
	MaxPromptComponentName = 64
)

// ErrDuplicatePromptName rejects a prompt component list that names two
// components the same.
var ErrDuplicatePromptName = errors.New("contextcap: duplicate prompt component name")

// ScenePromptComponentName is the component the old per-scene prompt
// (agent_scene_config.prompt) was migrated into (migration 9428).
const ScenePromptComponentName = "场域提示词"

// PromptComponent is one row of context_prompt_component: a named prompt of
// one org, scene or person scope. Order is the position column.
type PromptComponent struct {
	ID        string
	ScopeType string
	OrgID     string
	ScopeKey  string
	Name      string
	Order     int
	Text      string
	// Disabled is the inverse of the enabled column (9431): a disabled
	// component stays stored but takes no part in MergeContext. The zero
	// value is enabled.
	Disabled bool
	// UpdatedBy is the user who last changed the component ("" when
	// unknown); UpdatedByName is that user's name.
	UpdatedBy     string
	UpdatedByName string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PromptComponentInput is one component of a ReplacePromptComponents list.
// Disabled stores the component switched off (the zero value is enabled).
type PromptComponentInput struct {
	Name     string
	Order    int
	Text     string
	Disabled bool
}

// NormalizePromptComponents trims names and texts and validates a full
// component list: at most MaxPromptComponents entries, names of
// 1..MaxPromptComponentName characters without control characters, texts
// that pass ValidScenePrompt and are not empty, orders within ±1_000_000.
// Duplicate names are ErrDuplicatePromptName; anything else invalid is
// ErrInvalidInput. The result keeps the input order.
func NormalizePromptComponents(in []PromptComponentInput) ([]PromptComponentInput, error) {
	if len(in) > MaxPromptComponents {
		return nil, ErrInvalidInput
	}
	out := make([]PromptComponentInput, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, component := range in {
		name := strings.TrimSpace(component.Name)
		text := strings.TrimSpace(component.Text)
		if !validPromptComponentName(name) || text == "" || !ValidScenePrompt(text) ||
			component.Order < -1_000_000 || component.Order > 1_000_000 {
			return nil, ErrInvalidInput
		}
		if seen[name] {
			return nil, ErrDuplicatePromptName
		}
		seen[name] = true
		out = append(out, PromptComponentInput{Name: name, Order: component.Order, Text: text, Disabled: component.Disabled})
	}
	return out, nil
}

func validPromptComponentName(name string) bool {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxPromptComponentName {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

const promptComponentColumns = `p.id::text, p.scope_type, p.org_id, p.scope_key, p.name, p.position, p.text, p.enabled,
	COALESCE(p.updated_by::text, ''), COALESCE((SELECT u.name FROM "user" u WHERE u.id = p.updated_by), ''), p.created_at, p.updated_at`

// ListPromptComponents returns the prompt components of one org, scene or
// person scope, disabled ones included, ordered by order, then name.
func ListPromptComponents(ctx context.Context, db DBTX, workspaceID, agentID, scopeType, orgID, scopeKey string) ([]PromptComponent, error) {
	if !ValidConfigScope(scopeType, orgID, scopeKey) {
		return nil, ErrInvalidInput
	}
	rows, err := db.Query(ctx, `SELECT `+promptComponentColumns+`
		FROM context_prompt_component p
		WHERE p.workspace_id = $1::uuid AND p.agent_id = $2::uuid AND p.scope_type = $3 AND p.org_id = $4 AND p.scope_key = $5
		ORDER BY p.position, p.name`, workspaceID, agentID, scopeType, orgID, scopeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PromptComponent{}
	for rows.Next() {
		var c PromptComponent
		var enabled bool
		if err := rows.Scan(&c.ID, &c.ScopeType, &c.OrgID, &c.ScopeKey, &c.Name, &c.Order, &c.Text, &enabled,
			&c.UpdatedBy, &c.UpdatedByName, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Disabled = !enabled
		out = append(out, c)
	}
	return out, rows.Err()
}

// PromptComponentsWrite is the input of ReplacePromptComponents. ActorID
// may be empty.
type PromptComponentsWrite struct {
	WorkspaceID string
	AgentID     string
	ScopeType   string
	OrgID       string
	ScopeKey    string
	Components  []PromptComponentInput
	ActorID     string
}

// ReplacePromptComponents replaces the prompt components of one org, scene
// or person scope with exactly in.Components (NormalizePromptComponents
// first). A component whose name stays keeps its id and creation time, and
// its update stamp when neither order, text nor its enabled switch changed. Run it inside a
// transaction: concurrent replacements of one scope serialize on an
// advisory lock. It returns the stored list (ListPromptComponents).
func ReplacePromptComponents(ctx context.Context, tx DBTX, in PromptComponentsWrite) ([]PromptComponent, error) {
	if !ValidConfigScope(in.ScopeType, in.OrgID, in.ScopeKey) {
		return nil, ErrInvalidInput
	}
	for _, id := range []string{in.WorkspaceID, in.AgentID} {
		if _, err := canonicalUUID(id); err != nil {
			return nil, err
		}
	}
	components, err := NormalizePromptComponents(in.Components)
	if err != nil {
		return nil, err
	}
	actor, err := optionalUUID(in.ActorID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('context_prompt_component:' || $1 || ':' || $2 || ':' || $3 || ':' || $4, 0))`,
		in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(components))
	for _, component := range components {
		names = append(names, component.Name)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM context_prompt_component
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = $3 AND org_id = $4 AND scope_key = $5
		  AND NOT (name = ANY($6::text[]))`,
		in.WorkspaceID, in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey, names); err != nil {
		return nil, err
	}
	for _, component := range components {
		tag, err := tx.Exec(ctx, `INSERT INTO context_prompt_component AS p
			(workspace_id, agent_id, scope_type, org_id, scope_key, name, position, text, enabled, updated_by)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid)
			ON CONFLICT (agent_id, scope_type, org_id, scope_key, name)
			DO UPDATE SET position = EXCLUDED.position, text = EXCLUDED.text, enabled = EXCLUDED.enabled,
			  updated_by = CASE WHEN p.position = EXCLUDED.position AND p.text = EXCLUDED.text AND p.enabled = EXCLUDED.enabled
			    THEN p.updated_by ELSE EXCLUDED.updated_by END,
			  updated_at = CASE WHEN p.position = EXCLUDED.position AND p.text = EXCLUDED.text AND p.enabled = EXCLUDED.enabled
			    THEN p.updated_at ELSE now() END
			WHERE p.workspace_id = EXCLUDED.workspace_id`,
			in.WorkspaceID, in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey, component.Name, component.Order, component.Text, !component.Disabled, actor)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			// A row of another workspace holds the name.
			return nil, ErrNotFound
		}
	}
	return ListPromptComponents(ctx, tx, in.WorkspaceID, in.AgentID, in.ScopeType, in.OrgID, in.ScopeKey)
}
