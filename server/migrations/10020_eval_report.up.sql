CREATE TABLE IF NOT EXISTS eval_report (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    submitted_by UUID NOT NULL,
    run_id UUID NOT NULL,
    content_sha256 TEXT NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    definition_sha256 TEXT NOT NULL CHECK (definition_sha256 ~ '^[0-9a-f]{64}$'),
    submission JSONB NOT NULL CHECK (jsonb_typeof(submission) = 'object'),
    summary JSONB NOT NULL CHECK (jsonb_typeof(summary) = 'object'),
    received_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
