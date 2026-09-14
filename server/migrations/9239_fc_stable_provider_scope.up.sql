ALTER TABLE fc_e2b_stable_release
    ADD COLUMN IF NOT EXISTS provider_scope TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_release
    DROP CONSTRAINT IF EXISTS fc_e2b_stable_release_provider_scope_check,
    ADD CONSTRAINT fc_e2b_stable_release_provider_scope_check CHECK (
        provider_scope = '' OR (sandbox_backend = 'aliyun_fc' AND
        provider_scope IN ('hermes','opencode','pi','dsh','opencode-v2','claude','codex'))
    );

ALTER TABLE fc_e2b_stable_channel
    DROP CONSTRAINT IF EXISTS fc_e2b_stable_channel_channel_check,
    ADD CONSTRAINT fc_e2b_stable_channel_channel_check CHECK (
        channel = 'stable' OR (sandbox_backend = 'aliyun_fc' AND
        channel IN ('stable:hermes','stable:opencode','stable:pi','stable:dsh',
                    'stable:opencode-v2','stable:claude','stable:codex'))
    );

-- Reject a legacy worker attempting to widen a scoped release's target set.
-- No relationship is enforced through a foreign key or cascading action.
CREATE OR REPLACE FUNCTION check_fc_stable_target_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE scope TEXT; backend TEXT;
BEGIN
    SELECT provider_scope,sandbox_backend INTO scope,backend
      FROM fc_e2b_stable_release WHERE id=NEW.release_id;
    IF NOT FOUND OR backend<>NEW.sandbox_backend OR
       (scope<>'' AND scope<>NEW.provider) OR
       (scope='' AND EXISTS (SELECT 1 FROM fc_e2b_stable_channel
         WHERE sandbox_backend=backend AND channel='stable:'||NEW.provider)) THEN
        RAISE EXCEPTION 'stable release target is outside its provider scope'
          USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS fc_stable_target_scope ON fc_e2b_stable_release_target;
CREATE TRIGGER fc_stable_target_scope BEFORE INSERT OR UPDATE OF release_id,provider,sandbox_backend
ON fc_e2b_stable_release_target FOR EACH ROW EXECUTE FUNCTION check_fc_stable_target_scope();

CREATE OR REPLACE FUNCTION check_fc_stable_channel_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE scope TEXT; backend TEXT;
BEGIN
    IF NEW.active_release_id IS NOT NULL THEN
        SELECT provider_scope,sandbox_backend INTO scope,backend
          FROM fc_e2b_stable_release WHERE id=NEW.active_release_id;
        IF NOT FOUND OR backend<>NEW.sandbox_backend OR
           NEW.channel<>(CASE WHEN scope='' THEN 'stable' ELSE 'stable:'||scope END) THEN
            RAISE EXCEPTION 'stable channel does not match the active release scope'
              USING ERRCODE='23514';
        END IF;
    END IF;
    IF TG_OP='UPDATE' AND NEW.current_release_id IS DISTINCT FROM OLD.current_release_id THEN
        SELECT provider_scope,sandbox_backend INTO scope,backend
          FROM fc_e2b_stable_release WHERE id=NEW.current_release_id;
        IF NOT FOUND OR backend<>NEW.sandbox_backend OR
           NEW.channel<>(CASE WHEN scope='' THEN 'stable' ELSE 'stable:'||scope END) THEN
            RAISE EXCEPTION 'stable publication does not match the release scope'
              USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS fc_stable_channel_scope ON fc_e2b_stable_channel;
CREATE TRIGGER fc_stable_channel_scope BEFORE INSERT OR UPDATE
ON fc_e2b_stable_channel FOR EACH ROW EXECUTE FUNCTION check_fc_stable_channel_scope();

-- An older server must not create a new stable Runtime on the shared pointer
-- after a provider-specific pointer has been published.
CREATE OR REPLACE FUNCTION check_fc_stable_runtime_creation_scope() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE expected TEXT;
BEGIN
    IF NEW.runtime_mode='cloud' AND
       (NEW.metadata->>'kind'='fc-e2b' OR NEW.metadata->>'sandbox_backend'='aliyun_fc') AND
       COALESCE(NULLIF(NEW.metadata->>'artifact_channel',''),
                NULLIF(NEW.metadata->>'template_channel',''),'stable')='stable' THEN
        PERFORM pg_advisory_xact_lock_shared(1179861810,1);
        SELECT current_artifact_ref INTO expected FROM fc_e2b_stable_channel
          WHERE sandbox_backend='aliyun_fc' AND channel='stable:'||NEW.provider;
        IF FOUND AND COALESCE(NULLIF(NEW.metadata->>'artifact_ref',''),
                             NEW.metadata->>'template_id','')<>expected THEN
            RAISE EXCEPTION 'stable Runtime requires its provider channel artifact'
              USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS fc_stable_runtime_creation_scope ON agent_runtime;
CREATE TRIGGER fc_stable_runtime_creation_scope BEFORE INSERT
ON agent_runtime FOR EACH ROW EXECUTE FUNCTION check_fc_stable_runtime_creation_scope();
