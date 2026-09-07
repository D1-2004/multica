-- Task-finished coordinator loop (outbound). Default off.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS task_finished_loop_enabled BOOLEAN NOT NULL DEFAULT false;
