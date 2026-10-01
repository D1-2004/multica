DELETE FROM context_capability_binding WHERE scope_type = 'org';
ALTER TABLE context_capability_binding
    DROP CONSTRAINT IF EXISTS context_capability_binding_scope_type_check,
    ADD CONSTRAINT context_capability_binding_scope_type_check CHECK (scope_type IN ('offer', 'scene', 'person'));
