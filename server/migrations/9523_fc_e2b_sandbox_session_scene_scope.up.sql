-- Scene buckets reuse one FC/E2B sandbox across tasks that share a scene and
-- trigger. scope_id stays a UUID (a hash of scene + actor); this only widens
-- the scope_type check. Idempotent: every check whose definition mentions
-- scope_type is dropped, then the widened check is installed again.
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
    CHECK (scope_type IN ('chat', 'issue', 'scene'));
