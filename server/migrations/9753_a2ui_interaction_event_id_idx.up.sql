CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS a2ui_interaction_event_id_idx ON a2ui_interaction (event_id) WHERE event_id <> '';
