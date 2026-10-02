CREATE TABLE IF NOT EXISTS employee_learning_consumption (
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 tenant_org_id text NOT NULL,
 scene_id uuid NOT NULL,
 task_id uuid NOT NULL,
 run_id uuid NOT NULL,
 queue_task_id uuid NOT NULL,
 requester_ref text NOT NULL,
 state text NOT NULL CHECK(state IN ('captured','skipped')),
 learning_id uuid,
 reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((state='captured' AND learning_id IS NOT NULL AND reason='') OR (state='skipped' AND learning_id IS NULL AND reason<>''))
);
