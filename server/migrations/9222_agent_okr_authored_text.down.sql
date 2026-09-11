-- Preserve authored text after package imports have started using this column.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_okr WHERE authored_text IS NOT NULL) THEN
        RAISE EXCEPTION 'Cannot remove authored_text while Agent OKRs use it';
    END IF;
END $$;
ALTER TABLE agent_okr DROP COLUMN IF EXISTS authored_text;
