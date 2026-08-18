ALTER TABLE agent_task_queue
  ADD COLUMN IF NOT EXISTS runtime_launch_lease_token UUID,
  ADD COLUMN IF NOT EXISTS runtime_launch_lease_expires_at TIMESTAMPTZ;
