DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM workspace_access_token) THEN
        RAISE EXCEPTION 'cannot roll back workspace access token schema while tokens exist; disable the feature and retain the additive schema';
    END IF;
END $$;

DROP TABLE IF EXISTS workspace_access_audit;
DROP TABLE IF EXISTS workspace_access_token;

ALTER TABLE "user" DROP COLUMN IF EXISTS principal_type;
