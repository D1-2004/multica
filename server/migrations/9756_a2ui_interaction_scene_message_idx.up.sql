CREATE INDEX CONCURRENTLY IF NOT EXISTS a2ui_interaction_scene_message_idx
    ON a2ui_interaction (agent_id, scene_id, message_id);
