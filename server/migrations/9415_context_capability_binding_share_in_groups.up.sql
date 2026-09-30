-- 「在群聊中由我触发时也可用」: a person's connector binding may opt in to
-- group runs that person triggers. Meaningful only for scope_type='person'
-- and resource_type='connector'. Stored only this round; runtime resolution
-- does not read it yet.
ALTER TABLE context_capability_binding ADD COLUMN IF NOT EXISTS share_in_groups boolean NOT NULL DEFAULT false;
