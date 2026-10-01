-- Per-scene configuration of an agent: the scene prompt (场域提示词) of one
-- DingTalk group chat or 1:1 chat. Scene identity is (agent, platform, org_id,
-- scene_key = openConversationId), the same identity as scene_memory and the
-- scene layer of context_capability_binding. Configuration only: the prompt
-- is stored and shown, not yet applied at runtime. No foreign keys; the
-- workspace deletion sweep removes rows explicitly.
CREATE TABLE IF NOT EXISTS agent_scene_config (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    platform text NOT NULL DEFAULT 'dingtalk',
    org_id text NOT NULL DEFAULT '',
    scene_key text NOT NULL,
    scene_kind text NOT NULL,
    scene_title text NOT NULL DEFAULT '',
    prompt text NOT NULL DEFAULT '',
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_scene_config_scene_kind_check CHECK (scene_kind IN ('group', 'dm'))
);
