package service

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestASBChatSandboxWithinReclaimGrace(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	chat := db.FcE2bSandboxSession{
		ScopeType:  fcE2BScopeTypeChat,
		LastUsedAt: pgtype.Timestamptz{Time: now.Add(-5 * time.Minute), Valid: true},
	}
	if !asbChatSandboxWithinReclaimGrace(chat, now) {
		t.Fatal("chat sandbox used 5 minutes ago must be inside the 20-minute grace")
	}

	expired := chat
	expired.LastUsedAt = pgtype.Timestamptz{Time: now.Add(-21 * time.Minute), Valid: true}
	if asbChatSandboxWithinReclaimGrace(expired, now) {
		t.Fatal("chat sandbox used 21 minutes ago must be reclaimable")
	}

	issue := chat
	issue.ScopeType = fcE2BScopeTypeIssue
	if asbChatSandboxWithinReclaimGrace(issue, now) {
		t.Fatal("issue sandboxes must not receive the chat reclaim grace")
	}
}
