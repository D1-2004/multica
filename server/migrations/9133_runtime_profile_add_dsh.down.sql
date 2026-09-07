-- Restore the pre-DSH write whitelist. Existing rows with protocol_family
-- 'dsh' are intentionally left in place: NOT VALID does not re-check them, so
-- they remain readable, but any later UPDATE of such a row will fail the
-- narrowed constraint. Disable or delete DSH runtime profiles before rolling
-- back if they must stay editable.
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
