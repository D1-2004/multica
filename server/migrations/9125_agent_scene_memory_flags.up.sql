-- Per-agent Scene Memory switches. Default off; only bound digital-employee
-- inbound uses them even when enabled.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS scene_memory_write_enabled BOOLEAN NOT NULL DEFAULT false,
ADD COLUMN IF NOT EXISTS scene_memory_recall_enabled BOOLEAN NOT NULL DEFAULT false,
ADD COLUMN IF NOT EXISTS scene_memory_ui_enabled BOOLEAN NOT NULL DEFAULT false,
ADD COLUMN IF NOT EXISTS scene_memory_bootstrap_enabled BOOLEAN NOT NULL DEFAULT false;
