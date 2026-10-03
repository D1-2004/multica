-- Cross-scene input ledger for one EmployeeTask
-- (docs/plans/2026-10-03/employee-loop-backend-delivery/02-collection.md).
-- A collection asks invited participants in other scenes for bounded answers.
-- Invitations carry the participant authorization and the delivery facts;
-- inputs are the accepted answers keyed by their source message, whose B-scene
-- receipt stays where it was admitted; a ready intent is the durable request to
-- wake the origin scene once every mandatory slot is filled. These tables are
-- input and authorization records, not an execution queue.
--
-- Every row carries its workspace/agent/tenant/task ownership. No FOREIGN KEY
-- or cascade: relationships and cleanup are application-owned, and indexes are
-- built concurrently in the following single-statement migrations.
CREATE TABLE IF NOT EXISTS employee_task_collection (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    task_id uuid NOT NULL,
    origin_scene_id uuid NOT NULL,
    authority_ref text NOT NULL CHECK (char_length(authority_ref) BETWEEN 1 AND 512),
    requester_ref text NOT NULL CHECK (char_length(requester_ref) BETWEEN 1 AND 256),
    delivery_anchor_ref text NOT NULL CHECK (char_length(delivery_anchor_ref) BETWEEN 1 AND 512),
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    state text NOT NULL DEFAULT 'open'
        CHECK (state IN ('open', 'ready', 'summarizing', 'completed', 'cancelled', 'revoked', 'expired')),
    completion_rule text NOT NULL DEFAULT 'all_required' CHECK (completion_rule IN ('all_required')),
    expected_count integer NOT NULL CHECK (expected_count BETWEEN 1 AND 32),
    received_count integer NOT NULL DEFAULT 0 CHECK (received_count >= 0),
    deadline_at timestamptz,
    deadline_tz text NOT NULL DEFAULT '',
    source_namespace text NOT NULL,
    source_key text NOT NULL,
    create_payload jsonb NOT NULL,
    close_mode text NOT NULL DEFAULT '' CHECK (close_mode IN ('', 'partial', 'cancel', 'revoke', 'expire')),
    close_actor_ref text NOT NULL DEFAULT '',
    close_reason text NOT NULL DEFAULT '',
    close_source_namespace text NOT NULL DEFAULT '',
    close_source_key text NOT NULL DEFAULT '',
    close_payload jsonb,
    close_revision bigint NOT NULL DEFAULT 0 CHECK (close_revision >= 0),
    closed_at timestamptz,
    summary_ref text NOT NULL DEFAULT '',
    complete_source_namespace text NOT NULL DEFAULT '',
    complete_source_key text NOT NULL DEFAULT '',
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_collection_count_check CHECK (received_count <= expected_count),
    CONSTRAINT employee_task_collection_deadline_check CHECK ((deadline_at IS NULL) = (deadline_tz = ''))
);

CREATE TABLE IF NOT EXISTS employee_task_invitation (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    task_id uuid NOT NULL,
    collection_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 1 AND 32),
    collection_revision bigint NOT NULL CHECK (collection_revision > 0),
    -- NULL only for a 1:1 invitation to a person who has no DM scene yet
    -- (pending_scene). The scene is backfilled once from the outbound send's
    -- provider receipt via scene.Resolve; it is never minted from a person.
    target_scene_id uuid,
    target_scene_kind text NOT NULL CHECK (target_scene_kind IN ('dm', 'group')),
    participant_ref text NOT NULL CHECK (char_length(participant_ref) BETWEEN 1 AND 256),
    authorization_scope text NOT NULL DEFAULT 'answer_own_question'
        CHECK (authorization_scope IN ('answer_own_question')),
    question text NOT NULL CHECK (char_length(question) BETWEEN 1 AND 2000),
    expires_at timestamptz,
    delivery_state text NOT NULL DEFAULT 'pending_delivery'
        CHECK (delivery_state IN ('pending_scene', 'pending_delivery', 'delivered', 'answered', 'revoked', 'expired')),
    delivery_attempt integer NOT NULL DEFAULT 1 CHECK (delivery_attempt BETWEEN 1 AND 8),
    delivery_action_id text NOT NULL CHECK (char_length(delivery_action_id) BETWEEN 1 AND 128),
    delivery_outcome text NOT NULL DEFAULT 'pending'
        CHECK (delivery_outcome IN ('pending', 'unknown', 'failed', 'held', 'sent')),
    delivery_error text NOT NULL DEFAULT '',
    provider_message_id text NOT NULL DEFAULT '',
    rendered_hash text NOT NULL DEFAULT '' CHECK (rendered_hash = '' OR rendered_hash ~ '^[0-9a-f]{64}$'),
    effective_version integer NOT NULL DEFAULT 0 CHECK (effective_version >= 0),
    end_reason text NOT NULL DEFAULT '',
    end_source_namespace text NOT NULL DEFAULT '',
    end_source_key text NOT NULL DEFAULT '',
    delivered_at timestamptz,
    answered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_invitation_scene_check CHECK (
        (delivery_state = 'pending_scene' AND target_scene_id IS NULL AND target_scene_kind = 'dm') OR
        (delivery_state IN ('pending_delivery', 'delivered', 'answered') AND target_scene_id IS NOT NULL) OR
        (delivery_state IN ('revoked', 'expired') AND (target_scene_id IS NOT NULL OR target_scene_kind = 'dm')))
);

CREATE TABLE IF NOT EXISTS employee_task_input (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    task_id uuid NOT NULL,
    collection_id uuid NOT NULL,
    invitation_id uuid NOT NULL,
    version integer NOT NULL CHECK (version BETWEEN 1 AND 64),
    kind text NOT NULL CHECK (kind IN ('answer', 'correction')),
    binding text NOT NULL CHECK (binding IN ('reply_chain', 'dm_single_pending', 'invite_reference')),
    source_namespace text NOT NULL,
    source_key text NOT NULL,
    source_scene_id uuid NOT NULL,
    receipt_ref text NOT NULL CHECK (char_length(receipt_ref) BETWEEN 1 AND 512),
    provider_message_id text NOT NULL DEFAULT '',
    actor_ref text NOT NULL CHECK (char_length(actor_ref) BETWEEN 1 AND 256),
    occurred_at timestamptz NOT NULL,
    body text NOT NULL DEFAULT '',
    body_ref text NOT NULL DEFAULT '',
    collection_revision bigint NOT NULL CHECK (collection_revision > 0),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_input_body_check CHECK (body <> '' OR body_ref <> '')
);

CREATE TABLE IF NOT EXISTS employee_task_ready_intent (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    task_id uuid NOT NULL,
    collection_id uuid NOT NULL,
    collection_revision bigint NOT NULL CHECK (collection_revision > 0),
    goal_revision bigint NOT NULL CHECK (goal_revision > 0),
    origin_scene_id uuid NOT NULL,
    event_source text NOT NULL DEFAULT 'employee.collection',
    event_type text NOT NULL DEFAULT 'collection.ready',
    event_id text NOT NULL,
    occurred_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'admitted', 'superseded')),
    admitted_ref text NOT NULL DEFAULT '',
    admitted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
