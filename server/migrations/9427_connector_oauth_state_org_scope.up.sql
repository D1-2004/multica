-- An OAuth connect of an enterprise-level (org) scope keeps the org in its
-- pending state (scope_type 'org', scope_key = org_id). Widening only.
ALTER TABLE connector_oauth_state
    DROP CONSTRAINT IF EXISTS connector_oauth_state_scope_type_check,
    ADD CONSTRAINT connector_oauth_state_scope_type_check CHECK (scope_type IN ('workspace', 'org', 'scene', 'person'));
