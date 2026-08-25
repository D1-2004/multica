ALTER TABLE agent
ADD COLUMN IF NOT EXISTS dispatch_prompt TEXT NOT NULL DEFAULT '';

UPDATE agent
SET dispatch_prompt = COALESCE(dispatch_prompt_overrides ->> 'policy', '')
WHERE dispatch_prompt_overrides ? 'policy';

ALTER TABLE agent
DROP COLUMN IF EXISTS dispatch_prompt_overrides;
