-- Enterprise-level (org) bindings: scope_type 'org' with scope_key = org_id.
-- Widening only; every existing row satisfies the new check.
ALTER TABLE context_capability_binding
    DROP CONSTRAINT IF EXISTS context_capability_binding_scope_type_check,
    ADD CONSTRAINT context_capability_binding_scope_type_check CHECK (scope_type IN ('offer', 'org', 'scene', 'person'));
