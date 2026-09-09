package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const fcE2BInstanceLockClass int32 = 0x46433249

// RunSandboxLifecycle uses Redis for scheduling and existing PostgreSQL task,
// startup-attempt and sandbox-session rows. No Redis means no background work.
func (l *FCE2BLauncher) RunSandboxLifecycle(ctx context.Context, namespace string) {
	if l == nil || l.LifecycleRedis == nil || l.Pool == nil {
		return
	}
	ticker := time.NewTicker(5*time.Second)
	defer ticker.Stop()
	for {
		configured := l.withCurrentConfig()
		if configured.Config.Enabled && configured.Config.SandboxRenewalEnabled {
			key := namespace + "|" + configured.Config.ServerURL + "|" + configured.Config.APIURL
			coordinator := newFCE2BCheckCoordinator(l.LifecycleRedis, key, 90*time.Second, 30*time.Second)
			if err := runFCE2BCheckRound(ctx, coordinator, configured.checkSandboxExecutions); err != nil && ctx.Err() == nil {
				slog.Warn("FC sandbox lifecycle round failed", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type fcE2BCheckLease interface {
	claim(context.Context) (string, bool, error)
	renew(context.Context, string) error
	finish(context.Context, string) error
}

func runFCE2BCheckRound(ctx context.Context, lease fcE2BCheckLease, check func(context.Context) error) error {
	// The complete round deadline is shorter than the original lease, including
	// a bounded Redis claim. This also bounds takeover during process pauses.
	roundCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	token, acquired, err := lease.claim(roundCtx)
	if err != nil || !acquired {
		return err
	}
	renewDone := make(chan struct{})
	renewCtx, stopRenew := context.WithCancel(roundCtx)
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(20*time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if err := lease.renew(renewCtx, token); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = check(roundCtx)
	stopRenew()
	<-renewDone
	if roundCtx.Err() != nil {
		// Never release an uncertain lease while an external call may be in
		// flight. Expiry provides takeover; the next owner reads platform state.
		return roundCtx.Err()
	}
	if finishErr := lease.finish(roundCtx, token); finishErr != nil {
		return finishErr
	}
	return err
}

func (l *FCE2BLauncher) checkSandboxExecutions(ctx context.Context) error {
	rows, err := l.Queries.ListFCE2BSandboxExecutionChecks(ctx)
	if err != nil {
		return errors.New("load FC sandbox executions failed")
	}
	// Bounded parallelism avoids one slow provider call blocking every sandbox.
	slots := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for _, row := range rows {
		select {
		case <-ctx.Done():
			workers.Wait()
			return ctx.Err()
		case slots <- struct{}{}:
		}
		workers.Add(1)
		go func(execution db.AgentTaskRuntimeStartAttempt) {
			defer workers.Done()
			defer func() { <-slots }()
			if err := l.checkSandboxExecution(ctx, execution); err != nil && ctx.Err() == nil {
				slog.Warn("FC sandbox check failed", "sandbox_id", execution.SandboxID, "task_id", execution.TaskID, "error", err)
			}
		}(row)
	}
	workers.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func (l *FCE2BLauncher) renewalWindow() (time.Duration, time.Duration) {
	target := time.Duration(l.Config.TimeoutSeconds)*time.Second
	return min(10*time.Minute, target/3), target
}

func (l *FCE2BLauncher) checkSandboxExecution(ctx context.Context, execution db.AgentTaskRuntimeStartAttempt) error {
	missing := false
	guard := db.LockFCE2BSandboxExecutionTaskParams{
		TaskID: execution.TaskID, RuntimeID: execution.RuntimeID,
		AttemptID: execution.ID, SandboxID: execution.SandboxID,
	}
	err := l.withSandboxInstanceLock(ctx, execution.SandboxID, nil, func(q *db.Queries) error {
		// Revalidate after taking the instance lock: the task may have finished
		// or acquired a different startup attempt since the batch was loaded.
		if _, err := q.LockFCE2BSandboxExecutionTask(ctx, guard); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return errors.New("verify FC sandbox execution failed")
		}
		client := newFCE2BSandboxClient(l.Config)
		info, err := client.get(ctx, execution.SandboxID)
		if errors.Is(err, errFCE2BSandboxNotFound) {
			// A second independent lookup distinguishes an isolated 404 response
			// from a confirmed missing instance. Transport/auth errors stay unknown.
			if _, err = client.get(ctx, execution.SandboxID); !errors.Is(err, errFCE2BSandboxNotFound) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := q.MarkFCE2BSandboxSessionMissing(ctx, db.MarkFCE2BSandboxSessionMissingParams{
				SandboxID: execution.SandboxID, RuntimeID: execution.RuntimeID,
			}); err != nil {
				return errors.New("mark FC sandbox missing failed")
			}
			missing = true
			return nil
		}
		if err != nil {
			return err
		}
		// A task can finish while the provider lookup is in flight.
		if _, err := q.LockFCE2BSandboxExecutionTask(ctx, guard); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return errors.New("recheck FC sandbox execution failed")
		}
		if info.State == "running" {
			minimum, target := l.renewalWindow()
			oldEnd := info.EndAt
			info, err = client.extend(ctx, info, minimum, target)
			if err != nil {
				return err
			}
			if info.EndAt.After(oldEnd) {
				slog.Info("FC sandbox renewed", "sandbox_id", execution.SandboxID, "task_id", execution.TaskID,
					"attempt_id", execution.ID, "old_expires_at", oldEnd, "expires_at", info.EndAt)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return q.UpdateFCE2BSandboxSessionExpiry(ctx, db.UpdateFCE2BSandboxSessionExpiryParams{
			RuntimeID: execution.RuntimeID, SandboxID: execution.SandboxID,
			ExpiresAt: pgtype.Timestamptz{Time: info.EndAt, Valid: true},
		})
	})
	// Release the instance connection before entering the task transaction.
	// Its task/attempt guard fences replacements without exhausting the pool.
	if err != nil || !missing {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if l.Tasks == nil {
		return errors.New("FC sandbox task service unavailable")
	}
	_, err = l.Tasks.failTask(ctx, execution.TaskID, "FC sandbox no longer exists; task execution was interrupted", "", "", "sandbox_expired", false, "",
		failTaskOptions{fcE2BExecution: &execution})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (l *FCE2BLauncher) withSandboxInstanceLock(ctx context.Context, sandboxID string, existing *pgxpool.Conn, operation func(*db.Queries) error) error {
	if l.Pool == nil {
		return errors.New("FC sandbox lifecycle requires PostgreSQL coordination")
	}
	conn := existing
	if conn == nil {
		var err error
		conn, err = l.Pool.Acquire(ctx)
		if err != nil {
			return errors.New("acquire FC sandbox lifecycle connection failed")
		}
		defer conn.Release()
	}
	digest := sha256.Sum256([]byte(l.Config.APIURL + "|" + sandboxID))
	key := int32(binary.BigEndian.Uint32(digest[:4]))
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", fcE2BInstanceLockClass, key); err != nil {
		return errors.New("acquire FC sandbox instance lock failed")
	}
	defer releaseFCE2BAdvisoryLock(conn, false, fcE2BInstanceLockClass, key, "sandbox lifecycle")
	return operation(db.New(conn))
}

func (l *FCE2BLauncher) ensureSandboxLifetime(ctx context.Context, runtimeID pgtype.UUID, sandboxID string, existing *pgxpool.Conn) error {
	if !l.Config.SandboxRenewalEnabled {
		return nil
	}
	return l.withSandboxInstanceLock(ctx, sandboxID, existing, func(q *db.Queries) error {
		minimum, target := l.renewalWindow()
		info, err := newFCE2BSandboxClient(l.Config).ensureTTL(ctx, sandboxID, minimum, target)
		if err != nil {
			return err
		}
		return q.UpdateFCE2BSandboxSessionExpiry(ctx, db.UpdateFCE2BSandboxSessionExpiryParams{
			RuntimeID: runtimeID, SandboxID: sandboxID,
			ExpiresAt: pgtype.Timestamptz{Time: info.EndAt, Valid: true},
		})
	})
}

// SetLifecycleRedis shares the server's existing coordination connection pool.
func (l *FCE2BLauncher) SetLifecycleRedis(client *redis.Client) { l.LifecycleRedis = client }
