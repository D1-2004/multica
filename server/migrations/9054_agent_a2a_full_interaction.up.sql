-- Expand inbound A2A from a one-message projection to a durable public task
-- with ordered turns, events, artifacts, and push delivery. All changes are
-- additive so an old server can continue reading the original binding fields
-- during the fenced rollout.

ALTER TABLE a2a_task_binding
    ADD COLUMN IF NOT EXISTS public_state TEXT NOT NULL DEFAULT 'TASK_STATE_SUBMITTED',
    ADD COLUMN IF NOT EXISTS status_message JSONB,
    ADD COLUMN IF NOT EXISTS status_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS next_event_sequence BIGINT NOT NULL DEFAULT 1;

ALTER TABLE a2a_task_binding
    DROP CONSTRAINT IF EXISTS a2a_task_binding_public_state_check;
ALTER TABLE a2a_task_binding
    ADD CONSTRAINT a2a_task_binding_public_state_check CHECK (public_state IN (
        'TASK_STATE_AUTH_REQUIRED',
        'TASK_STATE_CANCELED',
        'TASK_STATE_COMPLETED',
        'TASK_STATE_FAILED',
        'TASK_STATE_INPUT_REQUIRED',
        'TASK_STATE_REJECTED',
        'TASK_STATE_SUBMITTED',
        'TASK_STATE_WORKING'
    ));

-- Give the task-binding foreign keys a matching composite ownership target.
-- Existing single-column uniqueness remains unchanged.
CREATE UNIQUE INDEX IF NOT EXISTS idx_a2a_task_binding_identity
    ON a2a_task_binding(id, endpoint_id, client_id);

CREATE TABLE IF NOT EXISTS a2a_task_turn (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    binding_id UUID NOT NULL REFERENCES a2a_task_binding(id) ON DELETE CASCADE,
    endpoint_id UUID NOT NULL,
    client_id UUID NOT NULL,
    accepted_credential_id UUID REFERENCES a2a_client_credential(id) ON DELETE SET NULL,
    sequence INTEGER NOT NULL,
    message_id TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    local_task_id UUID NOT NULL REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    input_chat_message_id UUID NOT NULL REFERENCES chat_message(id) ON DELETE CASCADE,
    input_parts JSONB NOT NULL DEFAULT '[]'::jsonb,
    message_extensions TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    message_metadata JSONB,
    reference_task_ids TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    accepted_output_modes TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    control_signal TEXT,
    control_payload JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    FOREIGN KEY (binding_id, endpoint_id, client_id)
        REFERENCES a2a_task_binding(id, endpoint_id, client_id) ON DELETE CASCADE,
    UNIQUE (binding_id, sequence),
    UNIQUE (endpoint_id, client_id, message_id),
    UNIQUE (local_task_id),
    UNIQUE (input_chat_message_id),
    CHECK (sequence > 0),
    CHECK (char_length(message_id) BETWEEN 1 AND 256),
    CHECK (btrim(message_id) = message_id),
    CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(input_parts) = 'array'),
    CHECK (cardinality(message_extensions) <= 16),
    CHECK (array_position(message_extensions, '') IS NULL),
    CHECK (cardinality(reference_task_ids) <= 10),
    CHECK (control_signal IS NULL OR control_signal IN ('input_required', 'auth_required'))
);

CREATE INDEX IF NOT EXISTS idx_a2a_task_turn_binding_sequence
    ON a2a_task_turn(binding_id, sequence);
CREATE INDEX IF NOT EXISTS idx_a2a_task_turn_client_created
    ON a2a_task_turn(endpoint_id, client_id, created_at DESC);

CREATE TABLE IF NOT EXISTS a2a_artifact (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    binding_id UUID NOT NULL REFERENCES a2a_task_binding(id) ON DELETE CASCADE,
    public_artifact_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    extensions TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    metadata JSONB,
    parts JSONB NOT NULL DEFAULT '[]'::jsonb,
    append BOOLEAN NOT NULL DEFAULT FALSE,
    last_chunk BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (binding_id, public_artifact_id),
    CHECK (char_length(public_artifact_id) BETWEEN 1 AND 128),
    CHECK (btrim(public_artifact_id) = public_artifact_id),
    CHECK (jsonb_typeof(parts) = 'array')
);

