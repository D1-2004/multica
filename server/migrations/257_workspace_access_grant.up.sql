ALTER TABLE "user"
    ADD COLUMN principal_type TEXT NOT NULL DEFAULT 'human'
        CHECK (principal_type IN ('human', 'workspace_access_grant'));

CREATE TABLE workspace_access_grant (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    subject_user_id UUID NOT NULL UNIQUE REFERENCES "user"(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    capabilities TEXT[] NOT NULL,
    resource_scope TEXT NOT NULL DEFAULT 'own_agents'
        CHECK (resource_scope IN ('own_agents', 'workspace')),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'disabled')),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    updated_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at TIMESTAMPTZ,
    CHECK (cardinality(capabilities) > 0),
    CHECK (capabilities <@ ARRAY['deployment.manage', 'deployment.retire', 'trace.read']::TEXT[])
);

CREATE INDEX idx_workspace_access_grant_workspace
    ON workspace_access_grant(workspace_id, created_at DESC);

CREATE TABLE workspace_access_token (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    grant_id UUID NOT NULL REFERENCES workspace_access_grant(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (btrim(name) <> ''),
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_by UUID NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_by UUID REFERENCES "user"(id) ON DELETE RESTRICT,
    revoked_at TIMESTAMPTZ
);

CREATE INDEX idx_workspace_access_token_grant
    ON workspace_access_token(grant_id, created_at DESC);

CREATE TABLE workspace_access_audit (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    grant_id UUID REFERENCES workspace_access_grant(id) ON DELETE SET NULL,
    token_id UUID REFERENCES workspace_access_token(id) ON DELETE SET NULL,
    actor_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    resource_type TEXT,
    resource_id UUID,
    result TEXT NOT NULL DEFAULT 'success',
    request_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspace_access_audit_workspace_created
    ON workspace_access_audit(workspace_id, created_at DESC);
