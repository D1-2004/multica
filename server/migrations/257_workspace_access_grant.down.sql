DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM workspace_access_grant) THEN
        RAISE EXCEPTION 'cannot roll back workspace access grant schema while grants exist; disable the feature and retain the additive schema';
    END IF;
END $$;

DROP TABLE IF EXISTS workspace_access_audit;
DROP TABLE IF EXISTS workspace_access_token;
DROP TABLE IF EXISTS workspace_access_grant;

ALTER TABLE "user" DROP COLUMN IF EXISTS principal_type;
