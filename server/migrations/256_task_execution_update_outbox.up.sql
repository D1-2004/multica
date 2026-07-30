-- Migration 256: persist delegated task execution updates.
CREATE TABLE IF NOT EXISTS task_execution_update_outbox (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    root_task_id        UUID NOT NULL,
    target_task_id      UUID NOT NULL,
    issue_id            UUID NOT NULL,
    issue_identifier    TEXT NOT NULL,
    callback_url        TEXT NOT NULL,
    target_identity     TEXT NOT NULL,
    request_id          TEXT NOT NULL,
    agent_id            UUID NOT NULL,
    target_agent_id     UUID NOT NULL,
    update_type         TEXT NOT NULL CHECK (update_type IN ('delegated_to_issue')),
    occurred_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    status              TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'delivered', 'dead_letter')),
    available_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    lease_token         UUID,
    lease_expires_at    TIMESTAMPTZ,
    last_error          TEXT,
    delivered_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_task_execution_update_outbox_root UNIQUE (root_task_id),
    CONSTRAINT uq_task_execution_update_outbox_request UNIQUE (request_id),
    CONSTRAINT ck_task_execution_update_outbox_callback_url CHECK (
        callback_url ~ '^/api/v1/dispatch-tasks/[A-Za-z0-9_-]{1,128}/execution-update$'
    ),
    CONSTRAINT ck_task_execution_update_outbox_target_identity CHECK (
        target_identity ~ '^router-target:v1:sha256:[a-f0-9]{64}$'
    )
);

CREATE INDEX IF NOT EXISTS idx_task_execution_update_outbox_queue
    ON task_execution_update_outbox (target_identity, available_at, created_at)
    WHERE status = 'queued';
