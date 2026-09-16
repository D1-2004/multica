-- name: ListGitConnections :many
SELECT * FROM git_connection WHERE workspace_id = $1 AND provider = 'github' ORDER BY created_at, id;

-- name: GetGitConnection :one
SELECT * FROM git_connection WHERE id = $1 AND workspace_id = $2 AND provider = 'github';
