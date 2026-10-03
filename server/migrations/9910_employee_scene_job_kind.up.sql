-- Typed scene jobs. A constant default is a metadata-only change, so existing
-- rows and binaries that do not name a kind keep writing 'message'.
-- The CHECKs are installed NOT VALID (a brief lock without a table scan); 9911
-- validates them under SHARE UPDATE EXCLUSIVE. Only the employee entry owner
-- rewrites these lists, always as the complete set.
ALTER TABLE employee_scene_job ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'message';

ALTER TABLE employee_scene_job
    DROP CONSTRAINT IF EXISTS employee_scene_job_kind_check,
    ADD CONSTRAINT employee_scene_job_kind_check CHECK (kind IN ('message', 'task_wake')) NOT VALID;

-- A message window carries 1-32 human messages; a task wake carries none.
ALTER TABLE employee_scene_job
    DROP CONSTRAINT IF EXISTS employee_scene_job_message_count_check,
    ADD CONSTRAINT employee_scene_job_message_count_check CHECK (
        (kind = 'message' AND message_count BETWEEN 1 AND 32) OR
        (kind = 'task_wake' AND message_count = 0)) NOT VALID;
