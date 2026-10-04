ALTER TABLE agent ADD COLUMN IF NOT EXISTS coordination_mode text NOT NULL DEFAULT 'coordinator'
    CONSTRAINT agent_coordination_mode_check CHECK (coordination_mode IN ('coordinator', 'employee'));
