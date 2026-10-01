ALTER TABLE internal_connector_call_audit ADD COLUMN IF NOT EXISTS binding_layer text NOT NULL DEFAULT '';
ALTER TABLE internal_connector_call_audit ADD COLUMN IF NOT EXISTS credential_layer text NOT NULL DEFAULT '';
