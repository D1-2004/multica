ALTER TABLE internal_connector
    ADD COLUMN auth_mode text NOT NULL DEFAULT 'bearer';

ALTER TABLE internal_connector
    ADD CONSTRAINT internal_connector_auth_mode_check
    CHECK (auth_mode IN ('none', 'bearer'));
