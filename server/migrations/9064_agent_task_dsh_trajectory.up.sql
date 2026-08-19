CREATE TABLE agent_task_dsh_trajectory (
    task_id UUID PRIMARY KEY REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL CHECK (
        session_id <> ''
        AND length(session_id) <= 128
    ),
    storage_key TEXT NOT NULL CHECK (
        storage_key <> ''
        AND length(storage_key) <= 512
    ),
    encryption_scheme TEXT NOT NULL CHECK (encryption_scheme = 'aes-256-gcm-v1'),
    encryption_key BYTEA NOT NULL CHECK (octet_length(encryption_key) = 32),
    sha256 CHAR(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 33554432),
    stored_size_bytes BIGINT NOT NULL CHECK (stored_size_bytes = size_bytes + 28),
    event_count INTEGER NOT NULL CHECK (event_count >= 0),
    format_version INTEGER NOT NULL CHECK (format_version >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE agent_task_dsh_trajectory IS
    'Private, task-scoped index for a DSH native session trajectory stored in object storage.';

COMMENT ON COLUMN agent_task_dsh_trajectory.storage_key IS
    'Internal ciphertext object key only. User APIs decrypt and stream it after task and private-agent authorization and never expose this key or a bucket URL.';

COMMENT ON COLUMN agent_task_dsh_trajectory.encryption_key IS
    'Per-object AES-256 data key. Object storage receives only nonce-prefixed authenticated ciphertext, because attachment buckets and local /uploads routes may be public.';
