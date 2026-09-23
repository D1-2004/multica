CREATE TABLE IF NOT EXISTS global_model_configuration (
 revision bigint NOT NULL,
 document jsonb NOT NULL,
 updated_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
