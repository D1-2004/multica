-- Task wake rows cannot satisfy the original count constraint; keep them
-- readable as history by restoring the old CHECK without validating it.
ALTER TABLE employee_scene_job
    DROP CONSTRAINT IF EXISTS employee_scene_job_message_count_check,
    ADD CONSTRAINT employee_scene_job_message_count_check CHECK (message_count BETWEEN 1 AND 32) NOT VALID;

ALTER TABLE employee_scene_job DROP CONSTRAINT IF EXISTS employee_scene_job_kind_check;
ALTER TABLE employee_scene_job DROP COLUMN IF EXISTS kind;
