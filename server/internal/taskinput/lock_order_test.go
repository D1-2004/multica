package taskinput

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

// Two Host transactions each create a collection for the same Task and then,
// in the same transaction, write the Task's lifecycle (which locks the Task
// FOR UPDATE). If taskinput held only a share lock, both would hold SHARE and
// both would try to upgrade: a deadlock that aborts one of them.
func TestCreateCollectionCombinedWithTaskWriteDoesNotDeadlock(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := f.newTask(t, "Collect from two groups")

	lifecycleWrite := func(tx employeetask.DB, key string) error {
		tasks := employeetask.NewStore(tx)
		current, err := tasks.Get(ctx, f.taskScope(), task.ID)
		if err != nil {
			return err
		}
		_, _, err = tasks.AppendInput(ctx, f.taskScope(), task.ID, employeetask.InputParams{
			Source: employeetask.Source{Namespace: "host.wait", Key: key}, ActorRef: requesterRef, Body: "waiting on " + key, ExpectedVersion: current.Version})
		return err
	}
	createIn := func(tx employeetask.DB, key string) error {
		p := f.createParams(task, spec(f.dmC, participantC, "Number for "+key+"?"))
		p.Source.Key = "create/" + key
		_, _, err := NewStore(tx).CreateCollectionTx(ctx, f.scope, p)
		return err
	}

	txA, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback(ctx)
	if err := createIn(txA, "a"); err != nil {
		t.Fatal(err)
	}

	pidB := make(chan int, 1)
	doneB := make(chan error, 1)
	go func() {
		txB, err := f.pool.Begin(ctx)
		if err != nil {
			pidB <- 0
			doneB <- err
			return
		}
		defer txB.Rollback(ctx)
		var pid int
		if err := txB.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			pidB <- 0
			doneB <- err
			return
		}
		pidB <- pid
		if err := createIn(txB, "b"); err != nil {
			doneB <- err
			return
		}
		if err := lifecycleWrite(txB, "b"); err != nil {
			doneB <- err
			return
		}
		doneB <- txB.Commit(ctx)
	}()
	pid := <-pidB
	if pid == 0 {
		t.Fatal(<-doneB)
	}
	// B is now blocked on the Task row, either inside its create or inside its
	// lifecycle write; then A writes the lifecycle and commits.
	deadline := time.Now().Add(5 * time.Second)
	for f.count(t, `SELECT count(*) FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock'`, pid) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("transaction B never waited on the Task row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	errA := lifecycleWrite(txA, "a")
	if errA == nil {
		errA = txA.Commit(ctx)
	}
	errB := <-doneB
	if errA != nil || errB != nil {
		t.Fatalf("combined create + lifecycle write: A=%v B=%v", errA, errB)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_collection WHERE task_id=$1::uuid`, task.ID); n != 2 {
		t.Fatalf("collections: %d", n)
	}
	waits, err := f.store.TaskWaits(ctx, f.scope, task.ID)
	if err != nil || len(waits) != 2 {
		t.Fatalf("waits: %+v %v", waits, err)
	}
}
