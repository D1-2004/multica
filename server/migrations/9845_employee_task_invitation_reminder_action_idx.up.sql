CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_invitation_reminder_action_idx ON employee_task_invitation_reminder (action_id) WHERE action_id IS NOT NULL;
