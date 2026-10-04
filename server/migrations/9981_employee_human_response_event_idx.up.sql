CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_human_response_event_idx ON employee_human_response(question_id,input_surface,event_id);
