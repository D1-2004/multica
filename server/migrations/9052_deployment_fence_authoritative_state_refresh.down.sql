CREATE OR REPLACE FUNCTION multica_current_deployment_fence_state()
RETURNS TEXT
LANGUAGE plpgsql
AS $$
DECLARE
    current_state TEXT;
BEGIN
    current_state := NULLIF(current_setting('multica.deployment_fence_state', true), '');
    IF current_state NOT IN ('normal', 'draining', 'frozen') THEN
        PERFORM pg_advisory_xact_lock_shared(7244554146635925502);
        SELECT fence.state
        INTO STRICT current_state
        FROM deployment_fence AS fence
        WHERE fence.singleton_id = 1;
        PERFORM set_config('multica.deployment_fence_state', current_state, true);
    END IF;
    RETURN current_state;
END;
$$;
