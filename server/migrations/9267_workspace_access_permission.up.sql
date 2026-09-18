ALTER TABLE workspace_access_token ADD COLUMN permission TEXT NOT NULL DEFAULT 'all' CHECK (permission IN ('all', 'dsh_config'));
