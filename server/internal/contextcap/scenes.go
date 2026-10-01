package contextcap

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// Scene kinds. A DingTalk 1:1 chat is a scene exactly like a group chat for
// configuration (docs/context-capabilities.md §1); runtime resolution still
// applies group scenes only. Scenes are DingTalk IM scenes only (platform
// 'dingtalk').
const (
	SceneKindGroup = "group"
	SceneKindDM    = "dm"
	// MaxScenePrompt is the prompt component text limit in characters
	// (code points), inherited from the scene prompt it replaced.
	MaxScenePrompt   = 8000
	maxScenesPerPage = 200
)

// SceneKindForConversationType maps a dispatch conversation type onto a
// scene kind: dm for positively 1:1 types (IsDirectConversationType),
// group otherwise.
func SceneKindForConversationType(conversationType string) string {
	if IsDirectConversationType(conversationType) {
		return SceneKindDM
	}
	return SceneKindGroup
}

// ValidScenePrompt reports whether prompt (already trimmed) fits the scene
// prompt limit: at most MaxScenePrompt characters of valid UTF-8 without NUL
// bytes, which PostgreSQL text rejects.
func ValidScenePrompt(prompt string) bool {
	return utf8.ValidString(prompt) && utf8.RuneCountInString(prompt) <= MaxScenePrompt && !strings.ContainsRune(prompt, 0)
}

// SceneSummary is one IM scene of an agent, merged from every place a scene
// shows up: scene_memory, inbound Coordinator conversations
// (inbound_coordinator_job), agent_scene_config, scene bindings and scene
// credentials (a group can store a token without turning anything on). The raw
// per-source titles are returned so the caller can pick a display title.
type SceneSummary struct {
	SceneKey string
	Kind     string
	// OrgID is the scene memory row's org when there is one, else the
	// agent's current org.
	OrgID        string
	LastActiveAt time.Time
	// InboundSessionID is the newest Coordinator chat session of the
	// conversation ("" when there is none); InboundCount counts the sessions
	// its transcript covers (same endpoint namespace and source).
	InboundSessionID string
	InboundCount     int
	MemoryID         string
	HasPrompt        bool

	MemoryTitle       string
	MemoryText        string
	ConversationTitle string
	SenderName        string
	ConfigTitle       string
	BindingTitle      string
}

// SceneListQuery selects scenes of one agent under OrgID (a tenant org of
// the agent; "" for an agent without a DingTalk identity, which matches the
// agent's scene memory of any org). IdentityOrgID is the agent's DingTalk
// identity org ("" without one): Coordinator jobs that recorded no agent
// org belong to it. Keys, when non-nil, restricts the result to those scene
// keys; such a lookup is bounded by its key set and not paged (Limit and
// Offset are ignored, every matching scene is returned). Otherwise
// ListAgentScenes clamps Limit to 1..200. GroupsOnly drops 1:1 chats.
type SceneListQuery struct {
	WorkspaceID   string
	AgentID       string
	OrgID         string
	IdentityOrgID string
	Keys          []string
	Limit         int
	Offset        int
	GroupsOnly    bool
}

// ListAgentScenes returns the agent's IM scenes ordered by last activity
// (newest first) and whether more rows follow the page. It is one statement
// over the indexed scene tables plus the agent's Coordinator jobs, which it
// reads through inbound_coordinator_job_agent_conversation_idx (agent_id,
// openConversationId): the whole agent for a page, one conversation per key
// for a Keys lookup.
//
// Org scoping follows scene memory: rows of scene_memory match q.OrgID, or
// any org while OrgID is "" (an agent without a DingTalk identity; scene
// memory then records the dispatch's DWS org); configuration rows, bindings,
// credentials and prompt components match the org exactly; Coordinator jobs
// match their recorded agent org, and jobs that recorded none belong to
// q.IdentityOrgID. Only keys shaped like an openConversationId are
// returned. HasPrompt reports scene-scope prompt components.
//
// The inbound session is the newest Coordinator chat session of the
// conversation, and InboundCount counts the sessions its transcript shows:
// the ListCoordinatorConversationMessages anchor partition (same endpoint
// namespace, source platform and source type as that session).
//
// Kind is dm only on positive evidence: the newest job's conversation type is
// a 1:1 type (IsDirectConversationType), or, when that job carries no type or
// there is no job, the configuration row says dm (RegisterDirectScene).
// scene_memory.scene_kind is not used: scenememory.KindFromChatType
// records dm for every type that is not "group", an empty or unknown type
// included.
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
// of the agent, newest activity first, and whether more exist. Unlike paging
// through ListAgentScenes it is one statement: the agent's Coordinator jobs
// and scene memory are scanned once, and the list is one consistent snapshot
// (a scene whose activity moves it between pages is neither skipped nor
// repeated). q.Limit and q.Offset are ignored. It is for internal callers
// that need the whole list (the configure page of an agent manager); paged
// HTTP listings use ListAgentScenes.
func ListAllAgentScenes(ctx context.Context, db DBTX, q SceneListQuery, limit int) ([]SceneSummary, bool, error) {
	if limit <= 0 || limit > MaxAllScenes {
		limit = MaxAllScenes
	}
	return listAgentScenes(ctx, db, q, limit, 0)
}

