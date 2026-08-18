ALTER TABLE agent_task_queue
  DROP COLUMN IF EXISTS runtime_launch_lease_token,
  DROP COLUMN IF EXISTS runtime_launch_lease_expires_at;
