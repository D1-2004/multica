ALTER TABLE internal_connector
    DROP CONSTRAINT IF EXISTS internal_connector_auth_mode_check;

ALTER TABLE internal_connector
    DROP COLUMN IF EXISTS auth_mode;
