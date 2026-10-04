DELETE FROM fc_e2b_sandbox_session WHERE scope_type = 'scene';

DO $$
DECLARE
    constraint_name text;
BEGIN
    FOR constraint_name IN
        SELECT c.conname
        FROM pg_constraint c
        JOIN pg_class t ON t.oid = c.conrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE n.nspname = current_schema()
          AND t.relname = 'fc_e2b_sandbox_session'
          AND c.contype = 'c'
          AND pg_get_constraintdef(c.oid) ILIKE '%scope_type%'
    LOOP
        EXECUTE format('ALTER TABLE fc_e2b_sandbox_session DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END $$;

ALTER TABLE fc_e2b_sandbox_session
    ADD CONSTRAINT fc_e2b_sandbox_session_scope_type_check
    CHECK (scope_type IN ('chat', 'issue'));
