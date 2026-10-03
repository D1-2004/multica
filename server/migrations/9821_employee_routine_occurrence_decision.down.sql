DELETE FROM employee_routine_occurrence WHERE state = 'decision' OR dispatch_mode = 'employee_decide';
ALTER TABLE employee_routine_occurrence DROP CONSTRAINT IF EXISTS employee_routine_occurrence_check1;
ALTER TABLE employee_routine_occurrence ADD CONSTRAINT employee_routine_occurrence_check1 CHECK (
    (state = 'accepted' AND employee_task_id IS NOT NULL AND employee_run_id IS NOT NULL AND queue_task_id IS NOT NULL
     AND reason = '' AND prompt_sha256 <> '' AND creator_kind <> '' AND creator_id IS NOT NULL)
 OR (state <> 'accepted' AND employee_task_id IS NULL AND employee_run_id IS NULL AND queue_task_id IS NULL AND reason <> ''));
ALTER TABLE employee_routine_occurrence DROP CONSTRAINT IF EXISTS employee_routine_occurrence_state_check;
ALTER TABLE employee_routine_occurrence ADD CONSTRAINT employee_routine_occurrence_state_check CHECK (state IN ('accepted', 'skipped', 'skipped_overlap', 'failed'));
ALTER TABLE employee_routine_occurrence DROP CONSTRAINT IF EXISTS employee_routine_occurrence_dispatch_mode_check;
ALTER TABLE employee_routine_occurrence ADD CONSTRAINT employee_routine_occurrence_dispatch_mode_check CHECK (dispatch_mode = 'employee_direct');
