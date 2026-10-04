-- One committed verification attempt per (run, spec digest) with the evidence
-- generation that produced it. Discovery re-verifies a Run once when a newer
-- generation (for example provider-delivered files) can see more evidence.
-- No FK; the index is built concurrently in the next file.
CREATE TABLE IF NOT EXISTS employee_task_verification_attempt (
    run_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    spec_digest text NOT NULL CHECK (spec_digest ~ '^[0-9a-f]{64}$'),
    evidence_generation integer NOT NULL CHECK (evidence_generation > 0),
    attempted_at timestamptz NOT NULL DEFAULT now()
);
