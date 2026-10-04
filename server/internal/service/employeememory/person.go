package employeememory

// DM person view (Multica Host extension; design 12-memory-design §5.2/§6.2).
// In a DM whose window has one known requester, the brief reads that
// requester's private records from every scene of the same agent and tenant,
// so a preference stated in a group applies in the DM. The direction is
// one-way: a group brief never reads private records at all. The principal is
// the org-qualified requester ref only; staffId-only refs, display names and
// body text never select a person.

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// PersonViewRef reports whether ref is an org-qualified uid or open_id
// requester ref of tenantOrgID ("dingtalk:<org>:uid:<v>" or
// "dingtalk:<org>:open_id:<v>"). A router uid ref and a native open_id ref of
// the same person are not merged: the view then finds nothing, which fails
// closed.
func PersonViewRef(tenantOrgID, ref string) bool {
	if !validText(tenantOrgID, 128) || strings.ContainsAny(tenantOrgID, ": \t\r\n") || len(ref) > 256 || !utf8.ValidString(ref) {
		return false
	}
	for _, kind := range []string{"uid", "open_id"} {
		prefix := "dingtalk:" + tenantOrgID + ":" + kind + ":"
		if value, ok := strings.CutPrefix(ref, prefix); ok {
			return value != "" && !strings.ContainsAny(value, ": \t\r\n") && !strings.ContainsRune(value, 0)
		}
	}
	return false
}

// personCorpus reads the requester's active private records across scenes.
// Each origin scene is re-fenced against the directory: it must still belong
// to this workspace, agent and current tenant. Records older than this DM's
// own private reset are omitted, so a reset in the DM starts its view fresh.
// Served by employee_learning_person_idx (9872).
func personCorpus(ctx context.Context, conn db.DBTX, dm Scope, requester string, limit int, now time.Time) ([]foregroundRecord, error) {
	rows, err := conn.Query(ctx, `SELECT l.scene_id::text,l.record FROM employee_learning l
JOIN agent_scene s ON s.id=l.scene_id AND s.workspace_id=l.workspace_id AND s.agent_id=l.agent_id AND s.tenant_org_id=l.tenant_org_id
WHERE l.workspace_id=$1 AND l.agent_id=$2 AND l.tenant_org_id=$3 AND l.scope_kind='private' AND l.principal_id=$4
AND l.forgotten_at IS NULL AND l.superseded_by IS NULL AND COALESCE(l.record->>'source','')<>'inferred'
AND l.created_at > COALESCE((SELECT m.reset_at FROM employee_memory_state m WHERE m.workspace_id=$1 AND m.agent_id=$2 AND m.tenant_org_id=$3 AND m.scene_id=$5 AND m.scope_kind='private' AND m.principal_id=$4),'-infinity'::timestamptz)
ORDER BY l.created_at DESC,l.id DESC LIMIT $6`, dm.WorkspaceID, dm.AgentID, dm.TenantOrgID, requester, dm.Scene.SceneID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []foregroundRecord
	for rows.Next() {
		var sceneID string
		var raw []byte
		if err := rows.Scan(&sceneID, &raw); err != nil {
			return nil, err
		}
		if rec, ok := decodeForegroundRecord(raw, now); ok {
			out = append(out, foregroundRecord{LearningSearchResult: rec, layer: ScopePrivate, sceneID: sceneID})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return dedupeForeground(out), nil
}
