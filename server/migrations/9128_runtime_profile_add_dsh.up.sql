-- Add the DeepSeek Harness runtime bridge as a first-party protocol family so
-- a custom runtime profile may declare protocol_family = 'dsh'.
--
-- Fork-owned (9000+) rather than upstream's 313: the fork's own whitelist stops
-- at 254, and the runner tracks applied migrations by full filename stem, so a
-- renamed or replayed file must stay idempotent. DROP ... IF EXISTS followed by
-- ADD inside one DO block keeps the replacement atomic under the autocommit
-- runner, so no window exists in which the table carries no constraint.
--
-- NOT VALID preserves historical-row tolerance (matching 254) while enforcing
-- the expanded whitelist for every new or updated row. No foreign key, no
-- cascade, and no index is created here.
DO $migration$
BEGIN
    ALTER TABLE runtime_profile
        DROP CONSTRAINT IF EXISTS runtime_profile_protocol_family_check;

    ALTER TABLE runtime_profile
        ADD CONSTRAINT runtime_profile_protocol_family_check
        CHECK (protocol_family IN (
            'claude',
            'codebuddy',
            'codex',
            'copilot',
            'opencode',
            'openclaw',
            'hermes',
            'pi',
            'cursor',
            'kimi',
            'reasonix',
            'dsh',
            'kiro',
            'antigravity',
            'qoder',
            'qoderclicn',
            'traecli',
            'deveco',
            'grok',
            'qwen',
            'qwenpaw'
        )) NOT VALID;
END;
$migration$;
