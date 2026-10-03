CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_invitation_reminder_ordinal_idx ON employee_task_invitation_reminder (invitation_id, ordinal);
