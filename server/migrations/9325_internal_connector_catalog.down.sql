-- NOT VALID keeps the rollback from failing on existing OAuth connectors;
-- new writes are checked again.
ALTER TABLE internal_connector DROP CONSTRAINT IF EXISTS internal_connector_auth_mode_check;
ALTER TABLE internal_connector
    ADD CONSTRAINT internal_connector_auth_mode_check CHECK (auth_mode IN ('none', 'bearer')) NOT VALID;
ALTER TABLE internal_connector DROP CONSTRAINT IF EXISTS internal_connector_discovered_tools_array;
ALTER TABLE internal_connector DROP COLUMN IF EXISTS write_enabled;
ALTER TABLE internal_connector DROP COLUMN IF EXISTS discovered_tools;
ALTER TABLE internal_connector DROP COLUMN IF EXISTS catalog_slug;
