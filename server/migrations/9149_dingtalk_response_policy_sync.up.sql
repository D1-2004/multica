CREATE TABLE IF NOT EXISTS dingtalk_response_policy_sync (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    target_identity TEXT NOT NULL,
    source_id TEXT NOT NULL,
    installation_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    agent_id UUID NOT NULL,
    agent_revision BIGINT NOT NULL CHECK (agent_revision > 0),
    rollout_revision BIGINT NOT NULL CHECK (rollout_revision > 0),
    source_updated_at TIMESTAMPTZ NOT NULL,
    policy_revision BIGINT NOT NULL CHECK (policy_revision > 0),
    desired_mode TEXT NOT NULL CHECK (desired_mode IN ('legacy', 'multica_coordinator')),
    show_ai_tag BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'syncing', 'synced')),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    observed_policy JSONB,
    synced_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status = 'syncing' AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'syncing' AND lease_token IS NULL AND lease_expires_at IS NULL))
);

-- This fence prevents differently configured replicas from oscillating policy.
CREATE TABLE IF NOT EXISTS dingtalk_response_policy_rollout (
    target_identity TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    enabled BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
