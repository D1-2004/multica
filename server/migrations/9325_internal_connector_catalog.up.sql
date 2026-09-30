-- Official app (catalog) connectors: the catalog slug, the tools discovered
-- with a connected account ([{"name","read_only"}]) and whether write tools
-- are pinned. auth_mode gains 'oauth'. Idempotent: the runner replays renamed
-- files, and older binaries keep reading the table during a rolling deploy.
ALTER TABLE internal_connector ADD COLUMN IF NOT EXISTS catalog_slug text NOT NULL DEFAULT '';
ALTER TABLE internal_connector ADD COLUMN IF NOT EXISTS discovered_tools jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE internal_connector ADD COLUMN IF NOT EXISTS write_enabled boolean NOT NULL DEFAULT false;

ALTER TABLE internal_connector DROP CONSTRAINT IF EXISTS internal_connector_discovered_tools_array;
ALTER TABLE internal_connector
    ADD CONSTRAINT internal_connector_discovered_tools_array CHECK (jsonb_typeof(discovered_tools) = 'array');

ALTER TABLE internal_connector DROP CONSTRAINT IF EXISTS internal_connector_auth_mode_check;
ALTER TABLE internal_connector
    ADD CONSTRAINT internal_connector_auth_mode_check CHECK (auth_mode IN ('none', 'bearer', 'oauth'));
