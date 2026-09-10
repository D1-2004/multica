-- Keep old-message receipts during migration; new messages use Coordinator jobs.
ALTER TABLE agent_event_trigger ADD COLUMN IF NOT EXISTS delivery_mode text NOT NULL DEFAULT 'coordinator';
CREATE TABLE IF NOT EXISTS coordinator_observed_message (
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 source_key text NOT NULL,
 conversation_id text NOT NULL,
 message_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS coordinator_issue_follow_up (
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 issue_id uuid NOT NULL,
 comment_id uuid NOT NULL,
 idempotency_key text NOT NULL,
 fingerprint text NOT NULL,
 dispatch_context jsonb NOT NULL,
 task_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 dispatched_at timestamptz,
 last_error text
);
