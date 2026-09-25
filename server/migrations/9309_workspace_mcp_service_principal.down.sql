ALTER TABLE "user" DROP CONSTRAINT IF EXISTS user_principal_type_check;
ALTER TABLE "user" ADD CONSTRAINT user_principal_type_check
    CHECK (principal_type IN ('human', 'workspace_access_token'));
