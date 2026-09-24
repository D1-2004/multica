package userdecision

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// A Stream CLI may acknowledge upstream before its stdout callback persists.
// Retain and retry the same event locally across transient database failures;
// the store's event ID makes ambiguous commits safe to retry. Exhaustion still
// reconnects, but upstream replay after process death is not guaranteed.
func persistCardEvent(ctx context.Context, persist func(context.Context) (string, error), backoff time.Duration) (string, error) {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		var outcome string
		outcome, err = persist(attemptCtx)
		cancel()
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			return outcome, err
		}
		if attempt == 4 {
			break
		}
		timer := time.NewTimer(backoff << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", err
}
