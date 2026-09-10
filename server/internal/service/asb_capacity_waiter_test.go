package service

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestASBCapacityWaitCoordinatorBoundsAndRotates(t *testing.T) {
	coordinator := newASBCapacityWaitCoordinator()
	for index := range asbCapacityWaitMaxConcurrent {
		if !coordinator.tryStart(fmt.Sprintf("tenant-%d", index)) {
			t.Fatalf("tenant %d was rejected below the global limit", index)
		}
	}
	if coordinator.tryStart("tenant-over-limit") {
		t.Fatal("coordinator exceeded the global ASB capacity-launch limit")
	}
	coordinator.finish("tenant-0")
	if !coordinator.tryStart("tenant-over-limit") {
		t.Fatal("coordinator did not release a global launch slot")
	}
	if first, second := coordinator.nextStart(5), coordinator.nextStart(5); first != 0 || second != 1 {
		t.Fatalf("round-robin starts = (%d, %d), want (0, 1)", first, second)
	}
}

type mapASBCredentialStore map[pgtype.UUID]db.AsbRuntimeCredential

func (store mapASBCredentialStore) GetASBRuntimeCredential(
	_ context.Context,
	runtimeID pgtype.UUID,
) (db.AsbRuntimeCredential, error) {
	credential, ok := store[runtimeID]
	if !ok {
		return db.AsbRuntimeCredential{}, pgx.ErrNoRows
	}
	return credential, nil
}

func (store mapASBCredentialStore) ListASBRuntimeCredentials(
	context.Context,
) ([]db.AsbRuntimeCredential, error) {
	credentials := make([]db.AsbRuntimeCredential, 0, len(store))
	for _, credential := range store {
		credentials = append(credentials, credential)
	}
	return credentials, nil
}

func TestASBCapacityWaiterSchedulesOneTaskPerTenant(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	tasks := NewTaskService(queries, pool, nil, events.New())

	seedWait := func() db.AgentTaskQueue {
		t.Helper()
		taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
		taskUUID := util.MustParseUUID(taskID)
		if _, err := pool.Exec(ctx, `
			UPDATE agent_task_queue
			SET status = 'queued', dispatched_at = NULL
			WHERE id = $1
		`, taskUUID); err != nil {
			t.Fatalf("reset capacity waiter: %v", err)
		}
		task, err := queries.GetAgentTask(ctx, taskUUID)
		if err != nil {
			t.Fatalf("load capacity waiter: %v", err)
		}
		attempt, err := tasks.BeginRuntimeStartAttempt(
			ctx,
			task,
			SandboxBackendASB,
			RuntimeStartProtocolHTTPJSONV1,
		)
		if err != nil {
			t.Fatalf("begin capacity waiter attempt: %v", err)
		}
		if waiting, err := tasks.MarkRuntimeStartCapacityWaiting(ctx, attempt); err != nil || !waiting {
			t.Fatalf("mark capacity waiter: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE agent_task_runtime_start_attempt
			SET finished_at = now() - interval '2 minutes',
			    updated_at = now() - interval '2 minutes'
			WHERE id = $1
		`, attempt.ID); err != nil {
			t.Fatalf("age capacity waiter: %v", err)
		}
		return task
	}

	first := seedWait()
	second := seedWait()
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue SET priority = 4 WHERE id = $1
	`, second.ID); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{0x71}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(testASBAPIKey))
	if err != nil {
		t.Fatal(err)
	}
	store := mapASBCredentialStore{
		first.RuntimeID: {
			RuntimeID:       first.RuntimeID,
			ApiKeyEncrypted: sealed,
			ApiKeyHint:      "-key",
		},
		second.RuntimeID: {
			RuntimeID:       second.RuntimeID,
			ApiKeyEncrypted: sealed,
			ApiKeyHint:      "-key",
		},
	}
	launcherCalls := make(chan runtimeLaunchCall, 2)
	releaseLaunch := make(chan struct{})
	tasks.RuntimeLauncher = &blockingRuntimeLauncher{calls: launcherCalls, release: releaseLaunch}
	tasks.runtimeLaunchLeases = newPostgresTaskRuntimeLaunchLeaseStore(queries)
	waiter := &ASBLauncher{
		Queries: queries,
		Tasks:   tasks,
		Credentials: &ASBRuntimeClientProvider{
			Store:   store,
			Secrets: box,
		},
	}

	scheduled, err := waiter.retryCapacityWaitingTasks(ctx)
	if err != nil {
		t.Fatalf("retry capacity waiters: %v", err)
	}
	if scheduled != 1 {
		t.Fatalf("scheduled capacity waiters = %d, want one per tenant", scheduled)
	}
	select {
	case call := <-launcherCalls:
		if call.task.ID != first.ID {
			t.Fatalf("scheduled task %s, want oldest task despite another Runtime's higher priority", util.UUIDToString(call.task.ID))
		}
	case <-time.After(time.Second):
		t.Fatal("capacity waiter did not invoke Runtime launcher")
	}
	scheduled, err = waiter.retryCapacityWaitingTasks(ctx)
	if err != nil {
		t.Fatalf("retry capacity waiters while tenant launch is in flight: %v", err)
	}
	if scheduled != 0 {
		t.Fatalf("scheduled %d additional waiter(s) while tenant launch was in flight", scheduled)
	}
	select {
	case call := <-launcherCalls:
		t.Fatalf("capacity waiter scheduled a second task for the same tenant: %s", util.UUIDToString(call.task.ID))
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseLaunch)
	select {
	case <-waiter.CapacityWait.wakeups:
	case <-time.After(time.Second):
		t.Fatal("completed capacity launch did not wake the next waiting task")
	}
}

func TestASBCapacityWaiterSelectsOldestTaskWithinRuntime(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	tasks := NewTaskService(queries, pool, nil, events.New())
	var first db.AgentTaskQueue
	for index := 0; index < 2; index++ {
		taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
		task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = task
		}
		if _, err := pool.Exec(ctx, `
			UPDATE agent_task_queue SET status = 'queued', dispatched_at = NULL,
			    runtime_id = $2, priority = $3,
			    created_at = now() - make_interval(secs => $4::double precision)
			WHERE id = $1
		`, task.ID, first.RuntimeID, index*4, 120-index*60); err != nil {
			t.Fatal(err)
		}
		task.RuntimeID = first.RuntimeID
		attempt, err := tasks.BeginRuntimeStartAttempt(ctx, task, SandboxBackendASB, RuntimeStartProtocolHTTPJSONV1)
		if err != nil {
			t.Fatal(err)
		}
		if waiting, err := tasks.MarkRuntimeStartCapacityWaiting(ctx, attempt); err != nil || !waiting {
			t.Fatalf("mark capacity wait: waiting=%v err=%v", waiting, err)
		}
	}
	waiting, err := queries.ListASBCapacityWaitingTasks(ctx, db.ListASBCapacityWaitingTasksParams{RetrySeconds: 0, StaleSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range waiting {
		if task.RuntimeID == first.RuntimeID {
			if task.ID != first.ID {
				t.Fatalf("selected task %s instead of the older low-priority task", util.UUIDToString(task.ID))
			}
			return
		}
	}
	t.Fatal("Runtime has no capacity waiter")
}
