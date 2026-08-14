-- DingTalk account binding reuses channel_installation. The pending state is
-- the short-lived interval between issuing the QR credentials and verifying
-- the Router subscription callback.
ALTER TABLE channel_installation
    DROP CONSTRAINT IF EXISTS channel_installation_status_check;

ALTER TABLE channel_installation
    ADD CONSTRAINT channel_installation_status_check
    CHECK (status IN ('pending', 'active', 'revoked'));

-- endpointId is the stable public routing identity. It must never resolve to
-- two installations, including revoked placeholders that may later be rebound.
CREATE UNIQUE INDEX IF NOT EXISTS idx_channel_installation_dingtalk_account_endpoint
    ON channel_installation ((config ->> 'dispatch_endpoint_id'))
    WHERE channel_type = 'dingtalk_account'
      AND NULLIF(config ->> 'dispatch_endpoint_id', '') IS NOT NULL;

-- The previous fork-only endpoint table stored per-row dispatch credentials.
-- The final contract derives the credential from the versioned endpointId and
-- stores the endpointId in channel_installation.config instead.
DROP TABLE IF EXISTS agent_dispatch_endpoint;
