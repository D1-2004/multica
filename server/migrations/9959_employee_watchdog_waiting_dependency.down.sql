ALTER TABLE employee_watchdog_episode
    DROP CONSTRAINT IF EXISTS employee_watchdog_episode_state_kind_check,
    ADD CONSTRAINT employee_watchdog_episode_state_kind_check CHECK (state_kind IN ('execution_queued', 'execution_running', 'execution_unreachable', 'waiting_inputs', 'scheduled_wait', 'stopping'));
ALTER TABLE employee_watchdog_notice
    DROP CONSTRAINT IF EXISTS employee_watchdog_notice_kind_check,
    ADD CONSTRAINT employee_watchdog_notice_kind_check CHECK (kind IN ('execution_queued', 'execution_running', 'execution_unreachable', 'waiting_inputs', 'scheduled_wait', 'stopping'));
