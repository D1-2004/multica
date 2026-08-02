CREATE TABLE IF NOT EXISTS dingtalk_binding_unbind_outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id UUID NOT NULL,
    workspace_id    UUID NOT NULL,
    agent_id        UUID NOT NULL,
    platform        TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    account_id      TEXT NOT NULL,
    target_identity TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'delivered')),
    available_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    lease_token     UUID,
    lease_expires_at TIMESTAMPTZ,
    last_error_code TEXT,
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_dingtalk_binding_unbind_account
        UNIQUE (installation_id, platform, tenant_id, account_id),
    CONSTRAINT ck_dingtalk_binding_unbind_account_key CHECK (
        platform = 'dingtalk'
        AND tenant_id = btrim(tenant_id) AND tenant_id <> ''
        AND account_id = btrim(account_id) AND account_id <> ''
    ),
    CONSTRAINT ck_dingtalk_binding_unbind_target CHECK (
        target_identity ~ '^router-target:v1:sha256:[a-f0-9]{64}$'
    )
);

CREATE INDEX IF NOT EXISTS idx_dingtalk_binding_unbind_outbox_queue
    ON dingtalk_binding_unbind_outbox (target_identity, available_at, created_at)
    WHERE status = 'queued';

CREATE UNIQUE INDEX IF NOT EXISTS uq_dingtalk_binding_unbind_installation_queued
    ON dingtalk_binding_unbind_outbox (installation_id)
    WHERE status = 'queued';
