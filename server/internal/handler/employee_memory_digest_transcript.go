package handler

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
)

// EmployeeSceneTranscript reads M8's persisted group transcript (withdrawn
// rows excluded, exact tenant-qualified scene) for the digest writer.
type EmployeeSceneTranscript struct{}

func employeeDigestTranscript(rows []employeeentry.SceneMessage) []digest.TranscriptMessage {
	out := make([]digest.TranscriptMessage, 0, len(rows))
	for _, m := range rows {
		out = append(out, digest.TranscriptMessage{ProviderMessageID: m.ProviderMessageID, SentAt: m.SentAt, SenderClass: m.SenderClass, SenderRef: m.SenderRef, SenderName: m.SenderName, QuotedMessageID: m.QuotedMessageID, Body: m.Body, Truncated: m.Truncated})
	}
	return out
}

func (EmployeeSceneTranscript) After(ctx context.Context, tx pgx.Tx, key digest.SceneKey, afterAt time.Time, afterID string, limit int) ([]digest.TranscriptMessage, error) {
	rows, err := employeeentry.SceneMessagesAfter(ctx, tx, employeeentry.SceneMessageKey(key), afterAt, afterID, limit)
	return employeeDigestTranscript(rows), err
}

func (EmployeeSceneTranscript) Before(ctx context.Context, tx pgx.Tx, key digest.SceneKey, beforeAt time.Time, beforeID string, limit int) ([]digest.TranscriptMessage, error) {
	rows, err := employeeentry.SceneMessagesBefore(ctx, tx, employeeentry.SceneMessageKey(key), beforeAt, beforeID, limit)
	return employeeDigestTranscript(rows), err
}
