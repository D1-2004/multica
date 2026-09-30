-- One dynamic client registration (RFC 7591) per official app connector,
-- reused by every OAuth connect of that connector. registration_ciphertext
-- is sealed with the internal connector secret box and binds
-- {workspace_id, connector_id, client_id, client_secret,
-- token_endpoint_auth_method}. No foreign keys: workspace deletion sweeps
-- this table explicitly.
CREATE TABLE IF NOT EXISTS connector_oauth_client (
    connector_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    issuer text NOT NULL DEFAULT '',
    authorization_endpoint text NOT NULL,
    token_endpoint text NOT NULL,
    resource text NOT NULL DEFAULT '',
    scope text NOT NULL DEFAULT '',
    redirect_uri text NOT NULL,
    registration_ciphertext bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
