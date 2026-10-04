-- Keep historical human responses readable without validating the old lists.
ALTER TABLE employee_scene_job
    DROP CONSTRAINT IF EXISTS employee_scene_job_kind_check,
    ADD CONSTRAINT employee_scene_job_kind_check CHECK (kind IN ('message', 'task_wake')) NOT VALID,
    DROP CONSTRAINT IF EXISTS employee_scene_job_message_count_check,
    ADD CONSTRAINT employee_scene_job_message_count_check CHECK (
        (kind = 'message' AND message_count BETWEEN 1 AND 32) OR
        (kind = 'task_wake' AND message_count = 0)) NOT VALID;
