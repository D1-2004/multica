ALTER TABLE employee_host_notice DROP CONSTRAINT IF EXISTS employee_host_notice_source_kind_check;
ALTER TABLE employee_host_notice ADD CONSTRAINT employee_host_notice_source_kind_check CHECK(source_kind IN ('task_wake','invitation','watchdog','task_plan','human_response')) NOT VALID;
