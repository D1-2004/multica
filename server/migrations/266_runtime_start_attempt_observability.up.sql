-- Runtime startup is an additive, negotiated protocol. Legacy Runtime images
-- never write startup events, but their claim finalization still completes the
-- attempt created by the launcher.
CREATE TABLE IF NOT EXISTS agent_task_runtime_start_attempt (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL REFERENCES agent_runtime(id) ON DELETE CASCADE,
    backend TEXT NOT NULL CHECK (backend IN ('aliyun_fc', 'asb')),
    protocol TEXT NOT NULL CHECK (protocol IN ('legacy-v1', 'http-json-v1')),
    sandbox_id TEXT NOT NULL DEFAULT '',
    cold_start BOOLEAN,
    status TEXT NOT NULL DEFAULT 'starting' CHECK (
        status IN ('starting', 'blocked', 'claimed', 'failed', 'timed_out', 'superseded')
    ),
    last_stage TEXT NOT NULL DEFAULT 'launch_started',
    error_code TEXT NOT NULL DEFAULT '',
    error_detail TEXT NOT NULL DEFAULT '',
    runner_started_at TIMESTAMPTZ,
    daemon_started_at TIMESTAMPTZ,
    claim_finalized_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agent_task_runtime_start_attempt_task
    ON agent_task_runtime_start_attempt (task_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_agent_task_runtime_start_attempt_active
    ON agent_task_runtime_start_attempt (runtime_id, created_at)
    WHERE status = 'starting';

CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_task_runtime_start_attempt_active_task
    ON agent_task_runtime_start_attempt (task_id)
    WHERE status = 'starting';
