-- Routine tasks (例行任务) of one Agent work scene (docs/agent-scene.md): a
-- cron or webhook trigger that runs the agent in a group or 1:1 chat scene
-- with that scene's configuration. The autopilot row carries the schedule,
-- trigger and run history; this row binds it to the scene and freezes what
-- the Host needs to post the start and end notices there.
-- No foreign keys; the workspace deletion sweep removes rows explicitly.
CREATE TABLE IF NOT EXISTS context_scope_routine (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scene_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_kind text NOT NULL,
    autopilot_id uuid NOT NULL,
    -- The 1:1 counterpart's openDingTalkId, frozen at creation (a dm send
    -- needs it); '' for a group.
    delivery_open_dingtalk_id text NOT NULL DEFAULT '',
    -- The 1:1 counterpart's staffId when the creation proved it; the run then
    -- carries that person's capability layer. '' otherwise and for groups.
    person_staff_id text NOT NULL DEFAULT '',
    -- Normalized purpose + schedule + timezone: a re-registration of the same
    -- routine in the same scene updates it instead of adding a second one.
    dedupe_key text NOT NULL,
    created_by_type text NOT NULL,
    created_by_id uuid,
    created_task_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_scope_routine_kind_check CHECK (scene_kind IN ('group', 'dm')),
    CONSTRAINT context_scope_routine_created_by_type_check CHECK (created_by_type IN ('member', 'agent'))
);
