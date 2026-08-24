-- Agent-level override for the Diamond-composed dispatch instruction.
-- Empty string means "no override": the claim path keeps composing
-- common.prompt + <surface>.prompt from Diamond. A non-empty value replaces
-- both Diamond sections entirely; the Router-supplied contextPrompt is
-- appended either way because it carries per-dispatch delivery facts that
-- no authored prompt can substitute for.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS dispatch_prompt TEXT NOT NULL DEFAULT '';
