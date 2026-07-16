-- Durable DingTalk Stream ingress. The connector commits Admit before ACK;
-- workers claim and finalize rows with lease-token fencing.

-- name: AdmitDingTalkStreamInbox :one
INSERT INTO dingtalk_stream_inbox AS inbox (
    installation_id,
    client_id,
    connection_id,
    node_id,
    dedupe_key,
    stream_message_id,
    bot_message_id,
    topic,
    spec_version,
    frame_time,
    payload_encrypted
)
SELECT
    installation.id,
    sqlc.arg('client_id')::text,
    sqlc.arg('connection_id')::text,
    sqlc.arg('node_id')::text,
    sqlc.arg('dedupe_key')::text,
    sqlc.arg('stream_message_id')::text,
    sqlc.narg('bot_message_id')::text,
    sqlc.arg('topic')::text,
    sqlc.narg('spec_version')::text,
    sqlc.narg('frame_time')::bigint,
    sqlc.arg('payload_encrypted')::bytea
FROM channel_installation AS installation
WHERE installation.channel_type = 'dingtalk'
  AND installation.id = sqlc.arg('installation_id')::uuid
  AND installation.status = 'active'
  AND installation.config ->> 'app_id' = sqlc.arg('client_id')::text
ON CONFLICT (installation_id, dedupe_key) DO UPDATE SET
    stream_message_id = EXCLUDED.stream_message_id,
    connection_id = EXCLUDED.connection_id,
    node_id = EXCLUDED.node_id,
    bot_message_id = COALESCE(EXCLUDED.bot_message_id, inbox.bot_message_id),
    payload_encrypted = CASE
        WHEN inbox.status IN ('queued', 'processing') THEN EXCLUDED.payload_encrypted
        ELSE inbox.payload_encrypted
    END,
    delivery_count = inbox.delivery_count + 1,
    last_received_at = now(),
    updated_at = now()
RETURNING inbox.*;

-- name: ListDueDingTalkStreamInboxInstallations :many
-- Return only installations whose oldest non-terminal row is due. A delayed
-- retry blocks younger rows in the same lane, preserving the connector's
-- existing per-installation ordering.
WITH lane_heads AS (
    SELECT DISTINCT ON (installation_id)
        installation_id,
        status,
        available_at,
        lease_expires_at,
        received_at
    FROM dingtalk_stream_inbox
    WHERE status IN ('queued', 'processing')
    ORDER BY installation_id, received_at, id
)
SELECT installation_id
FROM lane_heads
WHERE (status = 'queued' AND available_at <= now())
   OR (status = 'processing' AND lease_expires_at <= now())
ORDER BY received_at
LIMIT sqlc.arg('candidate_limit');

-- name: ClaimNextDingTalkStreamInboxForInstallation :one
-- The caller holds the installation's session-scoped advisory lock. Claim only
-- the lane head; never leapfrog a live claim or a row waiting for backoff.
WITH lane_head AS (
    SELECT candidate.id
    FROM dingtalk_stream_inbox AS candidate
    WHERE candidate.installation_id = sqlc.arg('installation_id')
      AND candidate.status IN ('queued', 'processing')
    ORDER BY candidate.received_at, candidate.id
    FOR UPDATE
    LIMIT 1
), due AS (
    SELECT inbox.id
    FROM dingtalk_stream_inbox AS inbox
    JOIN lane_head ON lane_head.id = inbox.id
    WHERE (inbox.status = 'queued' AND inbox.available_at <= now())
       OR (inbox.status = 'processing' AND inbox.lease_expires_at <= now())
)
UPDATE dingtalk_stream_inbox AS inbox
SET status = 'processing',
    lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    updated_at = now()
FROM due
WHERE inbox.id = due.id
RETURNING inbox.*;

-- name: RetryClaimedDingTalkStreamInbox :one
UPDATE dingtalk_stream_inbox
SET status = 'queued',
    attempt_count = attempt_count + 1,
    available_at = sqlc.arg('available_at'),
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_class = sqlc.arg('last_error_class'),
    last_error = sqlc.arg('last_error'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'processing'
  AND lease_token = sqlc.arg('lease_token')
RETURNING *;

-- name: CompleteClaimedDingTalkStreamInbox :one
UPDATE dingtalk_stream_inbox
SET status = sqlc.arg('status'),
    attempt_count = attempt_count + 1,
    -- A dead-letter is recoverable for the retention window: keep the
    -- ciphertext so operators can requeue it after repairing configuration.
    -- Successfully processed/discarded payloads are erased immediately.
    payload_encrypted = CASE
        WHEN sqlc.arg('status')::text = 'dead' THEN payload_encrypted
        ELSE NULL
    END,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_class = sqlc.narg('last_error_class'),
    last_error = sqlc.narg('last_error'),
    processed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'processing'
  AND lease_token = sqlc.arg('lease_token')
RETURNING *;

-- name: PurgeTerminalDingTalkStreamInbox :execrows
DELETE FROM dingtalk_stream_inbox
WHERE status IN ('processed', 'discarded', 'dead')
  AND processed_at < sqlc.arg('cutoff');

-- name: RequeueDeadDingTalkStreamInbox :one
-- Deliberately narrow operator repair primitive: only a retained dead-letter
-- can return to the queue. The caller identifies the SLS-correlated inbox UUID;
-- processed/discarded rows and dead rows whose ciphertext was already purged
-- cannot be replayed accidentally.
UPDATE dingtalk_stream_inbox
SET status = 'queued',
    attempt_count = 0,
    available_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_class = NULL,
    last_error = NULL,
    processed_at = NULL,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status = 'dead'
  AND payload_encrypted IS NOT NULL
RETURNING *;
