-- Directory facts about an employee agent's own DingTalk account in one
-- tenant org (supervisor, department, title), read by the Host from that
-- org's address book through the agent's DWS identity. Zero model calls.
-- status: pending (never read), known (value set), unregistered (the
-- address book has no such entry, recorded explicitly). A failed refresh
-- keeps status and value and only records error_code. The supervisor row
-- also carries the agent's refresh lease (lease_token, lease_until).
-- No FK by design; the unique index is built concurrently in the next file.
CREATE TABLE IF NOT EXISTS employee_agent_profile_fact (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (char_length(tenant_org_id) BETWEEN 1 AND 128),
    fact_key text NOT NULL CHECK (fact_key IN ('supervisor', 'department', 'title')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'known', 'unregistered')),
    value text NOT NULL DEFAULT '' CHECK (octet_length(value) <= 256),
    value_ref text NOT NULL DEFAULT '' CHECK (octet_length(value_ref) <= 256),
    source text NOT NULL DEFAULT 'dws_contact' CHECK (source IN ('dws_contact')),
    dws_uid text NOT NULL DEFAULT '' CHECK (octet_length(dws_uid) <= 128),
    refreshed_at timestamptz,
    attempted_at timestamptz,
    lease_token uuid,
    lease_until timestamptz,
    error_code text NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (status <> 'known' OR value <> '' OR value_ref <> ''),
    CHECK (status = 'pending' OR refreshed_at IS NOT NULL)
);
