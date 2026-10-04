-- Workspace-managed OAuth application registrations and the authorization
-- instances bound to a workspace, agent, project, or environment.
-- client_secret and instance tokens are ciphertext only.
-- Relationships are enforced in application code. Indexes are built in
-- their own concurrent migrations.

CREATE TABLE IF NOT EXISTS connector_app (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    provider text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    client_id text NOT NULL,
    client_secret_ciphertext bytea,
    client_secret_hint text NOT NULL DEFAULT '',
    scopes text NOT NULL DEFAULT '',
    authorization_endpoint text NOT NULL DEFAULT '',
    token_endpoint text NOT NULL DEFAULT '',
    callback_mode text NOT NULL DEFAULT 'production_forward',
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_app_provider_chk CHECK (provider ~ '^[a-z][a-z0-9-]{0,31}$'),
    CONSTRAINT connector_app_callback_chk CHECK (callback_mode IN ('self', 'production_forward')),
    CONSTRAINT connector_app_client_id_chk CHECK (char_length(client_id) BETWEEN 1 AND 512),
    CONSTRAINT connector_app_name_chk CHECK (char_length(display_name) <= 120),
    CONSTRAINT connector_app_scopes_chk CHECK (char_length(scopes) <= 2048),
    CONSTRAINT connector_app_authorization_endpoint_chk CHECK (char_length(authorization_endpoint) <= 512),
    CONSTRAINT connector_app_token_endpoint_chk CHECK (char_length(token_endpoint) <= 512)
);

CREATE TABLE IF NOT EXISTS connector_auth_instance (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    label text NOT NULL,
    external_subject text NOT NULL DEFAULT '',
    external_login text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending',
    token_ciphertext bytea,
    token_hint text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_auth_instance_label_chk CHECK (char_length(label) BETWEEN 1 AND 120),
    CONSTRAINT connector_auth_instance_subject_chk CHECK (char_length(external_subject) <= 256),
    CONSTRAINT connector_auth_instance_login_chk CHECK (char_length(external_login) <= 128),
    CONSTRAINT connector_auth_instance_status_chk CHECK (status IN ('pending', 'active', 'disabled', 'needs_reauth'))
);

CREATE TABLE IF NOT EXISTS connector_auth_binding (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id uuid NOT NULL,
    app_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    scope_kind text NOT NULL,
    scope_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_auth_binding_kind_chk CHECK (scope_kind IN ('workspace', 'agent', 'project', 'environment')),
    CONSTRAINT connector_auth_binding_scope_chk CHECK (
        (scope_kind = 'workspace' AND scope_id = '')
        OR (scope_kind <> 'workspace' AND char_length(scope_id) BETWEEN 1 AND 128)
    )
);
