package employeetask

import (
	"context"
	"strings"
	"time"
)

// DiscoveryTask is deliberately smaller than Task. Discovery never reads
// execution instructions, entries, reports or a different scene's material.
type DiscoveryTask struct {
	ID           string    `json:"-"`
	RequesterRef string    `json:"-"`
	Goal         string    `json:"goal"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Find selects a relevant owner's layer first. The second layer is available
// only in the exact registered group scene, and contains metadata only.
func (s *Store) Find(ctx context.Context, scope Scope, requester, query string, before time.Time, limit int) ([]DiscoveryTask, bool, error) {
	if validateScope(scope) != nil || scope.Kind != ScopeScene || strings.TrimSpace(requester) == "" || before.IsZero() || limit < 1 || limit > 10 || len(query) > 256 {
		return nil, false, ErrInvalid
	}
	query = strings.TrimSpace(query)
	rows, err := s.findLayer(ctx, scope, requester, query, before, limit, false)
	if err != nil || len(rows) != 0 {
		return rows, false, err
	}
	var group bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_scene WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 AND scene_kind='group' AND kind_source='observed')`, scope.Scene.SceneID, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID).Scan(&group); err != nil {
		return nil, false, err
	}
	if !group {
		return rows, false, nil
	}
	rows, err = s.findLayer(ctx, scope, requester, query, before, limit, true)
	return rows, true, err
}

func (s *Store) findLayer(ctx context.Context, scope Scope, requester, query string, before time.Time, limit int, shared bool) ([]DiscoveryTask, error) {
	owner := `requester_ref=$6`
	if shared {
		owner = `requester_ref<>$6`
	}
	args := append(scopeArgs(scope), requester, before, query, limit)
	rows, err := s.db.Query(ctx, `SELECT id::text,requester_ref,definition->>'goal',state,created_at,updated_at FROM employee_task WHERE `+taskScope+` AND `+owner+` AND created_at<=$7 AND owner_loop='employee' AND dispatch_mode='direct' AND ($8='' OR strpos(lower(definition->>'goal'),lower($8))>0) ORDER BY updated_at DESC,id DESC LIMIT $9`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DiscoveryTask{}
	for rows.Next() {
		var task DiscoveryTask
		if err := rows.Scan(&task.ID, &task.RequesterRef, &task.Goal, &task.State, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// ReadSharedMetadata performs a fresh, exact-scope group read without loading
// the other owner's Task definition or execution material.
func (s *Store) ReadSharedMetadata(ctx context.Context, scope Scope, requester, id string) (DiscoveryTask, error) {
	if validateScope(scope) != nil || scope.Kind != ScopeScene || requester == "" || !validUUID(id) {
		return DiscoveryTask{}, ErrInvalid
	}
	args := append(taskArgs(scope, id), requester)
	var task DiscoveryTask
	err := s.db.QueryRow(ctx, `SELECT id::text,requester_ref,definition->>'goal',state,created_at,updated_at FROM employee_task WHERE `+taskWhere+` AND requester_ref<>$7 AND owner_loop='employee' AND dispatch_mode='direct' AND EXISTS(SELECT 1 FROM agent_scene s WHERE s.id=$5::uuid AND s.workspace_id=$1::uuid AND s.agent_id=$2::uuid AND s.tenant_org_id=$3 AND s.scene_kind='group' AND s.kind_source='observed')`, args...).Scan(&task.ID, &task.RequesterRef, &task.Goal, &task.State, &task.CreatedAt, &task.UpdatedAt)
	return task, mapError(err)
}
