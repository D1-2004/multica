-- Coordinator jobs of one Agent work scene: the server-resolved SceneRef the
-- job's command carries (command.agent_scene.scene_id, docs/agent-scene.md).
CREATE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_job_agent_scene_idx
    ON inbound_coordinator_job (agent_id, (command #>> '{agent_scene,scene_id}'));
