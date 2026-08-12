-- Always read the deployment fence from its authoritative row while holding
-- the transaction-scoped shared transition barrier. A connection-scoped custom
-- GUC can outlive the transition that populated it and keep rejecting admission
-- after the fence has returned to normal.
CREATE OR REPLACE FUNCTION multica_current_deployment_fence_state()
RETURNS TEXT
LANGUAGE plpgsql
AS $$
DECLARE
    current_state TEXT;
BEGIN
    PERFORM pg_advisory_xact_lock_shared(7244554146635925502);
    SELECT fence.state
    INTO STRICT current_state
    FROM deployment_fence AS fence
    WHERE fence.singleton_id = 1;
    RETURN current_state;
END;
$$;
