ALTER TABLE dsh_plugin DROP CONSTRAINT IF EXISTS dsh_plugin_source_kind_check;
ALTER TABLE dsh_plugin
    ADD CONSTRAINT dsh_plugin_source_kind_check
    CHECK (source_kind IN ('npm', 'github', 'url', 'file'));
ALTER TABLE dsh_plugin DROP COLUMN IF EXISTS artifact_size;
ALTER TABLE dsh_plugin DROP COLUMN IF EXISTS artifact_key;
