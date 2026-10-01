DELETE FROM connector_oauth_state WHERE scope_type = 'org';
ALTER TABLE connector_oauth_state
    DROP CONSTRAINT IF EXISTS connector_oauth_state_scope_type_check,
    ADD CONSTRAINT connector_oauth_state_scope_type_check CHECK (scope_type IN ('workspace', 'scene', 'person'));
