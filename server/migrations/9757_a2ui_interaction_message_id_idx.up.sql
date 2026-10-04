CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS a2ui_interaction_message_id_idx
    ON a2ui_interaction (agent_id, message_id)
    WHERE message_id <> '';
