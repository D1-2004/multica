ALTER TABLE employee_message_resource
    DROP CONSTRAINT IF EXISTS employee_message_resource_state_check,
    ADD CONSTRAINT employee_message_resource_state_check CHECK (state IN ('available', 'partial', 'unavailable', 'unsupported')) NOT VALID;
