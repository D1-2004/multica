DROP TABLE IF EXISTS agent_template_github_source;
ALTER TABLE github_installation
    DROP CONSTRAINT IF EXISTS github_installation_workspace_id_id_unique;
DROP TABLE IF EXISTS agent_template;
DROP TABLE IF EXISTS platform_template_seed;
