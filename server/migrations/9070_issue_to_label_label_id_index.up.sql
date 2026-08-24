CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_to_label_label_id_idx ON issue_to_label (label_id, issue_id);
