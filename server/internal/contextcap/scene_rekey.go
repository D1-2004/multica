package contextcap

import (
	"context"
	"errors"
)

// ReconcileSceneCredentials re-keys connector credentials that still store a
// scene scope under a raw openConversationId (written before scene ids, or
// by an older binary during the rollout) onto the Agent work scene that
// migration 9510 registered for that conversation (docs/agent-scene.md).
// The credential seals its scope key, so each one is opened under its old
// binding and resealed under the scene_id; the row moves only while it
// still holds the old key. A conversation without a registered scene, or a
// credential that no longer opens, is left as is: it no longer applies.
// Idempotent and monotonic, so every replica may run it at startup.
func ReconcileSceneCredentials(ctx context.Context, db DBTX, box Box) (int, error) {
	if box == nil {
		return 0, ErrCredentialKeyUnavailable
	}
	rows, err := db.Query(ctx, `SELECT k.workspace_id::text, k.agent_id::text, k.connector_id::text, k.org_id, k.scope_key, k.ciphertext, s.id::text
		FROM context_connector_credential k
		JOIN agent_scene s ON s.workspace_id = k.workspace_id AND s.agent_id = k.agent_id AND s.provider = 'dingtalk'
		  AND s.tenant_org_id = k.org_id AND s.source_namespace = 'dingtalk.open_conversation_id'
		  AND s.external_scene_id = k.scope_key
		WHERE k.scope_type = 'scene' AND k.scope_key LIKE 'cid%'`)
	if err != nil {
		return 0, err
	}
	type pending struct {
		old, next CredentialBinding
		cipher    []byte
	}
	var todo []pending
	for rows.Next() {
		var p pending
		var sceneID string
		if err := rows.Scan(&p.old.WorkspaceID, &p.old.AgentID, &p.old.ConnectorID, &p.old.OrgID, &p.old.ScopeKey, &p.cipher, &sceneID); err != nil {
			rows.Close()
			return 0, err
		}
		p.old.ScopeType = ScopeScene
		p.next = p.old
		p.next.ScopeKey = sceneID
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	moved := 0
	for _, p := range todo {
		secret, err := OpenCredentialSecret(box, p.old, p.cipher)
		if err != nil {
			continue
		}
		var sealed []byte
		if secret.OAuth != nil {
			sealed, err = SealOAuthCredential(box, p.next, *secret.OAuth)
		} else {
			sealed, err = SealCredential(box, p.next, secret.Bearer)
		}
		if err != nil {
			if errors.Is(err, ErrInvalidBearer) {
				continue
			}
			return moved, err
		}
		tag, err := db.Exec(ctx, `UPDATE context_connector_credential
			SET scope_key = $6, ciphertext = $7
			WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND connector_id = $3::uuid
			  AND scope_type = 'scene' AND org_id = $4 AND scope_key = $5`,
			p.old.WorkspaceID, p.old.AgentID, p.old.ConnectorID, p.old.OrgID, p.old.ScopeKey, p.next.ScopeKey, sealed)
		if err != nil {
			return moved, err
		}
		moved += int(tag.RowsAffected())
	}
	return moved, nil
}
