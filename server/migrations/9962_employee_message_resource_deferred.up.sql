-- A resource left to a configured background executor (an image for a
-- verified vision executor) is frozen as 'deferred'. Widening is safe for
-- older binaries, which never write it. Installed NOT VALID (no table scan);
-- 9963 validates it.
ALTER TABLE employee_message_resource
    DROP CONSTRAINT IF EXISTS employee_message_resource_state_check,
    ADD CONSTRAINT employee_message_resource_state_check CHECK (state IN ('available', 'partial', 'unavailable', 'unsupported', 'deferred')) NOT VALID;
