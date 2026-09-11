ALTER TABLE agent_source ADD COLUMN package_binding_state JSONB NOT NULL DEFAULT '{}'::jsonb;
