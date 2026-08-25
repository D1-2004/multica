-- Agent OKRs. Each objective and each key result materializes as a workspace
-- label so the agent can tag issues with it through the existing labeling
-- machinery; this table holds the ownership, the O -> KR grouping, and the
-- display order that a flat label catalog cannot express.
--
-- Labels are created in the 'issue' namespace on purpose. The 'agent'
-- namespace exists but cannot be attached to an issue (the attach queries and
-- handlers both reject a namespace mismatch), and tagging issues is the entire
-- point of the feature.
--
-- No foreign keys per repository policy: label and agent cleanup is resolved in
-- application code inside a transaction with the parent operation.
CREATE TABLE IF NOT EXISTS agent_okr (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('objective', 'key_result')),
    -- Set for a key result, pointing at its objective row. NULL for objectives.
    parent_id UUID,
    label_id UUID NOT NULL,
    position INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
