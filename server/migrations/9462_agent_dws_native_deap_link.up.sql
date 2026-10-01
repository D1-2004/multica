-- How a DingTalk digital employee's (DEAP's) DWS credential is issued. An
-- Agent Identity credential of a digital employee acts as another
-- principal: it receives none of the employee's messages and cannot read or
-- reply in its conversations. DEAP issues the employee's own DWS auth code,
-- but only to the employee's supervisor. A row names the DEAP employee
-- (deap_agent_uuid) and the supervisor whose Agent Identity credential
-- requests that code; the DWS identity provider then issues every server
-- path's credential for the account that way (sandbox tasks excepted).
-- dws_uid/org_id are the account the link was made for: it applies only
-- while the agent's identity is still that account. Set by deployment
-- operators only. No foreign keys: the application removes the row when the
-- identity is unbound, and ignores it for any other account.
CREATE TABLE IF NOT EXISTS agent_dws_native_deap_link (
    agent_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    dws_uid TEXT NOT NULL,
    org_id TEXT NOT NULL,
    deap_agent_uuid TEXT NOT NULL,
    supervisor_uid TEXT NOT NULL,
    updated_by UUID NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
