-- Keep existing append-only resume records while preventing new ones on rollback.
ALTER TABLE employee_task_entry
    DROP CONSTRAINT IF EXISTS employee_task_entry_kind_check,
    ADD CONSTRAINT employee_task_entry_kind_check CHECK (kind IN ('request', 'input', 'amendment', 'run_started', 'result', 'issue_bound')) NOT VALID;
