package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/redis/go-redis/v9"
)

// Use real PG and Redis together: a committed execution must survive the gap
// before notify, even when another process has cached an empty runtime.
func TestDirectTaskCommitBeforeNotifyRecovery(t *testing.T) {
	for _, recovery := range []string{"source_replay", "cache_eviction", "cache_expiry", "redis_unavailable"} {
		t.Run(recovery, func(t *testing.T) {
			rdb := newRedisTestClient(t)
			f := directDatabase(t)
			ctx := context.Background()
			agent, err := f.service.Queries.GetAgent(ctx, util.MustParseUUID(f.request.Task.Scope.AgentID))
			if err != nil {
				t.Fatal(err)
			}
			runtimeKey := util.UUIDToString(agent.RuntimeID)
			auth := TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{agent.RuntimeID}}
			f.service.EmptyClaim = NewEmptyClaimCache(rdb)
			if got, err := f.service.ClaimTaskForRuntime(ctx, agent.RuntimeID, auth); err != nil || got != nil || !f.service.EmptyClaim.IsEmpty(ctx, runtimeKey) {
				t.Fatal("precondition: empty runtime was not cached", got, err)
			}

			prepared, err := f.service.PrepareDirectTask(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			accepted, err := f.service.enqueuePreparedDirectTaskTx(ctx, tx, prepared)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// Deliberately omit NotifyDirectTaskResult, as if the process died
			// immediately after commit. A fresh pool/service has no local state.
			pool, err := pgxpool.NewWithConfig(ctx, f.pool.Config().Copy())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			restarted := &TaskService{Queries: db.New(pool), TxStarter: pool, Bus: events.New(), EmptyClaim: NewEmptyClaimCache(rdb)}
			if got, err := restarted.ClaimTaskForRuntime(ctx, agent.RuntimeID, auth); err != nil || got != nil {
				t.Fatal("missed invalidation should retain the bounded empty verdict", got, err)
			}
			queued, err := restarted.Queries.GetAgentTask(ctx, accepted.Task.ID)
			if err != nil || queued.Status != "queued" {
				t.Fatal("cache hit lost or mutated committed work", queued.Status, err)
			}

			switch recovery {
			case "source_replay":
				for range 2 {
					replay, err := restarted.EnqueueDirectTask(ctx, f.request)
					if err != nil || replay.Created || replay.Task.ID != accepted.Task.ID || replay.Run.ID != accepted.Run.ID {
						t.Fatal("replay failed to recover the same execution", replay, err)
					}
				}
				if restarted.EmptyClaim.IsEmpty(ctx, runtimeKey) {
					t.Fatal("source replay did not invalidate the stale cache")
				}
			case "cache_eviction":
				if err := rdb.Del(ctx, emptyClaimKey(runtimeKey), emptyClaimVersion(runtimeKey)).Err(); err != nil {
					t.Fatal(err)
				}
			case "cache_expiry":
				ttl, err := rdb.PTTL(ctx, emptyClaimKey(runtimeKey)).Result()
				if err != nil || ttl <= 0 || ttl > 3*time.Minute {
					t.Fatal("missed notification has no three-minute cache bound", ttl, err)
				}
				// Advance only this Redis deadline, rather than sleeping for three
				// minutes. This checks expiry recovery, not wall-clock latency.
				if err := rdb.PExpireAt(ctx, emptyClaimKey(runtimeKey), time.Unix(1, 0)).Err(); err != nil {
					t.Fatal(err)
				}
			case "redis_unavailable":
				unavailable := redis.NewClient(rdb.Options())
				if err := unavailable.Close(); err != nil {
					t.Fatal(err)
				}
				restarted.EmptyClaim = NewEmptyClaimCache(unavailable)
			}

			// Separate PG transactions race to claim the durable row. Redis can
			// only change lookup cost; it cannot grant a second execution.
			claims := make(chan *db.AgentTaskQueue, 2)
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					got, err := restarted.ClaimTaskForRuntime(ctx, agent.RuntimeID, auth)
					claims <- got
					errs <- err
				})
			}
			wg.Wait()
			close(claims)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			claimed := 0
			for got := range claims {
				if got == nil {
					continue
				}
				claimed++
				if got.ID != accepted.Task.ID || got.IssueID.Valid || got.AutopilotRunID.Valid || got.ChatSessionID.Valid {
					t.Fatal("recovery replaced the execution or introduced a facade", got.ID)
				}
			}
			if claimed != 1 {
				t.Fatalf("want exactly one claim, got %d", claimed)
			}
			task, err := employeetask.NewStore(pool).Get(ctx, f.request.Task.Scope, f.request.Task.ID)
			if err != nil || task.ActiveRunID != accepted.Run.ID || task.IssueID != "" {
				t.Fatal("recovery changed the Task aggregate", task, err)
			}
			var runs, queues, starts int
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid),
				(SELECT count(*) FROM agent_task_queue WHERE agent_id=$2::uuid),
				(SELECT count(*) FROM employee_task_entry WHERE task_id=$1::uuid AND kind='run_started')`, task.ID, task.Scope.AgentID).Scan(&runs, &queues, &starts); err != nil || runs != 1 || queues != 1 || starts != 1 {
				t.Fatal("recovery duplicated the run, queue or ledger entry", runs, queues, starts, err)
			}
		})
	}
}
