-- Prompt components of the Context Builder: several named prompts per org
-- (enterprise), group scene or person scope of an agent, each {name,
-- position, text}. The effective context merges them by name, nearest layer
-- wins (global -> org -> scene -> person). An org scope uses
-- scope_key = org_id. No foreign keys; the workspace deletion sweep removes
-- rows explicitly.
CREATE TABLE IF NOT EXISTS context_prompt_component (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    scope_type text NOT NULL,
    org_id text NOT NULL DEFAULT '',
    scope_key text NOT NULL,
    name text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    text text NOT NULL,
    updated_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT context_prompt_component_scope_type_check CHECK (scope_type IN ('org', 'scene', 'person')),
    CONSTRAINT context_prompt_component_name_check CHECK (char_length(name) BETWEEN 1 AND 64),
    CONSTRAINT context_prompt_component_text_check CHECK (char_length(text) BETWEEN 1 AND 8000)
);
