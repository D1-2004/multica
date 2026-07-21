-- Keep the workspace created by the FDE quick-init flow separate from every
-- ordinary workspace the same user may own or administer. The row survives a
-- workspace deletion so a later scan can repair the onboarding by creating a
-- fresh dedicated workspace.

CREATE TABLE IF NOT EXISTS fde_onboarding (
    user_id UUID PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
    workspace_id UUID UNIQUE REFERENCES workspace(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Preserve completed quick-init flows created before the dedicated ledger was
-- introduced. The FDE runtime identity distinguishes these workspaces from
-- unrelated managed Agents.
INSERT INTO fde_onboarding (user_id, workspace_id)
SELECT DISTINCT ON (agent.owner_id)
    agent.owner_id,
    agent.workspace_id
FROM agent_source AS source
JOIN agent ON agent.id = source.agent_id
JOIN agent_runtime AS runtime ON runtime.id = agent.runtime_id
WHERE source.source_type = 'managed_git'
  AND source.managed_source_key IS NOT NULL
  AND agent.owner_id IS NOT NULL
  AND runtime.daemon_id LIKE 'fc-e2b:fde:%'
ORDER BY agent.owner_id, source.updated_at DESC
ON CONFLICT DO NOTHING;
