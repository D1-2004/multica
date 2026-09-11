-- Authored routing constraints are separate from executor job instructions.
ALTER TABLE agent ADD COLUMN IF NOT EXISTS coordinator_contract JSONB;
