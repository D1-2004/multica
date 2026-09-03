ALTER TABLE agent
DROP COLUMN IF EXISTS scene_memory_write_enabled,
DROP COLUMN IF EXISTS scene_memory_recall_enabled,
DROP COLUMN IF EXISTS scene_memory_ui_enabled,
DROP COLUMN IF EXISTS scene_memory_bootstrap_enabled;
