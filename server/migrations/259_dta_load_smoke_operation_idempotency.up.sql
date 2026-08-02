CREATE UNIQUE INDEX issue_dta_load_smoke_operation_unique
ON issue (
    workspace_id,
    (metadata->>'token_id'),
    (metadata->>'agent_id'),
    (metadata->>'marker')
)
WHERE metadata->>'kind' = 'dta_load_smoke'
  AND metadata->>'schema' = 'dta-multica-load-smoke@2';
