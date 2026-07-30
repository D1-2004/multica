DELETE FROM agent_enterprise_identity_attempt
WHERE requested_raw_emp_id IS NULL;

ALTER TABLE agent_enterprise_identity_attempt
    ALTER COLUMN requested_raw_emp_id SET NOT NULL;
