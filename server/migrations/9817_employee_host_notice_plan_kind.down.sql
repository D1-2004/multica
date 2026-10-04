ALTER TABLE IF EXISTS employee_host_notice
    DROP CONSTRAINT IF EXISTS employee_host_notice_source_kind_check,
    ADD CONSTRAINT employee_host_notice_source_kind_check CHECK (source_kind IN ('task_wake', 'invitation', 'watchdog')) NOT VALID;
