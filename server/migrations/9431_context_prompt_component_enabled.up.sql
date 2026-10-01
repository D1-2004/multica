-- 「启用」 switch of a Context Builder prompt component. A disabled
-- component stays stored but takes no part in the merge: it is neither
-- applied nor overrides an outer component of the same name. Existing rows
-- stay enabled. Additive and idempotent; old binaries ignore the column.
ALTER TABLE context_prompt_component ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;
