CREATE TABLE IF NOT EXISTS coordinator_user_decision_consumer (
 environment text NOT NULL,
 sender_uid text NOT NULL,
 sender_org_id text NOT NULL,
 owner uuid NOT NULL,
 lease_expires_at timestamptz NOT NULL,
 ready boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(environment,sender_uid,sender_org_id)
);
ALTER TABLE coordinator_user_decision ADD COLUMN IF NOT EXISTS interpretation jsonb;
ALTER TABLE coordinator_user_decision ADD COLUMN IF NOT EXISTS execution_result jsonb;
ALTER TABLE coordinator_user_decision ADD COLUMN IF NOT EXISTS review_label jsonb;
