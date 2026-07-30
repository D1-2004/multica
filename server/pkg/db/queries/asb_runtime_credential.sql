-- name: GetASBRuntimeCredential :one
SELECT *
FROM asb_runtime_credential
WHERE runtime_id = sqlc.arg('runtime_id');

-- name: UpsertASBRuntimeCredential :one
INSERT INTO asb_runtime_credential (
    runtime_id,
    api_key_encrypted,
    api_key_hint
) VALUES (
    sqlc.arg('runtime_id'),
    sqlc.arg('api_key_encrypted'),
    sqlc.arg('api_key_hint')
)
ON CONFLICT (runtime_id)
DO UPDATE SET
    api_key_encrypted = EXCLUDED.api_key_encrypted,
    api_key_hint = EXCLUDED.api_key_hint,
    updated_at = now()
RETURNING *;
