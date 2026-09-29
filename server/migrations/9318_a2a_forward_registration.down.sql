DROP TABLE IF EXISTS a2a_forward_registration;
DROP TABLE IF EXISTS agent_a2a_forward_registrant;
DROP TABLE IF EXISTS agent_a2a_forward_client;

ALTER TABLE agent_a2a_operator_config
    DROP COLUMN IF EXISTS a2a_identity_enabled,
    DROP COLUMN IF EXISTS accept_prod_forward,
    DROP COLUMN IF EXISTS updated_by;
