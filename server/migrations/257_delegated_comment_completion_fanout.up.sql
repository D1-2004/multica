-- Migration 257: fan out cancelled Issue-task completion to every delivered
-- delegated comment. The mapping is carried by comment.source_task_id and the
-- claim-time receipt by agent_task_queue.delivered_comment_ids, so no new table
-- is required.
CREATE OR REPLACE FUNCTION enqueue_cancelled_task_completion()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    root_task_id_value UUID;
    root_agent_id_value UUID;
    callback_url_value TEXT;
    target_identity_value TEXT;
    result_message_value TEXT;
    outbox_id_value UUID;
    comment_target RECORD;
    comment_root_task_ids UUID[] := '{}';
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_task_queue child
        WHERE child.parent_task_id = NEW.id
    ) THEN
        RETURN NEW;
    END IF;

    -- A source Chat task with a delegated member comment has transferred
    -- terminal ownership even when the comment is waiting for completion
    -- reconciliation and no child task exists yet.
    IF EXISTS (
        SELECT 1
        FROM comment delegated_input
        WHERE delegated_input.author_type = 'member'
          AND delegated_input.source_task_id = NEW.id
    ) THEN
        RETURN NEW;
    END IF;

    FOR comment_target IN
        WITH delivered AS (
            SELECT unnest(NEW.delivered_comment_ids) AS comment_id
        )
        SELECT DISTINCT ON (source_task.id)
            source_task.id AS root_task_id,
            source_task.agent_id AS root_agent_id,
            COALESCE(source_task.context #>> '{completion_callback,url}', '')::text AS callback_url,
            COALESCE(source_task.context #>> '{completion_callback,target}', '')::text AS target_identity,
            delegated_input.id AS comment_id
        FROM delivered
        JOIN comment delegated_input
          ON delegated_input.id = delivered.comment_id
         AND delegated_input.author_type = 'member'
         AND delegated_input.source_task_id IS NOT NULL
        JOIN agent_task_queue source_task
          ON source_task.id = delegated_input.source_task_id
        WHERE COALESCE(source_task.context #>> '{completion_callback,url}', '') <> ''
          AND COALESCE(source_task.context #>> '{completion_callback,target}', '') <> ''
        ORDER BY source_task.id, delegated_input.created_at DESC, delegated_input.id DESC
    LOOP
        comment_root_task_ids := array_append(
            comment_root_task_ids,
            comment_target.root_task_id
        );

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
        FROM comment reply
        JOIN lineage ON lineage.id = reply.source_task_id
        WHERE reply.author_type = 'agent'
          AND reply.parent_id = comment_target.comment_id
          AND COALESCE(BTRIM(reply.content), '') <> ''
        ORDER BY reply.created_at DESC, reply.id DESC
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
            comment_target.root_task_id,
            NEW.id,
            comment_target.callback_url,
            comment_target.target_identity,
            'multica-comment-terminal:' || comment_target.root_task_id::text,
            comment_target.root_agent_id,
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
                comment_target.root_task_id
                USING ERRCODE = 'unique_violation';
        END IF;
    END LOOP;

    WITH RECURSIVE lineage AS (
        SELECT task.id, task.parent_task_id, task.agent_id, task.context, 0 AS depth
        FROM agent_task_queue task
        WHERE task.id = NEW.id

        UNION ALL

        SELECT parent.id, parent.parent_task_id, parent.agent_id, parent.context, child.depth + 1
        FROM agent_task_queue parent
        JOIN lineage child ON parent.id = child.parent_task_id
    )
    SELECT
        lineage.id,
        lineage.agent_id,
        COALESCE(lineage.context #>> '{completion_callback,url}', ''),
        COALESCE(lineage.context #>> '{completion_callback,target}', '')
    INTO root_task_id_value, root_agent_id_value, callback_url_value, target_identity_value
    FROM lineage
    WHERE lineage.parent_task_id IS NULL
    ORDER BY lineage.depth DESC
    LIMIT 1;

    IF COALESCE(callback_url_value, '') = ''
        OR COALESCE(target_identity_value, '') = ''
        OR root_task_id_value = ANY(comment_root_task_ids)
        OR EXISTS (
            SELECT 1
            FROM comment delegated_input
            WHERE delegated_input.author_type = 'member'
              AND delegated_input.source_task_id = root_task_id_value
        ) THEN
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
        root_agent_id_value,
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
