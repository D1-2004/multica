-- Numbered after 9950 (which creates both tables) so a fresh database applies
-- it in order.
-- A goal waiting for another Task or an external event gets its own episode
-- kind and wording. Both CHECKs are owned by the watchdog and rewritten with
-- the full list. Older binaries never write the new value.
ALTER TABLE employee_watchdog_episode
    DROP CONSTRAINT IF EXISTS employee_watchdog_episode_state_kind_check,
    ADD CONSTRAINT employee_watchdog_episode_state_kind_check CHECK (state_kind IN ('execution_queued', 'execution_running', 'execution_unreachable', 'waiting_inputs', 'scheduled_wait', 'stopping', 'waiting_dependency'));
ALTER TABLE employee_watchdog_notice
    DROP CONSTRAINT IF EXISTS employee_watchdog_notice_kind_check,
    ADD CONSTRAINT employee_watchdog_notice_kind_check CHECK (kind IN ('execution_queued', 'execution_running', 'execution_unreachable', 'waiting_inputs', 'scheduled_wait', 'stopping', 'waiting_dependency'));
