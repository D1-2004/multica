ALTER TABLE runner_machine
    ADD COLUMN IF NOT EXISTS connection_id UUID,
    ADD COLUMN IF NOT EXISTS connected_at TIMESTAMPTZ;

ALTER TABLE agent_runner_binding
    ADD COLUMN IF NOT EXISTS disconnected_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS disconnected_by UUID REFERENCES "user"(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS runner_reconnect_session (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    binding_id UUID NOT NULL REFERENCES agent_runner_binding(id) ON DELETE CASCADE,
    machine_id UUID NOT NULL REFERENCES runner_machine(id) ON DELETE CASCADE,
    token_hash CHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON COLUMN runner_machine.connection_id IS
    'Server-assigned identifier for the currently registered Runner WebSocket';
COMMENT ON COLUMN agent_runner_binding.disconnected_at IS
    'Agent-scoped logical disconnect; the shared machine process exits only when no connected bindings remain';
COMMENT ON TABLE runner_reconnect_session IS
    'Short-lived one-time credentials for reconnecting one Agent Runner binding from the bound machine';
