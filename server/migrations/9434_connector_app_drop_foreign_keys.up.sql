-- An earlier revision of 9432 created foreign keys with ON DELETE CASCADE.
-- Drop them wherever that revision already ran. Fresh installs never add them.
DO $$
DECLARE
    constraint_row record;
BEGIN
    FOR constraint_row IN
        SELECT n.nspname AS schema_name, c.relname AS table_name, con.conname AS constraint_name
        FROM pg_constraint con
        JOIN pg_class c ON c.oid = con.conrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE con.contype = 'f'
          AND n.nspname = 'public'
          AND c.relname IN ('connector_app', 'connector_auth_instance', 'connector_auth_binding')
    LOOP
        EXECUTE format(
            'ALTER TABLE %I.%I DROP CONSTRAINT %I',
            constraint_row.schema_name,
            constraint_row.table_name,
            constraint_row.constraint_name
        );
    END LOOP;
END $$;
