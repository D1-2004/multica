CREATE TABLE IF NOT EXISTS dsh_storage_provision (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    spec jsonb NOT NULL CHECK (jsonb_typeof(spec) = 'object'),
    intent uuid NOT NULL,
    step integer NOT NULL DEFAULT 0 CHECK (step BETWEEN 0 AND 6),
    state text NOT NULL DEFAULT 'planned' CHECK (state IN ('planned', 'creating', 'complete')),
    resources text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (cardinality(resources) = step),
    CHECK (state <> 'complete' OR step = 6),
    CHECK (state <> 'creating' OR step < 6)
);
