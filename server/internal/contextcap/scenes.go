package contextcap

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Scene kinds. A scene is an Agent work scene (agent_scene,
// docs/agent-scene.md): a DingTalk group or 1:1 chat by its conversation,
// configured and applied the same way.
const (
	SceneKindGroup = "group"
	SceneKindDM    = "dm"
	// MaxScenePrompt is the prompt component text limit in characters
	// (code points), inherited from the scene prompt it replaced.
	MaxScenePrompt   = 8000
	maxScenesPerPage = 200
)

// ValidScenePrompt reports whether prompt (already trimmed) fits the scene
// prompt limit: at most MaxScenePrompt characters of valid UTF-8 without NUL
// bytes, which PostgreSQL text rejects.
func ValidScenePrompt(prompt string) bool {
	return utf8.ValidString(prompt) && utf8.RuneCountInString(prompt) <= MaxScenePrompt && !strings.ContainsRune(prompt, 0)
}

// SceneSummary is one conversation scene of an agent: its agent_scene
// directory row (docs/agent-scene.md) with the Coordinator, Scene Memory and
// prompt facts joined by scene_id. Scenes are never discovered from other
// tables.
type SceneSummary struct {
	// SceneID is the agent_scene id, the only scene reference.
	SceneID string
	// ConversationID is the scene's DingTalk openConversationId.
	ConversationID string
	Kind           string
	OrgID          string
	Title          string
	LastActiveAt   time.Time
	// InboundSessionID is the newest Coordinator chat session of the scene
	// ("" when there is none); InboundCount counts the sessions its
	// transcript covers (same endpoint namespace and source).
	InboundSessionID string
	InboundCount     int
	HasMemory        bool
	HasPrompt        bool
	MemoryText       string
}

// SceneListQuery selects the conversation scenes of one agent in tenant org
// OrgID. IDs, when non-nil, restricts the result to those scene ids; such a
// lookup is not paged. Otherwise ListAgentScenes clamps Limit to 1..200.
// GroupsOnly drops 1:1 chats.
type SceneListQuery struct {
	WorkspaceID string
	AgentID     string
	OrgID       string
	IDs         []string
	Limit       int
	Offset      int
	GroupsOnly  bool
}

// ListAgentScenes returns the agent's conversation scenes in q.OrgID ordered
// by last activity (newest first) and whether more rows follow the page.
func ListAgentScenes(ctx context.Context, db DBTX, q SceneListQuery) ([]SceneSummary, bool, error) {
	limit := q.Limit
	if limit <= 0 || limit > maxScenesPerPage {
		limit = maxScenesPerPage
	}
	return listAgentScenes(ctx, db, q, limit, max(q.Offset, 0))
}

// MaxAllScenes bounds ListAllAgentScenes.
const MaxAllScenes = 1000

// ListAllAgentScenes returns up to limit (clamped to 1..MaxAllScenes) scenes
// of the agent in one statement, newest activity first, and whether more
// exist. q.Limit and q.Offset are ignored.
func ListAllAgentScenes(ctx context.Context, db DBTX, q SceneListQuery, limit int) ([]SceneSummary, bool, error) {
	if limit <= 0 || limit > MaxAllScenes {
		limit = MaxAllScenes
	}
	return listAgentScenes(ctx, db, q, limit, 0)
}

func listAgentScenes(ctx context.Context, db DBTX, q SceneListQuery, limit, offset int) ([]SceneSummary, bool, error) {
	if strings.TrimSpace(q.OrgID) == "" {
		return []SceneSummary{}, false, nil
	}
	ids := []string(nil)
	if q.IDs != nil {
		for _, id := range q.IDs {
			if ValidSceneID(id) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return []SceneSummary{}, false, nil
		}
		limit, offset = len(ids), 0
	}
	args := []any{q.WorkspaceID, q.AgentID, q.OrgID, limit + 1, offset, q.GroupsOnly}
	idFilter := ""
	if ids != nil {
		args = append(args, ids)
		idFilter = ` AND s.id = ANY($7::uuid[])`
	}
	rows, err := db.Query(ctx, `SELECT s.id::text, s.external_scene_id, s.scene_kind, s.tenant_org_id, s.title, s.last_active_at,
		  COALESCE(conv.session_id, ''), COALESCE(cnt.session_count, 0),
		  m.scene_id IS NOT NULL, COALESCE(m.memory_text, ''),
		  EXISTS (SELECT 1 FROM context_prompt_component p
		    WHERE p.agent_id = s.agent_id AND p.scope_type = 'scene' AND p.org_id = s.tenant_org_id
		      AND p.scope_key = s.id::text AND p.workspace_id = s.workspace_id)
		FROM agent_scene s
		LEFT JOIN agent_scene_memory m ON m.scene_id = s.id AND m.workspace_id = s.workspace_id AND m.agent_id = s.agent_id
		LEFT JOIN LATERAL (
		  SELECT cs.id::text AS session_id, job.endpoint_namespace_id,
		    job.command #>> '{source,platform}' AS source_platform, job.command #>> '{source,type}' AS source_type
		  FROM inbound_coordinator_job job
		  JOIN chat_session cs ON cs.id = job.chat_session_id AND cs.workspace_id = job.workspace_id AND cs.agent_id = job.agent_id
		  WHERE job.agent_id = s.agent_id AND (job.command #>> '{agent_scene,scene_id}') = s.id::text
		    AND job.workspace_id = s.workspace_id
		  ORDER BY cs.updated_at DESC, cs.id DESC
		  LIMIT 1
		) conv ON TRUE
		LEFT JOIN LATERAL (
		  SELECT count(*) AS session_count
		  FROM inbound_coordinator_job job
		  WHERE job.agent_id = s.agent_id AND (job.command #>> '{agent_scene,scene_id}') = s.id::text
		    AND job.workspace_id = s.workspace_id AND job.chat_session_id IS NOT NULL
		    AND job.endpoint_namespace_id IS NOT DISTINCT FROM conv.endpoint_namespace_id
		    AND (job.command #>> '{source,platform}') IS NOT DISTINCT FROM conv.source_platform
		    AND (job.command #>> '{source,type}') IS NOT DISTINCT FROM conv.source_type
		) cnt ON conv.session_id IS NOT NULL
		WHERE s.workspace_id = $1::uuid AND s.agent_id = $2::uuid AND s.tenant_org_id = $3::text
		  AND s.scene_kind IN ('group', 'dm') AND (NOT $6::boolean OR s.scene_kind = 'group')`+idFilter+`
		ORDER BY s.last_active_at DESC, s.id DESC
		LIMIT $4 OFFSET $5`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []SceneSummary{}
	for rows.Next() {
		var s SceneSummary
		var inboundCount int64
		if err := rows.Scan(&s.SceneID, &s.ConversationID, &s.Kind, &s.OrgID, &s.Title, &s.LastActiveAt,
			&s.InboundSessionID, &inboundCount, &s.HasMemory, &s.MemoryText, &s.HasPrompt); err != nil {
			return nil, false, err
		}
		s.InboundCount = int(inboundCount)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// GetScene returns one conversation scene of the agent in orgID by its
// scene_id, or ErrNotFound (another agent's scene, another org, an
// enterprise scene or an unknown id).
func GetScene(ctx context.Context, db DBTX, workspaceID, agentID, orgID, sceneID string) (SceneSummary, error) {
	if !ValidSceneID(sceneID) {
		return SceneSummary{}, ErrInvalidInput
	}
	scenes, _, err := ListAgentScenes(ctx, db, SceneListQuery{
		WorkspaceID: workspaceID, AgentID: agentID, OrgID: orgID, IDs: []string{sceneID},
	})
	if err != nil {
		return SceneSummary{}, err
	}
	if len(scenes) == 0 {
		return SceneSummary{}, ErrNotFound
	}
	return scenes[0], nil
}
