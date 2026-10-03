-- Validate 9910's CHECKs separately: SHARE UPDATE EXCLUSIVE lets admission,
-- claim and lease writes continue during the scan.
ALTER TABLE employee_scene_job VALIDATE CONSTRAINT employee_scene_job_kind_check;
ALTER TABLE employee_scene_job VALIDATE CONSTRAINT employee_scene_job_message_count_check;
