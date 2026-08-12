CREATE INDEX IF NOT EXISTS agent_enterprise_identity_source_reuse_idx
    ON agent_enterprise_identity (
        workspace_id,
        bound_by,
        raw_emp_id,
        buc_agent_id,
        buc_identity_source_runtime_id,
        buc_identity_source_updated_at DESC
    )
    WHERE status = 'active'
      AND buc_identity_source_sandbox_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS agent_enterprise_identity_source_reference_idx
    ON agent_enterprise_identity (
        workspace_id,
        buc_identity_source_runtime_id,
        buc_identity_source_sandbox_id
    )
    WHERE status = 'active';
