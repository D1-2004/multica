-- A rollback deliberately releases any waiting handoff using the legacy
-- payload. Converge the data while the expanded constraint still permits both
-- states, then restore the original constraint before dropping the columns.
UPDATE task_completion_outbox AS completion
SET available_at = now(),
    updated_at = now()
FROM task_execution_update_outbox AS execution_update
WHERE completion.root_task_id = execution_update.root_task_id
  AND completion.status = 'queued'
  AND completion.available_at = 'infinity'::timestamptz
  AND execution_update.status = 'waiting_result';

UPDATE task_execution_update_outbox
SET status = 'queued',
    result_message_frozen = TRUE,
    updated_at = now()
WHERE status = 'waiting_result';

DROP TRIGGER IF EXISTS trg_hold_task_completion_for_waiting_execution_update
    ON task_completion_outbox;
DROP FUNCTION IF EXISTS hold_task_completion_for_waiting_execution_update();

ALTER TABLE task_execution_update_outbox
    DROP CONSTRAINT ck_task_execution_update_outbox_result_state,
    DROP CONSTRAINT task_execution_update_outbox_status_check,
    ADD CONSTRAINT task_execution_update_outbox_status_check
        CHECK (status IN ('queued', 'delivered', 'dead_letter'));

ALTER TABLE task_execution_update_outbox
    DROP COLUMN IF EXISTS result_message_frozen,
    DROP COLUMN IF EXISTS result_message;
