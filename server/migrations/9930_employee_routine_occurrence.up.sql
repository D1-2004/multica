-- One accepted (or refused) occurrence of a scene routine (例行任务) bound to
-- an employee-mode agent: the frozen, Host-verified source of the
-- EmployeeTask execution it admitted. The row commits in the same transaction
-- as its AutopilotRun, EmployeeTask/Run and queue row, so a replay of the same
-- occurrence reads this row and never recomputes planned_at, instructions,
-- principal or dispatch mode. source_event_id is the trigger plus the
-- canonical UTC planned_at for a schedule, or the AutopilotRun for a manual
-- run. occurred_at is DB time at the first commit. No secrets are stored.
-- No foreign keys; workspace deletion removes rows explicitly.
CREATE TABLE IF NOT EXISTS employee_routine_occurrence (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 tenant_org_id text NOT NULL,
 scene_id uuid NOT NULL,
 routine_id uuid NOT NULL,
 autopilot_id uuid NOT NULL,
 trigger_id uuid,
 source text NOT NULL CHECK (source IN ('scene.routine.schedule','scene.routine.manual')),
 source_event_id text NOT NULL CHECK (source_event_id <> ''),
 planned_at timestamptz,
 timezone text NOT NULL DEFAULT '',
 occurred_at timestamptz NOT NULL DEFAULT now(),
 state text NOT NULL CHECK (state IN ('accepted','skipped','skipped_overlap','failed')),
 reason text NOT NULL DEFAULT '',
 autopilot_run_id uuid NOT NULL,
 employee_task_id uuid,
 employee_run_id uuid,
 queue_task_id uuid,
 overlap_occurrence_id uuid,
 creator_kind text NOT NULL DEFAULT '' CHECK (creator_kind IN ('member','agent','')),
 creator_id uuid,
 manual_actor_id uuid,
 requester_ref text NOT NULL,
 config_revision text NOT NULL DEFAULT '',
 dispatch_mode text NOT NULL CHECK (dispatch_mode = 'employee_direct'),
 authorization_ref text NOT NULL DEFAULT '',
 input jsonb NOT NULL DEFAULT '{}'::jsonb,
 prompt_sha256 text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((source = 'scene.routine.schedule' AND trigger_id IS NOT NULL AND planned_at IS NOT NULL)
     OR (source = 'scene.routine.manual' AND planned_at IS NULL)),
 CHECK ((state = 'accepted' AND employee_task_id IS NOT NULL AND employee_run_id IS NOT NULL AND queue_task_id IS NOT NULL
         AND reason = '' AND prompt_sha256 <> '' AND creator_kind <> '' AND creator_id IS NOT NULL)
     OR (state <> 'accepted' AND employee_task_id IS NULL AND employee_run_id IS NULL AND queue_task_id IS NULL AND reason <> '')),
 CHECK ((state = 'skipped_overlap') = (overlap_occurrence_id IS NOT NULL))
);
