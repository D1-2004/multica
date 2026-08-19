package main

import (
	"context"
	"log/slog"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const runnerArtifactCleanupInterval = 5 * time.Minute

func runRunnerArtifactCleanup(ctx context.Context, queries *db.Queries) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := queries.DeleteExpiredRunnerArtifacts(cleanupCtx); err != nil && ctx.Err() == nil {
			slog.Warn("runner artifact cleanup failed", "error", err)
		}
	}

	cleanup()
	ticker := time.NewTicker(runnerArtifactCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}
