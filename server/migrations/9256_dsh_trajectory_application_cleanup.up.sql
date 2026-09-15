-- Parent validation and dependent deletion are owned by application transactions.
ALTER TABLE agent_task_dsh_trajectory
DROP CONSTRAINT IF EXISTS agent_task_dsh_trajectory_task_id_fkey;
