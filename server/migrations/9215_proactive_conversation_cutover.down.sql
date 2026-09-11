DROP TRIGGER IF EXISTS disable_proactive_without_coordinator ON agent;
DROP FUNCTION IF EXISTS disable_proactive_without_coordinator();
DROP TRIGGER IF EXISTS enforce_proactive_coordinator ON agent_event_trigger;
DROP FUNCTION IF EXISTS enforce_proactive_coordinator();
DROP TRIGGER IF EXISTS reject_legacy_event_admission ON agent_event;
DROP FUNCTION IF EXISTS reject_legacy_event_admission();
