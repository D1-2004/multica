-- Multica keeps the exact bytes it validated at import.
--
-- Two reasons. Fetching from npm at every task start makes plugin loading
-- depend on a registry being fast and available, and means a sandbox can run
-- bytes nobody validated if the registry serves something new. And a package
-- uploaded as a zip has no upstream URL at all, so hosting is the only way to
-- deliver it.

ALTER TABLE dsh_plugin
    ADD COLUMN IF NOT EXISTS artifact_key TEXT NOT NULL DEFAULT '';

ALTER TABLE dsh_plugin
    ADD COLUMN IF NOT EXISTS artifact_size BIGINT NOT NULL DEFAULT 0;

-- 'upload' is a package that arrived as a file rather than a reference, so it
-- has no upstream to re-resolve against.
ALTER TABLE dsh_plugin
    DROP CONSTRAINT IF EXISTS dsh_plugin_source_kind_check;

ALTER TABLE dsh_plugin
    ADD CONSTRAINT dsh_plugin_source_kind_check
    CHECK (source_kind IN ('npm', 'github', 'url', 'file', 'upload'));
