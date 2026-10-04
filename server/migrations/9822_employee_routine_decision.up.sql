-- The mutable decision state of one employee_decide occurrence; the frozen
-- receipt (employee_routine_occurrence) is never rewritten. pending until the
-- routine.decision wake (job_id) settles it: quiet, waited and replied end
-- without a Run; dispatched names the one Run and queue row it started;
-- failed records a decision that could not be made.
-- No foreign keys; workspace deletion removes rows explicitly.
CREATE TABLE IF NOT EXISTS employee_routine_decision (
 occurrence_id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 routine_id uuid NOT NULL,
 autopilot_run_id uuid NOT NULL,
 employee_task_id uuid NOT NULL,
 job_id uuid NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'quiet', 'waited', 'replied', 'dispatched', 'failed')),
 reason text NOT NULL DEFAULT '',
 employee_run_id uuid,
 queue_task_id uuid,
 decided_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((state = 'dispatched') = (employee_run_id IS NOT NULL AND queue_task_id IS NOT NULL)),
 CHECK ((state = 'pending') = (decided_at IS NULL))
);
