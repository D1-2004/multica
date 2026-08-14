DROP TRIGGER IF EXISTS trg_agent_task_cancelled_completion ON agent_task_queue;
DROP FUNCTION IF EXISTS enqueue_cancelled_task_completion();
DROP TABLE IF EXISTS task_completion_outbox;
DROP TABLE IF EXISTS agent_dispatch_acceptance;
