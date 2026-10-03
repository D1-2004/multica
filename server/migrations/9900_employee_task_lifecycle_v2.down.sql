-- Keep append-only ledger records while preventing new v2 kinds and states on rollback.
ALTER TABLE employee_task_entry
    DROP CONSTRAINT IF EXISTS employee_task_entry_kind_check,
    ADD CONSTRAINT employee_task_entry_kind_check CHECK (kind IN ('request', 'input', 'amendment', 'run_started', 'result', 'issue_bound', 'resumed', 'steer', 'writer_fenced')) NOT VALID;

ALTER TABLE employee_task
    DROP CONSTRAINT IF EXISTS employee_task_autonomous_rounds_check,
    DROP CONSTRAINT IF EXISTS employee_task_lifecycle_check,
    DROP CONSTRAINT IF EXISTS employee_task_state_check,
    ADD CONSTRAINT employee_task_state_check CHECK (state IN ('ready', 'running', 'succeeded', 'failed', 'cancelled')) NOT VALID;

ALTER TABLE employee_task DROP COLUMN IF EXISTS autonomous_rounds;
ALTER TABLE employee_task DROP COLUMN IF EXISTS completion_mode;
ALTER TABLE employee_task DROP COLUMN IF EXISTS lifecycle_version;
