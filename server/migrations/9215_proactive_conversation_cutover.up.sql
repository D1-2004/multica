-- Old replicas must return a retryable admission error after cutover, rather
-- than dispatching the same message through the former Autopilot inbox.
CREATE OR REPLACE FUNCTION reject_legacy_event_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM agent_event_stream s JOIN agent_event_trigger c ON c.agent_id=s.agent_id AND c.workspace_id=s.workspace_id WHERE s.id=NEW.stream_id AND c.delivery_mode='coordinator') THEN
  RAISE EXCEPTION 'observed messages now require Coordinator admission';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS reject_legacy_event_admission ON agent_event;
CREATE TRIGGER reject_legacy_event_admission BEFORE INSERT ON agent_event FOR EACH ROW EXECUTE FUNCTION reject_legacy_event_admission();

CREATE OR REPLACE FUNCTION enforce_proactive_coordinator() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.enabled AND NEW.delivery_mode='coordinator' THEN
  UPDATE agent SET inbound_coordinator=true,
   dingtalk_response_policy_revision=dingtalk_response_policy_revision+CASE WHEN inbound_coordinator THEN 0 ELSE 1 END
   WHERE id=NEW.agent_id AND workspace_id=NEW.workspace_id;
 END IF;
 IF NEW.autopilot_id IS NOT NULL AND NEW.delivery_mode='coordinator' THEN
  UPDATE autopilot SET status='paused',pause_reason='event_trigger_migrated_to_coordinator',updated_at=now()
   WHERE id=NEW.autopilot_id AND workspace_id=NEW.workspace_id;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS enforce_proactive_coordinator ON agent_event_trigger;
CREATE TRIGGER enforce_proactive_coordinator BEFORE INSERT OR UPDATE ON agent_event_trigger FOR EACH ROW EXECUTE FUNCTION enforce_proactive_coordinator();

CREATE OR REPLACE FUNCTION disable_proactive_without_coordinator() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT NEW.inbound_coordinator THEN
  UPDATE agent_event_trigger SET enabled=false,revision=revision+1,updated_at=now()
   WHERE agent_id=NEW.id AND workspace_id=NEW.workspace_id AND enabled;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS disable_proactive_without_coordinator ON agent;
CREATE TRIGGER disable_proactive_without_coordinator AFTER UPDATE OF inbound_coordinator ON agent FOR EACH ROW EXECUTE FUNCTION disable_proactive_without_coordinator();
UPDATE agent_event_trigger SET delivery_mode='coordinator',revision=revision+1,updated_at=now();
