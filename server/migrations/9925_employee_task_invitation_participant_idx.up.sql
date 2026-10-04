CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_invitation_participant_idx ON employee_task_invitation (workspace_id, agent_id, participant_ref);
