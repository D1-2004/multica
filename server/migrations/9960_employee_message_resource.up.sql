-- Frozen reads of Employee source-message resources. The first authorized
-- read of one own resource of a source message is kept, so a retry or replay
-- of that source reuses the same content hash and bounded text instead of
-- downloading again. Only small extracted text is stored: never a provider
-- URL, credential or node-local path. Transient provider failures are not
-- recorded. Indexes are built concurrently in separate migrations;
-- relationships and workspace cleanup are application-owned.
CREATE TABLE IF NOT EXISTS employee_message_resource (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL CHECK (tenant_org_id <> ''),
    scene_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    source_message_id text NOT NULL CHECK (source_message_id <> ''),
    requester_ref text NOT NULL CHECK (requester_ref <> ''),
    principal_id uuid NOT NULL,
    relation text NOT NULL CHECK (relation IN ('current', 'quoted')),
    message_id text NOT NULL CHECK (message_id <> ''),
    resource_id text NOT NULL CHECK (resource_id <> ''),
    resource_id_type text NOT NULL CHECK (resource_id_type IN ('fileId', 'mediaId')),
    resource_kind text NOT NULL,
    state text NOT NULL CHECK (state IN ('available', 'partial', 'unavailable', 'unsupported')),
    reason text NOT NULL DEFAULT '',
    file_name text NOT NULL DEFAULT '',
    media_type text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    content_sha256 text NOT NULL DEFAULT '',
    extracted_text text NOT NULL DEFAULT '' CHECK (octet_length(extracted_text) <= 65536),
    extract_start integer NOT NULL DEFAULT 0 CHECK (extract_start >= 0),
    extract_end integer NOT NULL DEFAULT 0 CHECK (extract_end >= extract_start),
    extract_total integer NOT NULL DEFAULT 0 CHECK (extract_total >= extract_end),
    created_at timestamptz NOT NULL DEFAULT now()
);
