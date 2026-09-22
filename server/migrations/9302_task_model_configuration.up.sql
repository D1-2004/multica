CREATE TABLE IF NOT EXISTS task_model_configuration (
 task_id text NOT NULL,
 document jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
