package userdecision

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

func (s *Service) runObservations(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		err := s.projectObservations(batchCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("user decision trace will retry", "event", "user_decision_trace_retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Only projection workers share this lock; business updates never wait for
// the network. One exporter per environment prevents stale replicas emitting
// an older version after a newer one. The watermark uses the observed version,
// so a simultaneous state change remains pending. Crash retries upsert one ID.
func (s *Service) projectObservations(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "user-decision-trace:"+s.Store.Environment).Scan(&locked); err != nil || !locked {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT to_jsonb(d) FROM coordinator_user_decision d WHERE environment=$1 AND (trace_exported_at IS NULL OR trace_exported_at<updated_at) AND (trace_next_attempt_at IS NULL OR trace_next_attempt_at<=now()) ORDER BY updated_at LIMIT 20`, s.Store.Environment)
	if err != nil {
		return err
	}
	var requests []Request
	for rows.Next() {
		var raw []byte
		var r Request
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err != nil {
			rows.Close()
			return err
		}
		requests = append(requests, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Reserve time to commit successful versions even if a later export
	// times out. Individual failures back off without occupying every batch.
	networkCtx, cancelNetwork := context.WithTimeout(ctx, 15*time.Second)
	defer cancelNetwork()
	var exported, failed []Request
	var exportErr error
	for _, r := range requests {
		if networkCtx.Err() != nil {
			break
		}
		attemptCtx, cancel := context.WithTimeout(networkCtx, 5*time.Second)
		r.Snapshot, err = s.Store.Snapshot(attemptCtx, r)
		if err == nil {
			err = s.Observe(attemptCtx, r)
		}
		cancel()
		if err != nil {
			exportErr = err
			failed = append(failed, r)
			continue
		}
		exported = append(exported, r)
	}
	// The transaction only locks domain rows after network work. A separate,
	// bounded commit context preserves progress after cancellation of the last
	// export; business updated_at is never changed by projection bookkeeping.
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelCommit()
	for _, r := range exported {
		if _, err = tx.Exec(commitCtx, `UPDATE coordinator_user_decision SET trace_exported_at=$2,trace_next_attempt_at=NULL WHERE id=$1`, r.ID, r.UpdatedAt); err != nil {
			return err
		}
	}
	for _, r := range failed {
		if _, err = tx.Exec(commitCtx, `UPDATE coordinator_user_decision SET trace_next_attempt_at=now()+interval '1 minute' WHERE id=$1 AND updated_at=$2`, r.ID, r.UpdatedAt); err != nil {
			return err
		}
	}
	if err = tx.Commit(commitCtx); err != nil {
		return err
	}
	return exportErr
}
