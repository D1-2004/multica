CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS git_connection_code_identity_idx ON git_connection (workspace_id, account_login) WHERE provider = 'alibaba_code';
