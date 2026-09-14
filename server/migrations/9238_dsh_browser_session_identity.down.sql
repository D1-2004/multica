-- Refuse a lossy downgrade after browser Sessions have been adopted.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM dsh_employee_session WHERE session_id !~ '^session-[0-9a-f-]{36}$')
 OR EXISTS (SELECT 1 FROM dsh_task_binding WHERE session_id !~ '^session-[0-9a-f-]{36}$') THEN
  RAISE EXCEPTION 'DSH browser Session bindings require a compatible application version';
 END IF;
END $$;
ALTER TABLE dsh_employee_session DROP CONSTRAINT dsh_employee_session_session_id_check;
ALTER TABLE dsh_employee_session ADD CONSTRAINT dsh_employee_session_session_id_check
 CHECK (session_id ~ '^session-[0-9a-f-]{36}$');
ALTER TABLE dsh_task_binding DROP CONSTRAINT dsh_task_binding_session_id_check;
ALTER TABLE dsh_task_binding ADD CONSTRAINT dsh_task_binding_session_id_check
 CHECK (session_id ~ '^session-[0-9a-f-]{36}$');