// listAgentScenes runs the scene statement for one page (limit rows from
// offset), or, for a Keys lookup, for every matching scene.
func listAgentScenes(ctx context.Context, db DBTX, q SceneListQuery, limit, offset int) ([]SceneSummary, bool, error) {
	if q.Keys != nil {
		limit, offset = max(len(q.Keys), 1), 0
	}
	args := []any{q.WorkspaceID, q.AgentID, q.OrgID, limit + 1, offset, q.IdentityOrgID}
	// A Keys lookup filters every source by key inside its own scan, as a
	// plain predicate (never "$n IS NULL OR ..."), so a cached generic plan
	// still probes the indexes by key.
	memKeys, jobKeys, cfgKeys, bindKeys, credKeys, promptKeys := "", "", "", "", "", ""
	if q.Keys != nil {
		args = append(args, q.Keys)
		memKeys = ` AND m.scene_key = ANY($7::text[])`
		jobKeys = ` AND BTRIM(job.command #>> '{event,data,conversation,openConversationId}') = ANY($7::text[])`
		cfgKeys = ` AND c.scene_key = ANY($7::text[])`
		bindKeys = ` AND b.scope_key = ANY($7::text[])`
		credKeys = ` AND k.scope_key = ANY($7::text[])`
		promptKeys = ` AND p.scope_key = ANY($7::text[])`
	}
	kindFilter := ""
	if q.GroupsOnly {
		kindFilter = ` AND listed.kind = 'group'`
	}
	rows, err := db.Query(ctx, `WITH mem AS (
		  SELECT DISTINCT ON (m.scene_key) m.scene_key, m.scene_title, m.memory_text, m.org_id, m.id::text AS memory_id, m.updated_at
		  FROM scene_memory m
		  WHERE m.workspace_id = $1::uuid AND m.agent_id = $2::uuid AND m.platform = 'dingtalk'
		    AND ($3::text = '' OR m.org_id = $3::text)`+memKeys+`
		  ORDER BY m.scene_key, (m.org_id = $3::text) DESC, m.updated_at DESC
		), jobs AS (
		  SELECT BTRIM(job.command #>> '{event,data,conversation,openConversationId}') AS scene_key,
		    COALESCE(job.command #>> '{event,data,conversation,type}', '') AS conversation_type,
		    COALESCE(job.command #>> '{event,data,conversation,title}', '') AS conversation_title,
		    COALESCE(job.command #>> '{event,data,sender,displayName}', '') AS sender_name,
		    job.endpoint_namespace_id, job.command #>> '{source,platform}' AS source_platform,
		    job.command #>> '{source,type}' AS source_type,
		    cs.id AS session_id, cs.updated_at
		  FROM inbound_coordinator_job job
		  JOIN chat_session cs ON cs.id = job.chat_session_id
		    AND cs.workspace_id = job.workspace_id AND cs.agent_id = job.agent_id
		  WHERE job.agent_id = $2::uuid AND job.workspace_id = $1::uuid`+jobKeys+`
		    AND lower(COALESCE(NULLIF(BTRIM(job.command #>> '{source,platform}'), ''), 'dingtalk')) = 'dingtalk'
		    AND ($3::text = '' OR `+jobOrgExpr("$6")+` = $3::text)
		    AND NULLIF(BTRIM(job.command #>> '{event,data,conversation,openConversationId}'), '') IS NOT NULL
		), conv AS (
		  SELECT DISTINCT ON (jobs.scene_key) jobs.scene_key, jobs.conversation_type, jobs.conversation_title, jobs.sender_name,
		    jobs.session_id::text AS session_id, jobs.updated_at,
		    count(*) OVER (PARTITION BY jobs.scene_key, jobs.endpoint_namespace_id, jobs.source_platform, jobs.source_type) AS session_count
		  FROM jobs
		  ORDER BY jobs.scene_key, jobs.updated_at DESC, jobs.session_id DESC
		), cfg AS (
		  SELECT c.scene_key, c.scene_kind, c.scene_title, c.updated_at
		  FROM agent_scene_config c
		  WHERE c.workspace_id = $1::uuid AND c.agent_id = $2::uuid AND c.platform = 'dingtalk' AND c.org_id = $3::text`+cfgKeys+`
		), bind AS (
		  SELECT b.scope_key AS scene_key,
		    COALESCE(max(b.scope_title) FILTER (WHERE b.scope_title <> ''), '') AS scope_title,
		    max(b.updated_at) AS updated_at
		  FROM context_capability_binding b
		  WHERE b.workspace_id = $1::uuid AND b.agent_id = $2::uuid AND b.scope_type = 'scene' AND b.org_id = $3::text`+bindKeys+`
		  GROUP BY b.scope_key
		), cred AS (
		  SELECT k.scope_key AS scene_key, max(k.updated_at) AS updated_at
		  FROM context_connector_credential k
		  WHERE k.workspace_id = $1::uuid AND k.agent_id = $2::uuid AND k.scope_type = 'scene' AND k.org_id = $3::text`+credKeys+`
		  GROUP BY k.scope_key
		), prm AS (
		  SELECT p.scope_key AS scene_key, max(p.updated_at) AS updated_at
		  FROM context_prompt_component p
		  WHERE p.workspace_id = $1::uuid AND p.agent_id = $2::uuid AND p.scope_type = 'scene' AND p.org_id = $3::text`+promptKeys+`
		  GROUP BY p.scope_key
		), keys AS (
		  SELECT scene_key FROM mem UNION SELECT scene_key FROM conv UNION SELECT scene_key FROM cfg UNION SELECT scene_key FROM bind
		  UNION SELECT scene_key FROM cred UNION SELECT scene_key FROM prm
		), listed AS (
		  SELECT k.scene_key,
		    CASE WHEN NULLIF(BTRIM(conv.conversation_type), '') IS NOT NULL
		      THEN CASE WHEN lower(BTRIM(conv.conversation_type)) IN ('single', 'p2p', 'private', 'direct') THEN 'dm' ELSE 'group' END
		      ELSE COALESCE(cfg.scene_kind, 'group') END AS kind,
		    COALESCE(mem.org_id, $3::text) AS org_id,
		    GREATEST(mem.updated_at, conv.updated_at, cfg.updated_at, bind.updated_at, cred.updated_at, prm.updated_at) AS last_active_at,
		    COALESCE(conv.session_id, '') AS session_id, COALESCE(conv.session_count, 0) AS session_count,
		    COALESCE(mem.memory_id, '') AS memory_id, prm.scene_key IS NOT NULL AS has_prompt,
		    COALESCE(mem.scene_title, '') AS memory_title, COALESCE(mem.memory_text, '') AS memory_text,
		    COALESCE(conv.conversation_title, '') AS conversation_title, COALESCE(conv.sender_name, '') AS sender_name,
		    COALESCE(cfg.scene_title, '') AS config_title, COALESCE(bind.scope_title, '') AS binding_title
		  FROM keys k
		  LEFT JOIN mem ON mem.scene_key = k.scene_key
		  LEFT JOIN conv ON conv.scene_key = k.scene_key
		  LEFT JOIN cfg ON cfg.scene_key = k.scene_key
		  LEFT JOIN bind ON bind.scene_key = k.scene_key
		  LEFT JOIN cred ON cred.scene_key = k.scene_key
		  LEFT JOIN prm ON prm.scene_key = k.scene_key
		  WHERE k.scene_key LIKE 'cid%' AND octet_length(k.scene_key) <= 256 AND k.scene_key !~ '[[:space:][:cntrl:]]'
		)
		SELECT listed.scene_key, listed.kind, listed.org_id, listed.last_active_at, listed.session_id, listed.session_count,
		  listed.memory_id, listed.has_prompt, listed.memory_title, listed.memory_text, listed.conversation_title,
		  listed.sender_name, listed.config_title, listed.binding_title
		FROM listed
		WHERE TRUE`+kindFilter+`
		ORDER BY listed.last_active_at DESC NULLS LAST, listed.scene_key
		LIMIT $4 OFFSET $5`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []SceneSummary{}
	for rows.Next() {
		var s SceneSummary
		var lastActive *time.Time
		var inboundCount int64
		if err := rows.Scan(&s.SceneKey, &s.Kind, &s.OrgID, &lastActive, &s.InboundSessionID, &inboundCount,
			&s.MemoryID, &s.HasPrompt,
			&s.MemoryTitle, &s.MemoryText, &s.ConversationTitle, &s.SenderName, &s.ConfigTitle, &s.BindingTitle); err != nil {
			return nil, false, err
		}
		if lastActive != nil {
			s.LastActiveAt = *lastActive
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

// SceneKinds returns the kind of each key for the configure page: the
// configured kind (a 1:1 scene granted through a personal link is always
// registered in agent_scene_config as dm), else group. It reads only the
// indexed agent_scene_config rows, so it suits hot read paths.
// scene_memory.scene_kind is not trusted for dm (see ListAgentScenes).
func SceneKinds(ctx context.Context, db DBTX, workspaceID, agentID, orgID string, keys []string) (map[string]string, error) {
	kinds := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return kinds, nil
	}
	rows, err := db.Query(ctx, `SELECT k.scene_key, COALESCE(
		  (SELECT c.scene_kind FROM agent_scene_config c
		   WHERE c.agent_id = $2::uuid AND c.platform = 'dingtalk' AND c.org_id = $3::text
		     AND c.scene_key = k.scene_key AND c.workspace_id = $1::uuid),
		  'group')
		FROM unnest($4::text[]) AS k(scene_key)`, workspaceID, agentID, orgID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, kind string
		if err := rows.Scan(&key, &kind); err != nil {
			return nil, err
		}
		kinds[key] = kind
	}
	return kinds, rows.Err()
}

// GetScene returns one scene of the agent under orgID (see
// ListAgentScenes; identityOrgID is the agent's DingTalk identity org), or
// ErrNotFound when the agent never saw it there.
func GetScene(ctx context.Context, db DBTX, workspaceID, agentID, orgID, identityOrgID, sceneKey string) (SceneSummary, error) {
	if !ValidOpenConversationID(sceneKey) {
		return SceneSummary{}, ErrInvalidInput
	}
	scenes, _, err := ListAgentScenes(ctx, db, SceneListQuery{
		WorkspaceID: workspaceID, AgentID: agentID, OrgID: orgID, IdentityOrgID: identityOrgID, Keys: []string{sceneKey},
	})
	if err != nil {
		return SceneSummary{}, err
	}
	if len(scenes) == 0 {
		return SceneSummary{}, ErrNotFound
	}
	return scenes[0], nil
}

// RegisterDirectScene records a 1:1 conversation as a known dm scene of the
// agent (an agent_scene_config row with an empty prompt) so it is listed and
// configurable before anything else was stored for it. An existing row keeps
// its prompt, kind and update stamp; an empty title is filled in.
func RegisterDirectScene(ctx context.Context, db DBTX, workspaceID, agentID, orgID, sceneKey, title string) error {
	if !ValidOpenConversationID(sceneKey) {
		return ErrInvalidInput
	}
	_, err := db.Exec(ctx, `INSERT INTO agent_scene_config AS c
		(workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title)
		VALUES ($1::uuid, $2::uuid, 'dingtalk', $3, $4, 'dm', $5)
		ON CONFLICT (agent_id, platform, org_id, scene_key)
		DO UPDATE SET scene_title = EXCLUDED.scene_title
		WHERE c.workspace_id = EXCLUDED.workspace_id AND c.scene_title = '' AND EXCLUDED.scene_title <> ''`,
		workspaceID, agentID, orgID, sceneKey, strings.TrimSpace(title))
	return err
}
