ALTER TABLE task_completion_outbox
    ADD COLUMN execution_summary JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN task_completion_outbox.execution_summary IS
    'Immutable Agent task observability summary sent with the terminal Router callback';
