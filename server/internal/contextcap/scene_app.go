package contextcap

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSceneAppExists: a scene already has an OAuth application for the
// provider (someone saved it first).
var ErrSceneAppExists = errors.New("contextcap: the scene already has an OAuth application for this app")

// SceneAppKey names a scene's own OAuth application of one provider (a
// catalog app slug).
type SceneAppKey struct {
	WorkspaceID string
	AgentID     string
	OrgID       string
	SceneID     string
	Provider    string
}

// SceneAppKeyOf is the scene application key of a scene credential binding;
// ok is false for any other scope.
func SceneAppKeyOf(binding CredentialBinding, provider string) (SceneAppKey, bool) {
	if binding.ScopeType != ScopeScene || binding.WorkspaceID == "" || binding.AgentID == "" ||
		binding.ScopeKey == "" || strings.TrimSpace(provider) == "" {
		return SceneAppKey{}, false
	}
	return SceneAppKey{
		WorkspaceID: binding.WorkspaceID, AgentID: binding.AgentID, OrgID: binding.OrgID,
		SceneID: binding.ScopeKey, Provider: provider,
	}, true
}

// SceneApp is a scene's own OAuth application. SecretCiphertext is sealed
// with the internal connector secret box.
type SceneApp struct {
	ID               string
	ClientID         string
	SecretCiphertext []byte
	SecretHint       string
	UpdatedAt        time.Time
}

// GetSceneApp returns the scene's OAuth application of key's provider, or
// ErrNotFound.
func GetSceneApp(ctx context.Context, db DBTX, key SceneAppKey) (SceneApp, error) {
	var app SceneApp
	err := db.QueryRow(ctx, `SELECT id::text, client_id, client_secret_ciphertext, client_secret_hint, updated_at
		FROM context_connector_app
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'scene' AND org_id = $3
		  AND scope_key = $4 AND provider = $5`,
		key.WorkspaceID, key.AgentID, key.OrgID, key.SceneID, key.Provider).
		Scan(&app.ID, &app.ClientID, &app.SecretCiphertext, &app.SecretHint, &app.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SceneApp{}, ErrNotFound
	}
	return app, err
}

// ListSceneAppProviders lists the providers the scene has its own OAuth
// application of, in name order.
func ListSceneAppProviders(ctx context.Context, db DBTX, workspaceID, agentID, orgID, sceneID string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT provider FROM context_connector_app
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'scene' AND org_id = $3 AND scope_key = $4
		ORDER BY provider`, workspaceID, agentID, orgID, sceneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, rows.Err()
}

// CreateSceneApp saves the scene's first OAuth application of key's
// provider; ErrSceneAppExists when one is already saved.
func CreateSceneApp(ctx context.Context, db DBTX, key SceneAppKey, clientID string, secretCiphertext []byte, hint, actorID string) error {
	if strings.TrimSpace(clientID) == "" || len(secretCiphertext) == 0 {
		return ErrInvalidInput
	}
	tag, err := db.Exec(ctx, `INSERT INTO context_connector_app
		(workspace_id, agent_id, scope_type, org_id, scope_key, provider, client_id, client_secret_ciphertext, client_secret_hint, created_by, updated_by)
		VALUES ($1::uuid, $2::uuid, 'scene', $3, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid, NULLIF($9, '')::uuid)
		ON CONFLICT (agent_id, scope_type, org_id, scope_key, provider) DO NOTHING`,
		key.WorkspaceID, key.AgentID, key.OrgID, key.SceneID, key.Provider, strings.TrimSpace(clientID), secretCiphertext, hint, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSceneAppExists
	}
	return nil
}

// UpdateSceneApp changes the scene's saved OAuth application: its client id,
// and its secret when secretCiphertext is not empty. ErrNotFound when none
// is saved.
func UpdateSceneApp(ctx context.Context, db DBTX, key SceneAppKey, clientID string, secretCiphertext []byte, hint, actorID string) error {
	if strings.TrimSpace(clientID) == "" {
		return ErrInvalidInput
	}
	tag, err := db.Exec(ctx, `UPDATE context_connector_app SET client_id = $6,
			client_secret_ciphertext = CASE WHEN length($7::bytea) > 0 THEN $7::bytea ELSE client_secret_ciphertext END,
			client_secret_hint = CASE WHEN length($7::bytea) > 0 THEN $8 ELSE client_secret_hint END,
			updated_by = NULLIF($9, '')::uuid, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND scope_type = 'scene' AND org_id = $3
		  AND scope_key = $4 AND provider = $5`,
		key.WorkspaceID, key.AgentID, key.OrgID, key.SceneID, key.Provider, strings.TrimSpace(clientID), secretCiphertext, hint, actorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
