-- Host-built member roster of one group scene: display names with
-- org-qualified refs, plus directory-public facts (title, department) for
-- members the agent's own org address book proves; other members keep their
-- name only. Never phone, email or any personal memory. members is a bounded
-- JSON array (the first members by priority); member_total counts the whole
-- group. Refreshed at most daily per scene under the row's lease; a failed
-- refresh keeps the previous members and records error_code.
-- No FK by design; the unique index is built concurrently in the next file.
CREATE TABLE IF NOT EXISTS employee_scene_member_roster (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scene_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    dws_uid text NOT NULL DEFAULT '' CHECK (octet_length(dws_uid) <= 128),
    members jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(members) = 'array' AND octet_length(members::text) <= 131072),
    member_total integer NOT NULL DEFAULT 0 CHECK (member_total >= 0),
    refreshed_at timestamptz,
    attempted_at timestamptz,
    lease_token uuid,
    lease_until timestamptz,
    error_code text NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
