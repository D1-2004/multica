-- Association graph: Issue-backed Task, Events, Scenes, People, and typed edges.
-- No PRIMARY KEY / FOREIGN KEY: indexes are created CONCURRENTLY in follow-up files.

CREATE TABLE IF NOT EXISTS assoc_task (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    purpose TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'waiting', 'done', 'cancelled')),
    intent TEXT NOT NULL DEFAULT '',
    props JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(props) = 'object' AND pg_column_size(props) <= 8192),
    run_id UUID,
    last_touched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT assoc_task_purpose_len CHECK (char_length(btrim(purpose)) >= 8)
);

CREATE TABLE IF NOT EXISTS assoc_event (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    source TEXT NOT NULL
        CHECK (source IN ('inbound_im', 'outbound_im', 'approval', 'calendar', 'aitable', 'tool')),
    direction TEXT NOT NULL
        CHECK (direction IN ('inbound', 'outbound')),
    evidence_id TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    scene_key TEXT NOT NULL DEFAULT '',
    person_key TEXT NOT NULL DEFAULT '',
    task_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS assoc_edge (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    src_type TEXT NOT NULL
        CHECK (src_type IN ('person', 'scene', 'task', 'event', 'issue')),
    src_id TEXT NOT NULL,
    dst_type TEXT NOT NULL
        CHECK (dst_type IN ('person', 'scene', 'task', 'event', 'issue')),
    dst_id TEXT NOT NULL,
    rel TEXT NOT NULL
        CHECK (rel IN ('task_issue', 'task_scene', 'task_person', 'outreach', 'waiting_on', 'spawned_from', 'event_of')),
    status TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'dormant', 'closed')),
    props JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(props) = 'object' AND pg_column_size(props) <= 8192),
    opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_touched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at TIMESTAMPTZ,
    opened_by_run_id UUID
);

CREATE TABLE IF NOT EXISTS assoc_scene (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    scene_key TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'dm'
        CHECK (kind IN ('dm', 'group', 'aitable', 'approval', 'doc')),
    last_touched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS assoc_person (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    person_key TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS assoc_person_alias (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    person_key TEXT NOT NULL,
    alias_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
