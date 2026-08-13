DROP TABLE IF EXISTS runner_reconnect_session;

ALTER TABLE agent_runner_binding
    DROP COLUMN IF EXISTS disconnected_by,
    DROP COLUMN IF EXISTS disconnected_at;

ALTER TABLE runner_machine
    DROP COLUMN IF EXISTS connected_at,
    DROP COLUMN IF EXISTS connection_id;
