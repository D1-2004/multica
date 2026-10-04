-- One row per digest claim: the page it read, the durable model journal
-- (reservation before I/O, response after) that makes a replay generate
-- nothing, and the deterministic validation result. Indexes follow.
CREATE TABLE IF NOT EXISTS employee_scene_digest_run (
    id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    scene_id uuid NOT NULL,
    lease_generation bigint NOT NULL CHECK (lease_generation > 0),
    trigger text NOT NULL CHECK (trigger IN ('segment_close', 'threshold', 'catch_up')),
    page_hash text NOT NULL DEFAULT '' CHECK (page_hash = '' OR page_hash ~ '^[0-9a-f]{64}$'),
    cursor_from_at timestamptz,
    cursor_from_id text NOT NULL DEFAULT '' CHECK (char_length(cursor_from_id) <= 256),
    cursor_to_at timestamptz,
    cursor_to_id text NOT NULL DEFAULT '' CHECK (char_length(cursor_to_id) <= 256),
    page_messages integer NOT NULL DEFAULT 0 CHECK (page_messages >= 0),
    page_human integer NOT NULL DEFAULT 0 CHECK (page_human >= 0),
    model text NOT NULL DEFAULT '' CHECK (char_length(model) <= 256),
    calls smallint NOT NULL DEFAULT 0 CHECK (calls BETWEEN 0 AND 2),
    model_journal jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(model_journal) = 'array' AND octet_length(model_journal::text) <= 131072),
    prompt_tokens integer NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens integer NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    ops_proposed integer NOT NULL DEFAULT 0 CHECK (ops_proposed >= 0),
    ops_accepted integer NOT NULL DEFAULT 0 CHECK (ops_accepted >= 0),
    ops_rejected jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(ops_rejected) = 'array' AND octet_length(ops_rejected::text) <= 4096),
    outcome text CHECK (outcome IS NULL OR outcome IN ('committed', 'no_change', 'rejected', 'timeout', 'error', 'budget_exhausted', 'blocked', 'skipped_gap', 'skipped_trivial')),
    error text NOT NULL DEFAULT '' CHECK (char_length(error) <= 512),
    langfuse_trace_id text NOT NULL DEFAULT '' CHECK (char_length(langfuse_trace_id) <= 64),
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CONSTRAINT employee_scene_digest_run_finished_check CHECK ((outcome IS NULL) = (finished_at IS NULL))
);
