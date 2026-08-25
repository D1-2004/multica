-- Freeze the user-visible DWS reply into the handoff outbox before delivery.
-- Existing and old-binary rows remain queued and immediately consumable. New
-- binaries use waiting_result, which old workers cannot claim because their
-- query only recognizes status = 'queued'.
ALTER TABLE task_execution_update_outbox
    ADD COLUMN IF NOT EXISTS result_message TEXT,
    ADD COLUMN IF NOT EXISTS result_message_frozen BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE task_execution_update_outbox
    DROP CONSTRAINT task_execution_update_outbox_status_check,
    ADD CONSTRAINT task_execution_update_outbox_status_check
        CHECK (status IN ('waiting_result', 'queued', 'delivered', 'dead_letter')),
    ADD CONSTRAINT ck_task_execution_update_outbox_result_state CHECK (
        (status = 'waiting_result' AND result_message_frozen = FALSE)
        OR (status <> 'waiting_result' AND result_message_frozen = TRUE)
    );

-- Old completion workers only understand the existing available_at gate. Hold
-- terminal callbacks behind that gate while their execution update is waiting.
-- FOR UPDATE serializes this INSERT trigger with the source-task freeze.
CREATE OR REPLACE FUNCTION hold_task_completion_for_waiting_execution_update()
RETURNS TRIGGER AS $$
DECLARE
    execution_update_status TEXT;
BEGIN
    IF NEW.root_task_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT status
    INTO execution_update_status
    FROM task_execution_update_outbox
    WHERE root_task_id = NEW.root_task_id
    FOR UPDATE;

    IF execution_update_status = 'waiting_result' THEN
        NEW.available_at := 'infinity'::timestamptz;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_hold_task_completion_for_waiting_execution_update
    ON task_completion_outbox;
CREATE TRIGGER trg_hold_task_completion_for_waiting_execution_update
BEFORE INSERT ON task_completion_outbox
FOR EACH ROW
EXECUTE FUNCTION hold_task_completion_for_waiting_execution_update();
