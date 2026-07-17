-- name: GetFDEOnboardingWorkspaceForUser :one
SELECT w.*
FROM fde_onboarding AS onboarding
JOIN workspace AS w ON w.id = onboarding.workspace_id
JOIN member AS membership
  ON membership.workspace_id = w.id
 AND membership.user_id = onboarding.user_id
WHERE onboarding.user_id = $1
  AND membership.role IN ('owner', 'admin');

-- name: LockFDEOnboardingWorkspaceForUser :one
SELECT workspace_id
FROM fde_onboarding
WHERE user_id = $1
FOR UPDATE;

-- name: UpsertFDEOnboardingWorkspace :one
INSERT INTO fde_onboarding (user_id, workspace_id)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id,
    updated_at = now()
RETURNING *;
