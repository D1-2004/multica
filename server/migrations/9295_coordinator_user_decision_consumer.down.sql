DROP TABLE IF EXISTS coordinator_user_decision_consumer;
ALTER TABLE coordinator_user_decision DROP COLUMN IF EXISTS interpretation;
ALTER TABLE coordinator_user_decision DROP COLUMN IF EXISTS execution_result;
ALTER TABLE coordinator_user_decision DROP COLUMN IF EXISTS review_label;
