-- name: GetManagedAgentSourceSnapshot :one
SELECT * FROM managed_agent_source_snapshot
WHERE source_key = $1;

-- name: UpsertManagedAgentSourceSnapshotSuccess :one
INSERT INTO managed_agent_source_snapshot (
    source_key, repository_url, ref, resolved_commit_sha, bundle_hash, bundle,
    last_check_at, last_success_at, last_error
) VALUES (
    $1, $2, $3, $4, $5, $6, now(), now(), NULL
)
ON CONFLICT (source_key) DO UPDATE SET
    repository_url = EXCLUDED.repository_url,
    ref = EXCLUDED.ref,
    resolved_commit_sha = EXCLUDED.resolved_commit_sha,
    bundle_hash = EXCLUDED.bundle_hash,
    bundle = EXCLUDED.bundle,
    last_check_at = now(),
    last_success_at = now(),
    last_error = NULL,
    updated_at = now()
RETURNING *;

-- name: UpsertManagedAgentSourceSnapshotFailure :one
INSERT INTO managed_agent_source_snapshot (
    source_key, repository_url, ref, last_check_at, last_error
) VALUES (
    $1, $2, $3, now(), $4
)
ON CONFLICT (source_key) DO UPDATE SET
    repository_url = EXCLUDED.repository_url,
    ref = EXCLUDED.ref,
    last_check_at = now(),
    last_error = EXCLUDED.last_error,
    updated_at = now()
RETURNING *;
