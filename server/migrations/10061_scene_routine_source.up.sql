-- Only allowlisted, bounded provenance/material is stored; not execution context.
ALTER TABLE context_scope_routine ADD COLUMN IF NOT EXISTS source jsonb;
ALTER TABLE context_scope_routine DROP CONSTRAINT IF EXISTS context_scope_routine_source_check;
ALTER TABLE context_scope_routine ADD CONSTRAINT context_scope_routine_source_check
 CHECK (source IS NULL OR COALESCE((jsonb_typeof(source) = 'object'
   AND source->>'schema' = 'scene.routine.source/1'
   AND source->>'workspace_id' = workspace_id::text
   AND source->>'agent_id' = agent_id::text
   AND source->>'scene_id' = scene_id::text
   AND source->>'tenant_org_id' = tenant_org_id
   AND octet_length(source::text) <= 262144), false));
