package employeememory

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrStaleRevision = errors.New("employee memory revision is stale")

// SceneSnapshot contains only the shared scene namespace. Manager access never
// widens this query to private principals or combines them into a shared brief.
type SceneSnapshot struct {
	Scope                       Scope
	ConversationID, Kind, Title string
	Revision                    int64
	UpdatedAt                   time.Time
	Learnings                   []LearningRecord
	Truncated                   bool
}

const managedSceneSQL = `SELECT d.id::text,d.tenant_org_id,d.external_scene_id,d.scene_kind,d.title,
 COALESCE(ms.revision,0),COALESCE(ms.updated_at,d.updated_at),
 COALESCE((SELECT jsonb_agg(selected.record) FROM
  (SELECT l.record FROM employee_learning l WHERE l.workspace_id=d.workspace_id AND l.agent_id=d.agent_id
   AND l.tenant_org_id=d.tenant_org_id AND l.scene_id=d.id AND l.scope_kind='scene' AND l.principal_id=''
   AND l.forgotten_at IS NULL AND l.superseded_by IS NULL ORDER BY l.created_at DESC,l.id DESC LIMIT 201) selected),'[]'::jsonb)
 FROM agent_scene d LEFT JOIN employee_memory_state ms ON ms.workspace_id=d.workspace_id AND ms.agent_id=d.agent_id
 AND ms.tenant_org_id=d.tenant_org_id AND ms.scene_id=d.id AND ms.scope_kind='scene' AND ms.principal_id=''
 WHERE d.workspace_id=$1 AND d.agent_id=$2 AND d.tenant_org_id=ANY($3::text[])
 AND ($4::uuid IS NULL OR d.id=$4) AND ($5 OR ms.scene_id IS NOT NULL)
 ORDER BY ms.updated_at DESC NULLS LAST,d.id LIMIT $6`

type managementQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readManagedScenes(ctx context.Context, q managementQuery, ws, agent pgtype.UUID, orgs []string, sceneID any, includeEmpty bool, limit int) ([]SceneSnapshot, error) {
	rows, err := q.Query(ctx, managedSceneSQL, ws, agent, orgs, sceneID, includeEmpty, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SceneSnapshot, 0)
	for rows.Next() {
		var snapshot SceneSnapshot
		var id, org string
		var raw []byte
		if err := rows.Scan(&id, &org, &snapshot.ConversationID, &snapshot.Kind, &snapshot.Title, &snapshot.Revision, &snapshot.UpdatedAt, &raw); err != nil {
			return nil, err
		}
		snapshot.Scope = Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: org, Scene: scene.Ref{SceneID: id}, Kind: ScopeScene}
		if err := json.Unmarshal(raw, &snapshot.Learnings); err != nil {
			return nil, err
		}
		if len(snapshot.Learnings) > 200 {
			snapshot.Learnings = snapshot.Learnings[:200]
			snapshot.Truncated = true
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}
func (s *Store) ManagedScene(ctx context.Context, scope Scope) (SceneSnapshot, error) {
	if s == nil || s.pool == nil || scope.Kind != ScopeScene || scope.PrincipalID != "" {
		return SceneSnapshot{}, ErrInvalidScope
	}
	if err := authorize(ctx, db.New(s.pool), scope); err != nil {
		return SceneSnapshot{}, err
	}
	rows, err := readManagedScenes(ctx, s.pool, scope.WorkspaceID, scope.AgentID, []string{scope.TenantOrgID}, scope.Scene.SceneID, true, 1)
	if err != nil {
		return SceneSnapshot{}, err
	}
	if len(rows) != 1 {
		return SceneSnapshot{}, ErrInvalidScope
	}
	return rows[0], nil
}

// ManagedScenes accepts the current tenant set authorized by the Host. It cannot
// query private scope, even if a manager supplies another person's identity.
func (s *Store) ManagedScenes(ctx context.Context, ws, agent pgtype.UUID, orgs []string, limit int) ([]SceneSnapshot, error) {
	if s == nil || s.pool == nil || !ws.Valid || !agent.Valid {
		return nil, ErrInvalidScope
	}
	if len(orgs) == 0 {
		return []SceneSnapshot{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	return readManagedScenes(ctx, s.pool, ws, agent, orgs, nil, false, limit)
}

// ResetScene is the reusable exact-scene clear. UI requests supply a revision;
// a trusted inbound reset command may pass nil after authorizing its frozen owner.
// Replay tombstones survive and private namespaces are deliberately untouched.
func (s *Store) ResetScene(ctx context.Context, scope Scope, expected *int64) (SceneSnapshot, error) {
	if s == nil || s.pool == nil {
		return SceneSnapshot{}, ErrInvalidScope
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SceneSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	out, err := s.ResetSceneTx(ctx, tx, scope, expected)
	if err != nil {
		return SceneSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SceneSnapshot{}, err
	}
	return out, nil
}

// ResetSceneTx lets the management Host hold the workspace/agent mode fence in
// the same transaction as the memory revision and reset, preventing stale UI writes.
func (s *Store) ResetSceneTx(ctx context.Context, tx pgx.Tx, scope Scope, expected *int64) (SceneSnapshot, error) {
	if scope.Kind != ScopeScene || scope.PrincipalID != "" {
		return SceneSnapshot{}, ErrInvalidScope
	}
	if err := scope.validate(); err != nil {
		return SceneSnapshot{}, err
	}
	var id pgtype.UUID
	if err := tx.QueryRow(ctx, "SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE", scope.WorkspaceID).Scan(&id); err != nil {
		return SceneSnapshot{}, err
	}
	if err := authorize(ctx, db.New(tx), scope); err != nil {
		return SceneSnapshot{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, scope.args()...); err != nil {
		return SceneSnapshot{}, err
	}
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM employee_memory_state WHERE `+scopePredicate+` FOR UPDATE`, scope.args()...).Scan(&revision); err != nil {
		return SceneSnapshot{}, err
	}
	if expected != nil && (*expected < 0 || *expected != revision) {
		return SceneSnapshot{}, ErrStaleRevision
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_learning SET forgotten_at=now() WHERE `+scopePredicate+` AND forgotten_at IS NULL`, scope.args()...); err != nil {
		return SceneSnapshot{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,reset_at=now(),updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
		return SceneSnapshot{}, err
	}
	rows, err := readManagedScenes(ctx, tx, scope.WorkspaceID, scope.AgentID, []string{scope.TenantOrgID}, scope.Scene.SceneID, true, 1)
	if err != nil {
		return SceneSnapshot{}, err
	}
	if len(rows) != 1 {
		return SceneSnapshot{}, ErrInvalidScope
	}
	return rows[0], nil
}
