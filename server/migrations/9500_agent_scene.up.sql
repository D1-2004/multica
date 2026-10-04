-- The Agent work scene (docs/agent-scene.md): one persisted scene_id for one
-- agent in one tenant org in one scene instance, a DingTalk group or 1:1
-- conversation by its stable openConversationId, or the enterprise itself.
-- The association graph, scene configuration, scene memory, Coordinator
-- jobs, task context and outbound targets all reference this id; nothing
-- else is a scene identity.
--
-- The natural key is (workspace_id, agent_id, provider, tenant_org_id,
-- source_namespace, scene_kind, external_scene_id). One external id carries
-- one kind, so uniqueness is enforced without scene_kind and a kind mismatch
-- is a conflict the resolver rejects. No PRIMARY KEY / FOREIGN KEY: indexes
-- are created CONCURRENTLY in follow-up files.
CREATE TABLE IF NOT EXISTS agent_scene (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    provider text NOT NULL,
    tenant_org_id text NOT NULL,
    source_namespace text NOT NULL,
    scene_kind text NOT NULL,
    external_scene_id text NOT NULL,
    title text NOT NULL DEFAULT '',
    last_active_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_scene_provider_check CHECK (provider IN ('dingtalk')),
    CONSTRAINT agent_scene_kind_check CHECK (scene_kind IN ('group', 'dm', 'enterprise')),
    CONSTRAINT agent_scene_tenant_check CHECK (
        char_length(tenant_org_id) BETWEEN 1 AND 128 AND tenant_org_id = btrim(tenant_org_id)),
    CONSTRAINT agent_scene_namespace_check CHECK (
        char_length(source_namespace) BETWEEN 1 AND 64 AND source_namespace = btrim(source_namespace)),
    CONSTRAINT agent_scene_external_check CHECK (
        char_length(external_scene_id) BETWEEN 1 AND 256 AND external_scene_id = btrim(external_scene_id)),
    CONSTRAINT agent_scene_title_check CHECK (char_length(title) <= 256)
);
