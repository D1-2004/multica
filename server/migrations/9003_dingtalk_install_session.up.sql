-- Persist the observable state of a DingTalk device-flow install session.
--
-- The session used to live only in the RegistrationService's in-process map.
-- That holds on a single replica, but this deployment runs several: the browser
-- POSTs /install/begin to one pod and its status polls are load-balanced across
-- all of them, so a poll served by any other pod found no session and returned
-- "install session not found" seconds after the QR was rendered. Any redeploy
-- wiped in-flight sessions for the same reason.
--
-- Only the state the poller needs to read is stored. The device code and the
-- polling goroutine stay in the memory of the pod that began the session — the
-- device code is a short-lived credential we do not want at rest, and one
-- driver per session is exactly what the device flow wants. If that pod dies
-- mid-flow the row simply ages out at expires_at and the user rescans.
--
-- Idempotent: this shipped as 167 before an upstream sync claimed that number,
-- and the runner keys applied migrations by full filename stem — so under its
-- new stem it re-runs against a database that already has the table.
CREATE TABLE IF NOT EXISTS dingtalk_install_session (
    -- Opaque random id minted by the service; also the URL path segment the
    -- frontend polls, so it is the natural primary key.
    id              TEXT PRIMARY KEY,
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'success', 'error')),
    -- Set when status='success'.
    installation_id UUID REFERENCES channel_installation(id) ON DELETE SET NULL,
    -- Stable reason code + human message; both empty unless status='error'.
    error_reason    TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    -- Device-code expiry. A row still 'pending' past this is reported as
    -- expired, which also covers the case where the driving pod went away.
    expires_at      TIMESTAMPTZ NOT NULL,
    -- Set when the session reaches a terminal state: the row is dropped after
    -- this, giving the frontend a window to read the final status.
    gc_after        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the sweep: terminal rows past gc_after, and pending rows abandoned
-- long after they expired.
CREATE INDEX IF NOT EXISTS idx_dingtalk_install_session_sweep
    ON dingtalk_install_session (gc_after, expires_at);
