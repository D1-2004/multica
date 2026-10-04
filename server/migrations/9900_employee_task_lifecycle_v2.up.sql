-- Lifecycle v2 separates goal completion from one Run's terminal state.
-- Existing and old-binary rows keep the accepted v1/single_run contract through
-- the column defaults; no row is ever backfilled to v2. The full state and
-- entry-kind lists are rewritten here because P1 owns both CHECKs.
ALTER TABLE employee_task ADD COLUMN IF NOT EXISTS lifecycle_version smallint NOT NULL DEFAULT 1;
ALTER TABLE employee_task ADD COLUMN IF NOT EXISTS completion_mode text NOT NULL DEFAULT 'single_run';
ALTER TABLE employee_task ADD COLUMN IF NOT EXISTS autonomous_rounds integer NOT NULL DEFAULT 0;

ALTER TABLE employee_task
    DROP CONSTRAINT IF EXISTS employee_task_state_check,
    ADD CONSTRAINT employee_task_state_check CHECK (state IN ('ready', 'running', 'waiting', 'succeeded', 'failed', 'cancelled')),
    DROP CONSTRAINT IF EXISTS employee_task_lifecycle_check,
    ADD CONSTRAINT employee_task_lifecycle_check CHECK (
        (lifecycle_version = 1 AND completion_mode = 'single_run' AND state <> 'waiting') OR
        (lifecycle_version = 2 AND completion_mode = 'explicit_goal' AND state <> 'failed'
            AND scope_kind = 'scene' AND owner_loop = 'employee')),
    DROP CONSTRAINT IF EXISTS employee_task_autonomous_rounds_check,
    ADD CONSTRAINT employee_task_autonomous_rounds_check CHECK (autonomous_rounds >= 0);

ALTER TABLE employee_task_entry
    DROP CONSTRAINT IF EXISTS employee_task_entry_kind_check,
    ADD CONSTRAINT employee_task_entry_kind_check CHECK (kind IN ('request', 'input', 'amendment', 'run_started', 'result', 'issue_bound', 'resumed', 'steer', 'writer_fenced', 'wait_opened', 'wait_resolved', 'goal_completed', 'autonomous_round'));
