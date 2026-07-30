-- Roll back migration 252 by restoring migration 203's cancellation snapshot.
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
