-- One webhook delivery of a scene routine (例行任务) whose agent is in employee
-- mode, run as an EmployeeTask Direct execution: the frozen, Host-verified
-- source of that execution (automation origin kind scene_routine_webhook).
-- The row commits in the same transaction as the Task, its Run, the queue row
-- and the AutopilotRun -> queue mapping. The AutopilotRun is the delivery's
-- own (autopilot_run.webhook_delivery_id); there is no planned_at.
-- source_event_id is "delivery/<webhook_delivery_id>": the delivery already
-- holds the provider event identity (trigger + event id, or one request).
-- occurred_at is DB time at the first commit; received_at is the delivery's.
-- input holds the routine facts and the allowlisted payload fields, never
-- the whole body, a token or a secret. No foreign keys; workspace deletion
-- removes rows explicitly.
CREATE TABLE IF NOT EXISTS employee_webhook_occurrence (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 tenant_org_id text NOT NULL,
 scene_id uuid NOT NULL,
 routine_id uuid NOT NULL,
 autopilot_id uuid NOT NULL,
 trigger_id uuid NOT NULL,
 webhook_delivery_id uuid NOT NULL,
 source text NOT NULL CHECK (source = 'scene.routine.webhook'),
 source_event_id text NOT NULL CHECK (source_event_id <> ''),
 identity_policy text NOT NULL CHECK (identity_policy IN ('provider_event_id', 'per_request')),
 provider_event_id text NOT NULL DEFAULT '',
 source_digest text NOT NULL CHECK (source_digest <> ''),
 received_at timestamptz NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 state text NOT NULL CHECK (state IN ('accepted', 'skipped', 'failed')),
 reason text NOT NULL DEFAULT '',
 autopilot_run_id uuid NOT NULL,
 employee_task_id uuid,
 employee_run_id uuid,
 queue_task_id uuid,
 creator_kind text NOT NULL DEFAULT '' CHECK (creator_kind IN ('member', 'agent', '')),
 creator_id uuid,
 requester_ref text NOT NULL,
 config_revision text NOT NULL DEFAULT '',
 dispatch_mode text NOT NULL CHECK (dispatch_mode = 'employee_direct'),
 authorization_ref text NOT NULL DEFAULT '',
 payload_fields jsonb NOT NULL DEFAULT '[]'::jsonb,
 input jsonb NOT NULL DEFAULT '{}'::jsonb,
 prompt_sha256 text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((identity_policy = 'provider_event_id') = (provider_event_id <> '')),
 CHECK ((state = 'accepted' AND employee_task_id IS NOT NULL AND employee_run_id IS NOT NULL AND queue_task_id IS NOT NULL
         AND reason = '' AND prompt_sha256 <> '' AND creator_kind <> '' AND creator_id IS NOT NULL)
     OR (state <> 'accepted' AND employee_task_id IS NULL AND employee_run_id IS NULL AND queue_task_id IS NULL AND reason <> ''))
);
