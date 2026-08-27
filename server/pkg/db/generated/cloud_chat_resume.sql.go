package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const getAgentChatSessionResume = `-- name: GetAgentChatSessionResume :one
SELECT chat_session_resume FROM agent WHERE id = $1
`

func (q *Queries) GetAgentChatSessionResume(ctx context.Context, id pgtype.UUID) (bool, error) {
	row := q.db.QueryRow(ctx, getAgentChatSessionResume, id)
	var chatSessionResume bool
	err := row.Scan(&chatSessionResume)
	return chatSessionResume, err
}

const updateAgentChatSessionResume = `-- name: UpdateAgentChatSessionResume :exec
UPDATE agent SET chat_session_resume = $2, updated_at = now() WHERE id = $1
`

func (q *Queries) UpdateAgentChatSessionResume(ctx context.Context, id pgtype.UUID, chatSessionResume bool) error {
	_, err := q.db.Exec(ctx, updateAgentChatSessionResume, id, chatSessionResume)
	return err
}

const listAgentChatSessionResumeByIDs = `-- name: ListAgentChatSessionResumeByIDs :many
SELECT id, chat_session_resume FROM agent WHERE id = ANY($1::uuid[])
`

type ListAgentChatSessionResumeByIDsRow struct {
	ID                pgtype.UUID `json:"id"`
	ChatSessionResume bool        `json:"chat_session_resume"`
}

func (q *Queries) ListAgentChatSessionResumeByIDs(ctx context.Context, ids []pgtype.UUID) ([]ListAgentChatSessionResumeByIDsRow, error) {
	rows, err := q.db.Query(ctx, listAgentChatSessionResumeByIDs, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ListAgentChatSessionResumeByIDsRow{}
	for rows.Next() {
		var i ListAgentChatSessionResumeByIDsRow
		if err := rows.Scan(&i.ID, &i.ChatSessionResume); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const getChatSessionResumeIdentity = `-- name: GetChatSessionResumeIdentity :one
SELECT resume_identity FROM chat_session WHERE id = $1
`

func (q *Queries) GetChatSessionResumeIdentity(ctx context.Context, id pgtype.UUID) (string, error) {
	row := q.db.QueryRow(ctx, getChatSessionResumeIdentity, id)
	var resumeIdentity string
	err := row.Scan(&resumeIdentity)
	return resumeIdentity, err
}

const setChatSessionResumeIdentity = `-- name: SetChatSessionResumeIdentity :exec
UPDATE chat_session
SET resume_identity = $1,
    updated_at = now()
WHERE id = $2
`

type SetChatSessionResumeIdentityParams struct {
	ResumeIdentity string      `json:"resume_identity"`
	ID             pgtype.UUID `json:"id"`
}

func (q *Queries) SetChatSessionResumeIdentity(ctx context.Context, arg SetChatSessionResumeIdentityParams) error {
	_, err := q.db.Exec(ctx, setChatSessionResumeIdentity, arg.ResumeIdentity, arg.ID)
	return err
}

const getLastCompletedChatTaskAt = `-- name: GetLastCompletedChatTaskAt :one
SELECT completed_at
FROM agent_task_queue
WHERE chat_session_id = $1
  AND status = 'completed'
  AND completed_at IS NOT NULL
ORDER BY completed_at DESC
LIMIT 1
`

func (q *Queries) GetLastCompletedChatTaskAt(ctx context.Context, chatSessionID pgtype.UUID) (pgtype.Timestamptz, error) {
	row := q.db.QueryRow(ctx, getLastCompletedChatTaskAt, chatSessionID)
	var completedAt pgtype.Timestamptz
	err := row.Scan(&completedAt)
	return completedAt, err
}
