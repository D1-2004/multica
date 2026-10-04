-- One follow-up decision per (plan revision, terminal Run): the Host consumed
-- that Run's verified terminal fact and either dispatched the next planned
-- step, woke the employee for a decision, completed the goal, or paused it.
-- next_run_id is the provenance of a Host-dispatched plan step.
CREATE TABLE IF NOT EXISTS employee_task_follow_up (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    scene_id uuid NOT NULL,
    task_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_revision bigint NOT NULL CHECK (plan_revision > 0),
    run_id uuid NOT NULL,
    step_index integer NOT NULL CHECK (step_index BETWEEN 1 AND 8),
    decision text NOT NULL CHECK (decision IN ('dispatched', 'woken', 'completed', 'paused', 'stopped', 'stale')),
    reason text NOT NULL DEFAULT '',
    next_step_index integer CHECK (next_step_index BETWEEN 2 AND 8),
    next_run_id uuid,
    next_queue_task_id uuid,
    wake_job_id uuid,
    note_action_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT employee_task_follow_up_next_check CHECK ((next_run_id IS NULL) = (next_queue_task_id IS NULL) AND (next_run_id IS NULL OR next_step_index IS NOT NULL))
);
