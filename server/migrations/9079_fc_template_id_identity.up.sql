UPDATE agent_runtime
SET metadata = jsonb_set(
    metadata - 'template_build_id' - 'artifact_build_id' - 'component_versions',
    '{template}',
    to_jsonb(metadata->>'template_id'),
    true
)
WHERE runtime_mode = 'cloud'
  AND (
      metadata->>'kind' = 'fc-e2b'
      OR metadata->>'sandbox_backend' = 'aliyun_fc'
  )
  AND COALESCE(metadata->>'template_id', '') <> '';

ALTER TABLE fc_e2b_stable_release
    DROP COLUMN template_build_id,
    DROP COLUMN previous_template_build_id;

ALTER TABLE fc_e2b_stable_channel
    DROP COLUMN current_template_build_id;

ALTER TABLE fc_e2b_stable_release_target
    DROP COLUMN previous_template_build_id;

ALTER TABLE fc_e2b_stable_release_target
    DROP CONSTRAINT IF EXISTS fc_e2b_stable_release_target_provider_check;
