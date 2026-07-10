package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TouchPATLastUsed refreshes a PAT's last_used_at off the request path.
//
// The context is bounded: the old fire-and-forget goroutines carried
// context.Background(), so when the update convoyed on the hot PAT row
// (all daemon traffic shares one token), each straggler camped on a pool
// connection with no way to expire — under enough concurrency that alone
// exhausts the pool. Five seconds is generous for a single-row update and
// guarantees the goroutine releases its connection either way. The SQL
// side carries the matching defense (60s staleness guard, see
// UpdatePersonalAccessTokenLastUsed in personal_access_token.sql).
func TouchPATLastUsed(queries *db.Queries, patID pgtype.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queries.UpdatePersonalAccessTokenLastUsed(ctx, patID); err != nil {
			slog.Debug("pat last_used refresh failed", "error", err)
		}
	}()
}
