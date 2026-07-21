-- Repair rows rewritten by the superseded unified Router migration.
-- That migration staged every existing DingTalk installation as pending even
-- though historical Stream robots do not participate in Router registration.
-- Limit recovery to rows that never acquired a Router association so a real
-- callback cutover cannot be reactivated as Stream ingress accidentally.
UPDATE channel_installation
SET config = config
        - 'router_source_id'
        - 'router_agent_id'
        - 'router_registration_status'
        - 'ingress_cutover_state',
    status = 'active',
    updated_at = now()
WHERE channel_type = 'dingtalk'
  AND status = 'pending'
  AND config ->> 'router_registration_status' = 'router_pending'
  AND config ->> 'ingress_cutover_state' = 'legacy_stream'
  AND NULLIF(config ->> 'router_source_id', '') IS NULL
  AND NULLIF(config ->> 'router_agent_id', '') IS NULL;
