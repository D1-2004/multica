CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_prompt_component_name_idx ON context_prompt_component (agent_id, scope_type, org_id, scope_key, name);
