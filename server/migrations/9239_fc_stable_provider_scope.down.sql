-- Provider channel history cannot be silently discarded during a downgrade.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM fc_e2b_stable_channel WHERE channel<>'stable') OR
       EXISTS (SELECT 1 FROM fc_e2b_stable_release WHERE provider_scope<>'') THEN
        RAISE EXCEPTION 'provider stable channels must be explicitly retired before schema downgrade';
    END IF;
END $$;
DROP TRIGGER IF EXISTS fc_stable_runtime_creation_scope ON agent_runtime;
DROP FUNCTION IF EXISTS check_fc_stable_runtime_creation_scope();
DROP TRIGGER IF EXISTS fc_stable_channel_scope ON fc_e2b_stable_channel;
DROP FUNCTION IF EXISTS check_fc_stable_channel_scope();
DROP TRIGGER IF EXISTS fc_stable_target_scope ON fc_e2b_stable_release_target;
DROP FUNCTION IF EXISTS check_fc_stable_target_scope();
ALTER TABLE fc_e2b_stable_channel DROP CONSTRAINT IF EXISTS fc_e2b_stable_channel_channel_check,
    ADD CONSTRAINT fc_e2b_stable_channel_channel_check CHECK (channel='stable');
ALTER TABLE fc_e2b_stable_release DROP COLUMN IF EXISTS provider_scope;
