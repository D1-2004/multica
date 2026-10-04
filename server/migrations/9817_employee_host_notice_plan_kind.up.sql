-- Plan pause notes are Host-initiated scene messages too. The complete list
-- is rewritten by its owner. 9913 (which runs later on a fresh database)
-- already carries the same list; this upgrades databases that applied 9913.
ALTER TABLE IF EXISTS employee_host_notice
    DROP CONSTRAINT IF EXISTS employee_host_notice_source_kind_check,
    ADD CONSTRAINT employee_host_notice_source_kind_check CHECK (source_kind IN ('task_wake', 'invitation', 'watchdog', 'task_plan'));
