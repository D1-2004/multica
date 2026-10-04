-- Task Service steer records the correction ('steer') and host evidence that a
-- failed or cancelled writer can no longer race a successor ('writer_fenced').
-- Widening the check is safe for older binaries, which never write these kinds.
ALTER TABLE employee_task_entry
    DROP CONSTRAINT IF EXISTS employee_task_entry_kind_check,
    ADD CONSTRAINT employee_task_entry_kind_check CHECK (kind IN ('request', 'input', 'amendment', 'run_started', 'result', 'issue_bound', 'resumed', 'steer', 'writer_fenced'));
