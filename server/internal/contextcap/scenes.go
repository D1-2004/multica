package contextcap

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Scene kinds. A DingTalk 1:1 chat is a scene exactly like a group chat for
// configuration (docs/context-capabilities.md §1); runtime resolution still
// applies group scenes only. Scenes are DingTalk IM scenes only (platform
// 'dingtalk').
const (
	SceneKindGroup = "group"
	SceneKindDM    = "dm"
	// MaxScenePrompt is the scene prompt limit in characters (code points).
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

// SceneListQuery selects scenes of one agent under OrgID (the agent's current
// DingTalk org, "" without an identity). Keys, when non-nil, restricts the
// result to those scene keys; such a lookup is bounded by its key set and not
// paged (Limit and Offset are ignored, every matching scene is returned).
// Otherwise ListAgentScenes clamps Limit to 1..200.
type SceneListQuery struct {
	WorkspaceID string
	AgentID     string
	OrgID       string
	Keys        []string
	Limit       int
	Offset      int
}

// ListAgentScenes returns the agent's IM scenes ordered by last activity
// (newest first) and whether more rows follow the page. It is one statement
// over the indexed scene tables plus the agent's Coordinator jobs, which it
// reads through inbound_coordinator_job_agent_conversation_idx (agent_id,
// openConversationId): the whole agent for a page, one conversation per key
// for a Keys lookup.
//
// Org scoping follows scene memory: rows of scene_memory match the agent's
// org, or any org while the agent has no DingTalk identity (scene memory then
// records the dispatch's DWS org); configuration rows and bindings match the
// org exactly; Coordinator jobs that recorded a different agent org are
// skipped. Only keys shaped like an openConversationId are returned.
//
// The inbound session is the newest Coordinator chat session of the
// conversation, and InboundCount counts the sessions its transcript shows:
// the ListCoordinatorConversationMessages anchor partition (same endpoint
// namespace, source platform and source type as that session).
//
// Kind is dm only on positive evidence: the newest job's conversation type is
// a 1:1 type (IsDirectConversationType), or, when that job carries no type or
// there is no job, the configuration row says dm (RegisterDirectScene or a
// prompt save). scene_memory.scene_kind is not used: scenememory.KindFromChatType
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
	args := []any{q.WorkspaceID, q.AgentID, q.OrgID, limit + 1, offset}
	// A Keys lookup filters every source by key inside its own scan, as a
	// plain predicate (never "$n IS NULL OR ..."), so a cached generic plan
	// still probes the indexes by key.
	memKeys, jobKeys, cfgKeys, bindKeys, credKeys := "", "", "", "", ""
	if q.Keys != nil {
		args = append(args, q.Keys)
		memKeys = ` AND m.scene_key = ANY($6::text[])`
		jobKeys = ` AND BTRIM(job.command #>> '{event,data,conversation,openConversationId}') = ANY($6::text[])`
		cfgKeys = ` AND c.scene_key = ANY($6::text[])`
		bindKeys = ` AND b.scope_key = ANY($6::text[])`
		credKeys = ` AND k.scope_key = ANY($6::text[])`
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
		    AND ($3::text = '' OR COALESCE(NULLIF(BTRIM(job.command #>> '{externalIdentity,dws,orgId}'), ''), $3::text) = $3::text)
		    AND NULLIF(BTRIM(job.command #>> '{event,data,conversation,openConversationId}'), '') IS NOT NULL
		), conv AS (
		  SELECT DISTINCT ON (jobs.scene_key) jobs.scene_key, jobs.conversation_type, jobs.conversation_title, jobs.sender_name,
		    jobs.session_id::text AS session_id, jobs.updated_at,
		    count(*) OVER (PARTITION BY jobs.scene_key, jobs.endpoint_namespace_id, jobs.source_platform, jobs.source_type) AS session_count
		  FROM jobs
		  ORDER BY jobs.scene_key, jobs.updated_at DESC, jobs.session_id DESC
		), cfg AS (
		  SELECT c.scene_key, c.scene_kind, c.scene_title, c.prompt <> '' AS has_prompt, c.updated_at
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
		), keys AS (
		  SELECT scene_key FROM mem UNION SELECT scene_key FROM conv UNION SELECT scene_key FROM cfg UNION SELECT scene_key FROM bind
		  UNION SELECT scene_key FROM cred
		)
		SELECT k.scene_key,
		  CASE WHEN NULLIF(BTRIM(conv.conversation_type), '') IS NOT NULL
		    THEN CASE WHEN lower(BTRIM(conv.conversation_type)) IN ('single', 'p2p', 'private', 'direct') THEN 'dm' ELSE 'group' END
		    ELSE COALESCE(cfg.scene_kind, 'group') END,
		  COALESCE(mem.org_id, $3::text),
		  GREATEST(mem.updated_at, conv.updated_at, cfg.updated_at, bind.updated_at, cred.updated_at),
		  COALESCE(conv.session_id, ''), COALESCE(conv.session_count, 0),
		  COALESCE(mem.memory_id, ''), COALESCE(cfg.has_prompt, FALSE),
		  COALESCE(mem.scene_title, ''), COALESCE(mem.memory_text, ''),
		  COALESCE(conv.conversation_title, ''), COALESCE(conv.sender_name, ''),
		  COALESCE(cfg.scene_title, ''), COALESCE(bind.scope_title, '')
		FROM keys k
		LEFT JOIN mem ON mem.scene_key = k.scene_key
		LEFT JOIN conv ON conv.scene_key = k.scene_key
		LEFT JOIN cfg ON cfg.scene_key = k.scene_key
		LEFT JOIN bind ON bind.scene_key = k.scene_key
		LEFT JOIN cred ON cred.scene_key = k.scene_key
		WHERE k.scene_key LIKE 'cid%' AND octet_length(k.scene_key) <= 256 AND k.scene_key !~ '[[:space:][:cntrl:]]'
		ORDER BY 4 DESC NULLS LAST, k.scene_key
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

// GetScene returns one scene of the agent (see ListAgentScenes), or
// ErrNotFound when the agent never saw it.
func GetScene(ctx context.Context, db DBTX, workspaceID, agentID, orgID, sceneKey string) (SceneSummary, error) {
	if !ValidOpenConversationID(sceneKey) {
		return SceneSummary{}, ErrInvalidInput
	}
	scenes, _, err := ListAgentScenes(ctx, db, SceneListQuery{
		WorkspaceID: workspaceID, AgentID: agentID, OrgID: orgID, Keys: []string{sceneKey},
	})
	if err != nil {
		return SceneSummary{}, err
	}
	if len(scenes) == 0 {
		return SceneSummary{}, ErrNotFound
	}
	return scenes[0], nil
}

// SceneConfig is one row of agent_scene_config.
type SceneConfig struct {
	WorkspaceID string
	AgentID     string
	Platform    string
	OrgID       string
	SceneKey    string
	SceneKind   string
	SceneTitle  string
	Prompt      string
	// UpdatedBy is the user who last wrote the prompt ("" when the row was
	// only registered, for example by a 1:1 link redemption).
	UpdatedBy     string
	UpdatedByName string
	UpdatedAt     time.Time
}

const sceneConfigColumns = `c.workspace_id::text, c.agent_id::text, c.platform, c.org_id, c.scene_key, c.scene_kind, c.scene_title, c.prompt,
	COALESCE(c.updated_by::text, ''), COALESCE((SELECT u.name FROM "user" u WHERE u.id = c.updated_by), ''), c.updated_at`

func scanSceneConfig(row pgx.Row) (SceneConfig, error) {
	var c SceneConfig
	err := row.Scan(&c.WorkspaceID, &c.AgentID, &c.Platform, &c.OrgID, &c.SceneKey, &c.SceneKind, &c.SceneTitle, &c.Prompt,
		&c.UpdatedBy, &c.UpdatedByName, &c.UpdatedAt)
	return c, err
}

// GetSceneConfig loads the configuration of one scene, or ErrNotFound.
func GetSceneConfig(ctx context.Context, db DBTX, workspaceID, agentID, orgID, sceneKey string) (SceneConfig, error) {
	out, err := scanSceneConfig(db.QueryRow(ctx, `SELECT `+sceneConfigColumns+`
		FROM agent_scene_config c
		WHERE c.workspace_id = $1::uuid AND c.agent_id = $2::uuid AND c.platform = 'dingtalk' AND c.org_id = $3 AND c.scene_key = $4`,
		workspaceID, agentID, orgID, sceneKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return SceneConfig{}, ErrNotFound
	}
	return out, err
}

// SceneConfigWrite is the input of UpsertScenePrompt. ActorID is required.
type SceneConfigWrite struct {
	WorkspaceID string
	AgentID     string
	OrgID       string
	SceneKey    string
	SceneKind   string
	SceneTitle  string
	Prompt      string
	ActorID     string
}

// UpsertScenePrompt stores the scene prompt of one scene with the scene's
// kind and title snapshot. The prompt must already be trimmed and pass
// ValidScenePrompt. A row of another workspace is never overwritten
// (ErrNotFound).
func UpsertScenePrompt(ctx context.Context, db DBTX, in SceneConfigWrite) (SceneConfig, error) {
	if !ValidOpenConversationID(in.SceneKey) || (in.SceneKind != SceneKindGroup && in.SceneKind != SceneKindDM) || !ValidScenePrompt(in.Prompt) {
		return SceneConfig{}, ErrInvalidInput
	}
	actor, err := canonicalUUID(in.ActorID)
	if err != nil {
		return SceneConfig{}, err
	}
	out, err := scanSceneConfig(db.QueryRow(ctx, `INSERT INTO agent_scene_config AS c
		(workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title, prompt, updated_by)
		VALUES ($1::uuid, $2::uuid, 'dingtalk', $3, $4, $5, $6, $7, $8::uuid)
		ON CONFLICT (agent_id, platform, org_id, scene_key)
		DO UPDATE SET prompt = EXCLUDED.prompt, scene_kind = EXCLUDED.scene_kind,
		  scene_title = CASE WHEN EXCLUDED.scene_title <> '' THEN EXCLUDED.scene_title ELSE c.scene_title END,
		  updated_by = EXCLUDED.updated_by, updated_at = now()
		WHERE c.workspace_id = EXCLUDED.workspace_id
		RETURNING `+sceneConfigColumns,
		in.WorkspaceID, in.AgentID, in.OrgID, in.SceneKey, in.SceneKind, in.SceneTitle, in.Prompt, actor))
	if errors.Is(err, pgx.ErrNoRows) {
		return SceneConfig{}, ErrNotFound
	}
	return out, err
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
