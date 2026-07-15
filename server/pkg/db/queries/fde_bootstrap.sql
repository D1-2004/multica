-- name: CreateFDEBootstrapIntent :one
INSERT INTO fde_bootstrap_intent (
    token_hash, expected_identity_hmac, source_workspace_id, source_agent_id,
    source_task_id, source_chat_session_id, source_installation_id, expires_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ExpirePendingFDEBootstrapIntentsBySourceTask :exec
UPDATE fde_bootstrap_intent
SET status = 'expired', updated_at = now()
WHERE source_task_id = $1
  AND status IN ('pending', 'failed_retryable');

-- name: ExpireFDEBootstrapIntentIfNeeded :exec
UPDATE fde_bootstrap_intent
SET status = 'expired', updated_at = now()
WHERE token_hash = $1
  AND expires_at <= now()
  AND status IN ('pending', 'failed_retryable');

-- name: GetFDEBootstrapIntentByTokenHash :one
SELECT * FROM fde_bootstrap_intent WHERE token_hash = $1;

-- name: LockFDEBootstrapIntentByTokenHash :one
SELECT * FROM fde_bootstrap_intent WHERE token_hash = $1 FOR UPDATE;

-- name: MarkFDEBootstrapIntentProvisioning :one
UPDATE fde_bootstrap_intent
SET status = 'provisioning', user_id = $2, last_error_code = NULL, updated_at = now()
WHERE id = $1
  AND status IN ('pending', 'failed_retryable', 'provisioning')
  AND expires_at > now()
RETURNING *;

-- name: MarkFDEBootstrapIntentReady :one
UPDATE fde_bootstrap_intent
SET status = 'ready', user_id = $2, workspace_id = $3,
    consumed_at = COALESCE(consumed_at, now()), last_error_code = NULL, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkFDEBootstrapIntentFailedRetryable :one
UPDATE fde_bootstrap_intent
SET status = 'failed_retryable', user_id = $2, last_error_code = $3, updated_at = now()
WHERE id = $1 AND status <> 'ready' AND expires_at > now()
RETURNING *;

-- name: InsertProductWorkspaceProvisioning :exec
INSERT INTO product_workspace_provisioning (user_id, product_key, status)
VALUES ($1, $2, 'provisioning')
ON CONFLICT (user_id, product_key) DO NOTHING;

-- name: LockProductWorkspaceProvisioning :one
SELECT * FROM product_workspace_provisioning
WHERE user_id = $1 AND product_key = $2
FOR UPDATE;

-- name: MarkProductWorkspaceProvisioningReady :one
UPDATE product_workspace_provisioning
SET workspace_id = $3, status = 'ready', last_error_code = NULL, updated_at = now()
WHERE user_id = $1 AND product_key = $2
RETURNING *;

-- name: MarkProductWorkspaceProvisioningFailedRetryable :one
UPDATE product_workspace_provisioning
SET workspace_id = NULL, status = 'failed_retryable', last_error_code = $3, updated_at = now()
WHERE user_id = $1 AND product_key = $2
RETURNING *;

-- name: GetProductWorkspaceProvisioning :one
SELECT * FROM product_workspace_provisioning
WHERE user_id = $1 AND product_key = $2;
