-- Enterprise-level (org) connector credentials: scope_type 'org' with
-- scope_key = org_id. Widening only.
ALTER TABLE context_connector_credential
    DROP CONSTRAINT IF EXISTS context_connector_credential_scope_type_check,
    ADD CONSTRAINT context_connector_credential_scope_type_check CHECK (scope_type IN ('org', 'scene', 'person'));
