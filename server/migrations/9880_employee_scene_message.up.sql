-- Persistent group transcript of an employee agent's group scenes (M8): the
-- human lines of groups that already have an agent_scene for this agent and
-- tenant, kept 14 days. Rows arrive from the all-group-messages native
-- subscription (native_group) and as a by-product of wake-time history reads
-- (wake_read). A row is never revived once withdrawn. proactive_* is the
-- deterministic Host wake gate state of an unaddressed question (DS-09).
-- No FK; indexes are built concurrently in the following files.
CREATE TABLE IF NOT EXISTS employee_scene_message (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    scene_id uuid NOT NULL,
    provider text NOT NULL DEFAULT 'dingtalk' CHECK (provider IN ('dingtalk')),
    provider_message_id text NOT NULL CHECK (char_length(provider_message_id) BETWEEN 1 AND 256),
    sent_at timestamptz NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    source text NOT NULL CHECK (source IN ('wake_read', 'native_group')),
    receipt_id uuid,
    sender_class text NOT NULL CHECK (sender_class IN ('human', 'self', 'bot', 'unknown')),
    sender_ref text NOT NULL DEFAULT '' CHECK (octet_length(sender_ref) <= 320),
    sender_name text NOT NULL DEFAULT '' CHECK (octet_length(sender_name) <= 128),
    quoted_message_id text NOT NULL DEFAULT '' CHECK (octet_length(quoted_message_id) <= 256),
    body text NOT NULL CHECK (octet_length(body) <= 4096),
    original_bytes integer NOT NULL DEFAULT 0 CHECK (original_bytes >= 0),
    truncated boolean NOT NULL DEFAULT false,
    withdrawn_at timestamptz,
    withdrawn_reason text NOT NULL DEFAULT '' CHECK (withdrawn_reason IN ('', 'recalled', 'memory_forget', 'scene_reset', 'retention')),
    proactive_state text NOT NULL DEFAULT '' CHECK (proactive_state IN ('', 'pending', 'admitted', 'skipped')),
    proactive_due_at timestamptz,
    proactive_reason text NOT NULL DEFAULT '' CHECK (octet_length(proactive_reason) <= 64),
    proactive_decided_at timestamptz,
    CONSTRAINT employee_scene_message_withdrawn_check CHECK ((withdrawn_at IS NULL) = (withdrawn_reason = '')),
    CONSTRAINT employee_scene_message_proactive_check CHECK (proactive_state <> 'pending' OR proactive_due_at IS NOT NULL)
);
