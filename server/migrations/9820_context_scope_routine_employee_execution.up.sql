-- Explicit execution choice of a scene routine whose agent runs in employee
-- mode: run_only compiles and dispatches every occurrence deterministically;
-- employee_decide gives each occurrence one EmployeeLoop decision wake.
-- Separate from autopilot.execution_mode, which old binaries still parse as
-- run_only. Old binaries list explicit columns and never read this one.
ALTER TABLE context_scope_routine ADD COLUMN IF NOT EXISTS employee_execution text NOT NULL DEFAULT 'run_only'
 CONSTRAINT context_scope_routine_employee_execution_check CHECK (employee_execution IN ('run_only', 'employee_decide'));
