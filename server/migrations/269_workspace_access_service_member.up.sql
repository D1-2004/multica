-- Workspace access token subjects now use Multica's native member role and
-- authorization model. A token subject must never carry an administrative
-- role, so the backfill also normalizes any pre-existing membership to member.
INSERT INTO member (workspace_id, user_id, role)
SELECT workspace_id, subject_user_id, 'member'
FROM workspace_access_token
ON CONFLICT (workspace_id, user_id) DO UPDATE
SET role = 'member';

-- Capability policy is intentionally removed instead of retained as an inert
-- compatibility field: no released DTA integration consumes the old contract.
ALTER TABLE workspace_access_token
    DROP COLUMN IF EXISTS capabilities,
    DROP COLUMN IF EXISTS resource_scope;
