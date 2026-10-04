CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_human_card_projection_due_idx ON employee_human_card_projection (available_at, question_id) WHERE state='pending';
