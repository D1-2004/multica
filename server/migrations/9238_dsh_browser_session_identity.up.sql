-- Official browser Sessions use canonical UUIDs without the platform prefix.
ALTER TABLE dsh_employee_session DROP CONSTRAINT dsh_employee_session_session_id_check;
ALTER TABLE dsh_employee_session ADD CONSTRAINT dsh_employee_session_session_id_check
 CHECK (session_id ~ '^(session-)?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');
ALTER TABLE dsh_task_binding DROP CONSTRAINT dsh_task_binding_session_id_check;
ALTER TABLE dsh_task_binding ADD CONSTRAINT dsh_task_binding_session_id_check
 CHECK (session_id ~ '^(session-)?[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');
