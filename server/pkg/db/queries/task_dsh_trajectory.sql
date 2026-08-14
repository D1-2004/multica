-- name: PutAgentTaskDSHTrajectory :one
INSERT INTO agent_task_dsh_trajectory (
    task_id,
    session_id,
    storage_key,
    encryption_scheme,
    encryption_key,
    sha256,
    size_bytes,
    stored_size_bytes,
    event_count,
    format_version
) VALUES (
    @task_id,
    @session_id,
    @storage_key,
    @encryption_scheme,
    @encryption_key,
    @sha256,
    @size_bytes,
    @stored_size_bytes,
    @event_count,
    @format_version
)
ON CONFLICT (task_id) DO UPDATE
SET updated_at = now()
WHERE agent_task_dsh_trajectory.session_id = EXCLUDED.session_id
  AND agent_task_dsh_trajectory.sha256 = EXCLUDED.sha256
RETURNING *;

-- name: GetAgentTaskDSHTrajectory :one
SELECT *
FROM agent_task_dsh_trajectory
WHERE task_id = @task_id;

-- name: ListAgentTaskDSHTrajectories :many
SELECT *
FROM agent_task_dsh_trajectory
WHERE task_id = ANY(@task_ids::uuid[]);
