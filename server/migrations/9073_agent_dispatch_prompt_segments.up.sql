-- Generalize the single dispatch_prompt override into per-segment overrides.
--
-- The claim-time instruction is composed from four independent segments
-- (managed policy, Router delivery context, DingTalk reply formatting, BUC
-- authorization), not one blob. A single TEXT column could only ever replace
-- the first, which made the other two invisible and uncustomizable. Keying by
-- segment lets the settings UI show the real structure and override any part
-- of it.
--
-- dispatch_prompt shipped earlier the same day and nothing in production had
-- authored one, but migrate rather than discard: replaying this on a database
-- that already holds a value must not lose it.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS dispatch_prompt_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Guarded because a replay runs after the source column is gone. The DROP below
-- is deliberately NOT inside this block: sqlc does not evaluate PL/pgSQL, so a
-- DO-block DROP leaves the column in sqlc's schema view and the generated code
-- would keep selecting a column the real database no longer has.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'agent' AND column_name = 'dispatch_prompt'
    ) THEN
        UPDATE agent
        SET dispatch_prompt_overrides = jsonb_build_object('policy', dispatch_prompt)
        WHERE COALESCE(BTRIM(dispatch_prompt), '') <> ''
          AND NOT dispatch_prompt_overrides ? 'policy';
    END IF;
END $$;

ALTER TABLE agent DROP COLUMN IF EXISTS dispatch_prompt;
