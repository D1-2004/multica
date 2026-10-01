DELETE FROM context_connector_credential WHERE scope_type = 'org';
ALTER TABLE context_connector_credential
    DROP CONSTRAINT IF EXISTS context_connector_credential_scope_type_check,
    ADD CONSTRAINT context_connector_credential_scope_type_check CHECK (scope_type IN ('scene', 'person'));
