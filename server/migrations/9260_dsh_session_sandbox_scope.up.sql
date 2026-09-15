ALTER TABLE dsh_employee_session ADD COLUMN IF NOT EXISTS sandbox_scope_id uuid DEFAULT '00000000-0000-0000-0000-000000000000'::uuid;
ALTER TABLE dsh_employee_session ALTER COLUMN sandbox_scope_id DROP DEFAULT;