CREATE INDEX IF NOT EXISTS idx_a2a_artifact_binding_created
    ON a2a_artifact(binding_id, created_at, id);

CREATE TABLE IF NOT EXISTS a2a_task_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    binding_id UUID NOT NULL REFERENCES a2a_task_binding(id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (binding_id, sequence),
    UNIQUE (binding_id, dedupe_key),
    CHECK (sequence > 0),
    CHECK (event_type IN ('task', 'status', 'artifact')),
    CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_a2a_task_event_binding_sequence
    ON a2a_task_event(binding_id, sequence);

CREATE TABLE IF NOT EXISTS a2a_push_config (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    binding_id UUID NOT NULL REFERENCES a2a_task_binding(id) ON DELETE CASCADE,
    endpoint_id UUID NOT NULL,
    client_id UUID NOT NULL,
    public_config_id TEXT NOT NULL,
    callback_url TEXT NOT NULL,
    notification_token_encrypted BYTEA,
    auth_scheme TEXT,
    auth_credentials_encrypted BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (binding_id, endpoint_id, client_id)
        REFERENCES a2a_task_binding(id, endpoint_id, client_id) ON DELETE CASCADE,
    UNIQUE (binding_id, public_config_id),
    CHECK (char_length(public_config_id) BETWEEN 1 AND 128),
    CHECK (btrim(public_config_id) = public_config_id),
    CHECK (auth_scheme IS NULL OR auth_scheme IN ('basic', 'bearer')),
    CHECK ((auth_scheme IS NULL) = (auth_credentials_encrypted IS NULL))
);

CREATE INDEX IF NOT EXISTS idx_a2a_push_config_client
    ON a2a_push_config(endpoint_id, client_id, binding_id, created_at, public_config_id);

CREATE TABLE IF NOT EXISTS a2a_push_delivery (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    push_config_id UUID NOT NULL REFERENCES a2a_push_config(id) ON DELETE CASCADE,
    event_id UUID NOT NULL REFERENCES a2a_task_event(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (push_config_id, event_id),
    CHECK (status IN ('pending', 'delivering', 'delivered', 'dead_letter')),
    CHECK (attempt_count >= 0)
);

CREATE INDEX IF NOT EXISTS idx_a2a_push_delivery_pending
    ON a2a_push_delivery(next_attempt_at, created_at, id)
    WHERE status IN ('pending', 'delivering');

-- Every pre-existing binding is turn one. Preserve its public identifiers and
-- replay fingerprint exactly; the source chat message remains the durable text.
INSERT INTO a2a_task_turn (
    binding_id,
    endpoint_id,
    client_id,
    accepted_credential_id,
    sequence,
    message_id,
    request_fingerprint,
    local_task_id,
    input_chat_message_id,
    input_parts,
    created_at,
    updated_at,
    completed_at
)
SELECT
    binding.id,
    binding.endpoint_id,
    binding.client_id,
    binding.accepted_credential_id,
    1,
    binding.message_id,
    binding.request_fingerprint,
    binding.root_local_task_id,
    binding.input_chat_message_id,
    jsonb_build_array(jsonb_build_object(
        'kind', 'text',
        'text', input_message.content,
        'mediaType', 'text/plain'
    )),
    binding.created_at,
    binding.updated_at,
    task.completed_at
FROM a2a_task_binding binding
JOIN chat_message input_message ON input_message.id = binding.input_chat_message_id
JOIN agent_task_queue task ON task.id = binding.root_local_task_id
ON CONFLICT (binding_id, sequence) DO NOTHING;

-- Backfill the public state from the newest retry descendant of turn one.
WITH RECURSIVE lineage AS (
    SELECT binding.id AS binding_id, task.id, task.status, task.created_at, 0 AS depth
    FROM a2a_task_binding binding
    JOIN agent_task_queue task ON task.id = binding.root_local_task_id
    UNION ALL
    SELECT parent.binding_id, child.id, child.status, child.created_at, parent.depth + 1
    FROM lineage parent
    JOIN agent_task_queue child ON child.parent_task_id = parent.id
), latest AS (
    SELECT DISTINCT ON (binding_id) binding_id, status
    FROM lineage
    ORDER BY binding_id, depth DESC, created_at DESC, id DESC
)
UPDATE a2a_task_binding binding
SET public_state = CASE
        WHEN binding.cancel_requested_at IS NOT NULL THEN 'TASK_STATE_CANCELED'
        WHEN latest.status IN ('queued', 'deferred') THEN 'TASK_STATE_SUBMITTED'
        WHEN latest.status IN ('dispatched', 'running', 'waiting_local_directory') THEN 'TASK_STATE_WORKING'
        WHEN latest.status = 'completed' THEN 'TASK_STATE_COMPLETED'
        WHEN latest.status = 'failed' THEN 'TASK_STATE_FAILED'
        WHEN latest.status = 'cancelled' THEN 'TASK_STATE_CANCELED'
        ELSE 'TASK_STATE_REJECTED'
    END,
    status_updated_at = COALESCE(binding.updated_at, binding.created_at)
FROM latest
WHERE latest.binding_id = binding.id;

WITH RECURSIVE lineage AS (
    SELECT binding.id AS binding_id, task.id, task.parent_task_id, task.status, task.created_at, 0 AS depth
    FROM a2a_task_binding binding
    JOIN agent_task_queue task ON task.id = binding.root_local_task_id

    UNION ALL

    SELECT parent.binding_id, child.id, child.parent_task_id, child.status, child.created_at, parent.depth + 1
    FROM lineage parent
    JOIN agent_task_queue child ON child.parent_task_id = parent.id
), latest AS (
    SELECT DISTINCT ON (binding_id) binding_id, id AS local_task_id, status
    FROM lineage
    ORDER BY binding_id, depth DESC, created_at DESC, id DESC
)
INSERT INTO a2a_artifact (
    binding_id,
    public_artifact_id,
    name,
    parts,
    created_at,
    updated_at
)
SELECT
    binding.id,
    binding.artifact_id,
    'result',
    jsonb_build_array(jsonb_build_object(
        'kind', 'text',
        'text', outcome.content,
        'mediaType', 'text/plain'
    )),
    outcome.created_at,
    outcome.created_at
FROM a2a_task_binding binding
JOIN latest ON latest.binding_id = binding.id
JOIN LATERAL (
    SELECT message.content, message.created_at
    FROM chat_message message
    WHERE message.task_id = latest.local_task_id
      AND message.role = 'assistant'
    ORDER BY message.created_at DESC, message.id DESC
    LIMIT 1
) outcome ON TRUE
WHERE binding.public_state = 'TASK_STATE_COMPLETED'
  AND latest.status = 'completed'
ON CONFLICT (binding_id, public_artifact_id) DO NOTHING;

-- Seed one current-state event per historical task. New writes allocate event
-- sequences transactionally through next_event_sequence.
INSERT INTO a2a_task_event (binding_id, sequence, event_type, dedupe_key, payload, created_at)
SELECT
    binding.id,
    1,
    'status',
    'migration-current-state',
    jsonb_build_object(
        'taskId', binding.public_task_id,
        'contextId', context.public_context_id,
        'status', jsonb_build_object(
            'state', binding.public_state,
            'timestamp', to_char(binding.status_updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
        ),
        'metadata', jsonb_build_object(
            'eventId',
            'evt_' || md5(binding.public_task_id || E'\\x00' || 'migration-current-state')
        )
    ),
    binding.status_updated_at
FROM a2a_task_binding binding
JOIN a2a_context context ON context.id = binding.context_id
ON CONFLICT (binding_id, dedupe_key) DO NOTHING;

UPDATE a2a_task_binding SET next_event_sequence = GREATEST(next_event_sequence, 2);
