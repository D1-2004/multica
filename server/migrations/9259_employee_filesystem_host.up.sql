CREATE OR REPLACE VIEW employee_filesystem_host AS
SELECT workspace_id,agent_id,state,generation,sandbox_id,template_id,
       '00000000-0000-0000-0000-000000000000'::uuid AS scope_id
FROM dsh_employee_host
UNION ALL
SELECT workspace_id,agent_id,state,generation,sandbox_id,template_id,scope_id
FROM employee_filesystem_sandbox;
