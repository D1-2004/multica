-- Host-owned verification of Employee Task Runs (G1). Relationships to
-- employee_task / employee_task_run are application-owned: no FK, no cascade.
-- Indexes are built CONCURRENTLY in the following single-statement files.

-- Verification contract revisions per Task; the highest revision is current.
-- Checks come only from Host derivation of the requester's own words,
-- automation configuration or Host fixtures; model proposals stay 'proposed'
-- until the requester confirms them.
CREATE TABLE IF NOT EXISTS employee_task_verification_spec (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    task_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    origin text NOT NULL CHECK (origin IN ('human_cue', 'automation_config', 'host_fixture', 'model_proposed')),
    state text NOT NULL CHECK (state IN ('active', 'proposed')),
    source_ref text NOT NULL CHECK (char_length(source_ref) BETWEEN 1 AND 512),
    author_ref text NOT NULL CHECK (char_length(author_ref) BETWEEN 1 AND 256),
    learning_scope text NOT NULL DEFAULT '' CHECK (learning_scope IN ('', 'scene', 'none')),
    checks jsonb NOT NULL CHECK (jsonb_typeof(checks) = 'array' AND pg_column_size(checks) <= 32768),
    spec_digest text NOT NULL CHECK (spec_digest ~ '^[0-9a-f]{64}$'),
    confirmed_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One immutable result per (run, check, evidence). check_kind is deliberately
-- unconstrained so a kind written by a newer binary is recorded as failed
-- rather than rejected.
CREATE TABLE IF NOT EXISTS employee_task_verification (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    scene_id uuid NOT NULL,
    task_id uuid NOT NULL,
    run_id uuid NOT NULL,
    queue_task_id uuid NOT NULL,
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    requester_ref text NOT NULL,
    spec_revision bigint NOT NULL CHECK (spec_revision > 0),
    spec_digest text NOT NULL CHECK (spec_digest ~ '^[0-9a-f]{64}$'),
    check_id text NOT NULL CHECK (char_length(check_id) BETWEEN 1 AND 64),
    check_kind text NOT NULL CHECK (char_length(check_kind) BETWEEN 1 AND 64),
    check_spec jsonb NOT NULL CHECK (jsonb_typeof(check_spec) = 'object' AND pg_column_size(check_spec) <= 8192),
    evidence_ref text NOT NULL CHECK (char_length(evidence_ref) BETWEEN 1 AND 512),
    evidence_sha256 text NOT NULL CHECK (evidence_sha256 ~ '^[0-9a-f]{64}$'),
    checker_version text NOT NULL CHECK (char_length(checker_version) BETWEEN 1 AND 64),
    outcome text NOT NULL CHECK (outcome IN ('passed', 'failed', 'unknown')),
    detail text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 4000),
    run_finished_at timestamptz NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT now()
);

-- Durable distill intent written in the same transaction as a passing
-- verification set; the background consumer marks it captured or skipped.
CREATE TABLE IF NOT EXISTS employee_task_verified_distill (
    run_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    scene_id uuid NOT NULL,
    task_id uuid NOT NULL,
    queue_task_id uuid NOT NULL,
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    requester_ref text NOT NULL,
    spec_revision bigint NOT NULL CHECK (spec_revision > 0),
    spec_digest text NOT NULL CHECK (spec_digest ~ '^[0-9a-f]{64}$'),
    learning_scope text NOT NULL DEFAULT '' CHECK (learning_scope IN ('', 'scene', 'none')),
    verification_ids jsonb NOT NULL CHECK (jsonb_typeof(verification_ids) = 'array'),
    run_finished_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'captured', 'skipped')),
    learning_id uuid,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz,
    CHECK ((state = 'pending' AND learning_id IS NULL AND reason = '' AND consumed_at IS NULL)
        OR (state = 'captured' AND learning_id IS NOT NULL AND reason = '' AND consumed_at IS NOT NULL)
        OR (state = 'skipped' AND learning_id IS NULL AND reason <> '' AND consumed_at IS NOT NULL))
);
