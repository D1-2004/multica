ALTER TABLE dsh_schedule_occurrence ADD COLUMN IF NOT EXISTS batch_ordinal integer NOT NULL DEFAULT 0 CHECK (batch_ordinal >= 0);
