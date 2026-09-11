package handler

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Authored text is independent from the unique workspace label used for tagging
// and usage. Old OKRs without this field continue to read their label text.
func agentOKRText(row db.ListAgentOKRsRow) string {
	if row.AuthoredText.Valid { return row.AuthoredText.String }
	prefix := agentOKRObjectivePrefix
	if row.Kind == "key_result" { prefix = agentOKRKeyResultPrefix }
	return strings.TrimPrefix(row.LabelName, prefix)
}

func upsertPortableOKRLabel(ctx context.Context, q *db.Queries, agent db.Agent, kind, text string, previous []db.ListAgentOKRsRow, reusable []pgtype.UUID, isolate bool) (db.IssueLabel, error) {
	text = strings.TrimSpace(text)
	prefix, color := agentOKRObjectivePrefix, agentOKRObjectiveColor
	if kind == "key_result" { prefix, color = agentOKRKeyResultPrefix, agentOKRKeyResultColor }
	for _, row := range previous {
		if row.Kind != kind || !strings.EqualFold(agentOKRText(row), text) { continue }
		for _, id := range reusable {
			if id != row.LabelID { continue }
			return q.UpsertAgentOKRLabel(ctx, db.UpsertAgentOKRLabelParams{WorkspaceID:agent.WorkspaceID, Name:row.LabelName, Description:agentOKRLabelDescription, Color:color, ReusableLabelIds:reusable})
		}
	}
	name := prefix + text
	if isolate { name += " · " + agent.Name }
	label, err := q.UpsertAgentOKRLabel(ctx, db.UpsertAgentOKRLabelParams{WorkspaceID:agent.WorkspaceID, Name:name, Description:agentOKRLabelDescription, Color:color, ReusableLabelIds:reusable})
	// A regular tag or orphaned historical tag can occupy the readable name.
	// Never take it over; allocate a distinct tag while retaining authored text.
	if isolate && errors.Is(err, pgx.ErrNoRows) {
		return q.UpsertAgentOKRLabel(ctx, db.UpsertAgentOKRLabelParams{WorkspaceID:agent.WorkspaceID, Name:prefix + text + " [" + uuid.NewString() + "]", Description:agentOKRLabelDescription, Color:color, ReusableLabelIds:reusable})
	}
	return label, err
}
