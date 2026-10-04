CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_member_roster_scene_idx ON employee_scene_member_roster (workspace_id, agent_id, scene_id);
