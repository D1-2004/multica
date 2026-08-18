DO $$
DECLARE
    target RECORD;
BEGIN
    FOR target IN
        SELECT namespace.nspname AS schema_name, class.relname AS table_name
        FROM pg_class AS class
        JOIN pg_namespace AS namespace ON namespace.oid = class.relnamespace
        WHERE namespace.nspname = 'public'
          AND class.relkind IN ('r', 'p')
    LOOP
        EXECUTE format(
            'DROP TRIGGER IF EXISTS trg_deployment_fence_10_admission ON %I.%I',
            target.schema_name,
            target.table_name
        );
        EXECUTE format(
            'DROP TRIGGER IF EXISTS trg_deployment_fence_00_global ON %I.%I',
            target.schema_name,
            target.table_name
        );
    END LOOP;
END;
$$;

DROP FUNCTION IF EXISTS multica_install_deployment_fence_triggers();
DROP FUNCTION IF EXISTS multica_enforce_deployment_admission_fence();
DROP FUNCTION IF EXISTS multica_enforce_deployment_fence();
DROP FUNCTION IF EXISTS multica_current_deployment_fence_state();
DROP TABLE IF EXISTS deployment_fence_replica_ack;
DROP TABLE IF EXISTS deployment_fence;
