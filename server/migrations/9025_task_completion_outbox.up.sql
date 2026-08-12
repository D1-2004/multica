CREATE TABLE IF NOT EXISTS agent_dispatch_acceptance (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id          UUID NOT NULL,
    agent_id             UUID NOT NULL,
    target_identity      TEXT NOT NULL,
    idempotency_key      TEXT NOT NULL,
    request_fingerprint  TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted')),
    lease_token          UUID DEFAULT gen_random_uuid(),
    lease_expires_at     TIMESTAMPTZ DEFAULT now() + interval '5 minutes',
    response_status      INTEGER,
    response_content_type TEXT,
    response_body        BYTEA,
    root_task_id         UUID,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_agent_dispatch_acceptance_key UNIQUE (endpoint_id, idempotency_key),
    CONSTRAINT ck_agent_dispatch_acceptance_key CHECK (
        char_length(idempotency_key) BETWEEN 1 AND 256
    ),
    CONSTRAINT ck_agent_dispatch_acceptance_fingerprint CHECK (
        request_fingerprint ~ '^sha256:[a-f0-9]{64}$'
    ),
    CONSTRAINT ck_agent_dispatch_acceptance_target CHECK (
        target_identity ~ '^router-target:v1:sha256:[a-f0-9]{64}$'
    ),
    CONSTRAINT ck_agent_dispatch_acceptance_response CHECK (
        status = 'pending' OR (
            response_status BETWEEN 200 AND 299 AND
            response_body IS NOT NULL
        )
    )
);

CREATE TABLE IF NOT EXISTS task_completion_outbox (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    root_task_id        UUID,
    terminal_task_id    UUID,
    callback_url        TEXT NOT NULL,
    target_identity     TEXT NOT NULL,
    request_id          TEXT NOT NULL,
    agent_id            UUID NOT NULL,
    external_session_id TEXT,
    execution_status    TEXT NOT NULL CHECK (execution_status IN ('completed', 'failed')),
    result_message      TEXT NOT NULL DEFAULT '',
    error               TEXT,
    failure_reason      TEXT,
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
    CONSTRAINT uq_task_completion_outbox_root UNIQUE (root_task_id),
    CONSTRAINT uq_task_completion_outbox_request UNIQUE (request_id),
    CONSTRAINT ck_task_completion_outbox_task_identity CHECK (
        (root_task_id IS NULL AND terminal_task_id IS NULL) OR
        (root_task_id IS NOT NULL AND terminal_task_id IS NOT NULL)
    ),
    CONSTRAINT ck_task_completion_outbox_callback_url CHECK (
        callback_url ~ '^/api/v1/dispatch-tasks/[A-Za-z0-9_-]{1,128}/execution-result$'
    ),
    CONSTRAINT ck_task_completion_outbox_target_identity CHECK (
        target_identity ~ '^router-target:v1:sha256:[a-f0-9]{64}$'
    )
);

CREATE INDEX IF NOT EXISTS idx_task_completion_outbox_queue
    ON task_completion_outbox (target_identity, available_at, created_at)
    WHERE status = 'queued';

-- Cancellation is often immediately followed by deleting the issue, chat
-- session, agent, or runtime that owns the task. Snapshot the terminal result
-- in the same transaction as the status transition, before those cascades can
-- remove the task and its partial replies.
CREATE OR REPLACE FUNCTION enqueue_cancelled_task_completion()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    root_task_id_value UUID;
    callback_url_value TEXT;
    target_identity_value TEXT;
    result_message_value TEXT;
    outbox_id_value UUID;
BEGIN
    WITH RECURSIVE lineage AS (
        SELECT task.id, task.parent_task_id, task.context, 0 AS depth
        FROM agent_task_queue task
        WHERE task.id = NEW.id

        UNION ALL

        SELECT parent.id, parent.parent_task_id, parent.context, child.depth + 1
        FROM agent_task_queue parent
        JOIN lineage child ON parent.id = child.parent_task_id
    )
    SELECT
        lineage.id,
        COALESCE(lineage.context #>> '{completion_callback,url}', ''),
        COALESCE(lineage.context #>> '{completion_callback,target}', '')
    INTO root_task_id_value, callback_url_value, target_identity_value
    FROM lineage
    WHERE lineage.parent_task_id IS NULL
    ORDER BY lineage.depth DESC
    LIMIT 1;

    IF COALESCE(callback_url_value, '') = ''
        OR COALESCE(target_identity_value, '') = '' THEN
        RETURN NEW;
    END IF;

    WITH RECURSIVE lineage AS (
        SELECT task.id, task.parent_task_id
        FROM agent_task_queue task
        WHERE task.id = NEW.id

        UNION ALL

        SELECT parent.id, parent.parent_task_id
        FROM agent_task_queue parent
        JOIN lineage child ON parent.id = child.parent_task_id
    )
    SELECT reply.content
    INTO result_message_value
    FROM (
        SELECT message.content, message.created_at
        FROM task_message message
        JOIN lineage ON lineage.id = message.task_id
        WHERE message.type = 'text'
          AND COALESCE(BTRIM(message.content), '') <> ''

        UNION ALL

        SELECT task_comment.content, task_comment.created_at
        FROM comment task_comment
        JOIN lineage ON lineage.id = task_comment.source_task_id
        WHERE task_comment.author_type = 'agent'
          AND COALESCE(BTRIM(task_comment.content), '') <> ''
    ) AS reply
    ORDER BY reply.created_at DESC
    LIMIT 1;

    INSERT INTO task_completion_outbox AS existing (
        root_task_id,
        terminal_task_id,
        callback_url,
        target_identity,
        request_id,
        agent_id,
        external_session_id,
        execution_status,
        result_message,
        error,
        failure_reason
    ) VALUES (
        root_task_id_value,
        NEW.id,
        callback_url_value,
        target_identity_value,
        'multica-terminal:' || root_task_id_value::text,
        NEW.agent_id,
        NEW.session_id,
        'failed',
        COALESCE(result_message_value, ''),
        'task cancelled',
        'cancelled'
    )
    ON CONFLICT (root_task_id) DO UPDATE
    SET updated_at = existing.updated_at
    WHERE existing.terminal_task_id = EXCLUDED.terminal_task_id
      AND existing.callback_url = EXCLUDED.callback_url
      AND existing.target_identity = EXCLUDED.target_identity
      AND existing.request_id = EXCLUDED.request_id
      AND existing.agent_id = EXCLUDED.agent_id
      AND existing.external_session_id IS NOT DISTINCT FROM EXCLUDED.external_session_id
      AND existing.execution_status = EXCLUDED.execution_status
      AND existing.result_message = EXCLUDED.result_message
      AND existing.error IS NOT DISTINCT FROM EXCLUDED.error
      AND existing.failure_reason IS NOT DISTINCT FROM EXCLUDED.failure_reason
    RETURNING id INTO outbox_id_value;

    IF NOT FOUND THEN
        RAISE EXCEPTION
            'cancelled task completion conflicts with existing terminal result for root task %',
            root_task_id_value
            USING ERRCODE = 'unique_violation';
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_agent_task_cancelled_completion ON agent_task_queue;
CREATE TRIGGER trg_agent_task_cancelled_completion
AFTER UPDATE OF status ON agent_task_queue
FOR EACH ROW
WHEN (
    OLD.status IS DISTINCT FROM 'cancelled'
    AND NEW.status = 'cancelled'
)
EXECUTE FUNCTION enqueue_cancelled_task_completion();
