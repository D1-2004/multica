-- A decision occurrence (employee_decide) is admitted with its EmployeeTask
-- and a routine.decision wake instead of a Run and queue row. Rewrites the
-- whole dispatch-mode, state and identity checks; earlier rows stay valid.
ALTER TABLE employee_routine_occurrence DROP CONSTRAINT IF EXISTS employee_routine_occurrence_dispatch_mode_check;
ALTER TABLE employee_routine_occurrence ADD CONSTRAINT employee_routine_occurrence_dispatch_mode_check CHECK (dispatch_mode IN ('employee_direct', 'employee_decide'));
ALTER TABLE employee_routine_occurrence DROP CONSTRAINT IF EXISTS employee_routine_occurrence_state_check;
ALTER TABLE employee_routine_occurrence ADD CONSTRAINT employee_routine_occurrence_state_check CHECK (state IN ('accepted', 'decision', 'skipped', 'skipped_overlap', 'failed'));
ALTER TABLE employee_routine_occurrence DROP CONSTRAINT IF EXISTS employee_routine_occurrence_check1;
ALTER TABLE employee_routine_occurrence ADD CONSTRAINT employee_routine_occurrence_check1 CHECK (
    (state = 'accepted' AND dispatch_mode = 'employee_direct' AND employee_task_id IS NOT NULL AND employee_run_id IS NOT NULL AND queue_task_id IS NOT NULL
     AND reason = '' AND prompt_sha256 <> '' AND creator_kind <> '' AND creator_id IS NOT NULL)
 OR (state = 'decision' AND dispatch_mode = 'employee_decide' AND employee_task_id IS NOT NULL AND employee_run_id IS NULL AND queue_task_id IS NULL
     AND reason = '' AND prompt_sha256 <> '' AND creator_kind <> '' AND creator_id IS NOT NULL)
 OR (state NOT IN ('accepted', 'decision') AND employee_task_id IS NULL AND employee_run_id IS NULL AND queue_task_id IS NULL AND reason <> ''));
