ALTER TABLE agent_enterprise_identity
    ADD COLUMN anchor_maintained_at TIMESTAMPTZ NOT NULL DEFAULT now();

DROP INDEX agent_enterprise_identity_anchor_maintenance_idx;

CREATE INDEX agent_enterprise_identity_anchor_maintenance_idx
    ON agent_enterprise_identity (anchor_maintained_at, token_version)
    WHERE status = 'active';
