DROP INDEX agent_enterprise_identity_anchor_maintenance_idx;

ALTER TABLE agent_enterprise_identity
    DROP COLUMN anchor_maintained_at;

CREATE INDEX agent_enterprise_identity_anchor_maintenance_idx
    ON agent_enterprise_identity (updated_at, token_version)
    WHERE status = 'active';
