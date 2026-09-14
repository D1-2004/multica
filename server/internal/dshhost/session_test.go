package dshhost

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionBindingSurvivesReplicaRacesAndSeparatesEmployees(t *testing.T) {
	a, b := stores(t)
	ctx := context.Background()
	scope := SessionScope{Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Kind: "chat", ID: uuid.New()}
	task := uuid.New()
	results := make(chan Execution, 24)
	var wg sync.WaitGroup
	for i := range 24 {
		store := a
		if i%2 != 0 {
			store = b
		}
		wg.Go(func() {
			binding, err := store.BindExecution(ctx, scope, task)
			if err != nil {
				t.Error(err)
				return
			}
			results <- binding
		})
	}
	wg.Wait()
	close(results)
	var first Execution
	for result := range results {
		if first.SessionID == "" {
			first = result
		}
		if result != first {
			t.Fatal("replicas returned different native execution identities")
		}
	}
	if first.SessionID == "" {
		t.Fatal("no binding returned")
	}
	next, err := b.BindExecution(ctx, scope, uuid.New())
	if err != nil || next.SessionID != first.SessionID || next.RequestID == first.RequestID || next.Workdir != first.Workdir {
		t.Fatalf("next turn binding did not preserve Session and change request: %v", err)
	}
	other := scope
	other.AgentID = uuid.New()
	separate, err := b.BindExecution(ctx, other, uuid.New())
	if err != nil || separate.SessionID == first.SessionID || separate.Workdir == first.Workdir {
		t.Fatalf("employees shared a native Session: %v", err)
	}
	other = scope
	other.ID = uuid.New()
	if _, err := a.BindExecution(ctx, other, task); err == nil {
		t.Fatal("same task moved to another Session")
	}
	retry, err := b.BindExecution(ctx, scope, task)
	if err != nil || retry != first {
		t.Fatalf("rejected rebind changed original task: %v", err)
	}
}

func TestNativeSessionIdentityValidation(t *testing.T) {
	id := uuid.NewString()
	for _, valid := range []string{id, "session-" + id} {
		if !ValidSessionID(valid) {
			t.Fatal("canonical native Session was rejected")
		}
	}
	for _, invalid := range []string{"", "../" + id, "session-session-" + id, "{" + id + "}", "urn:uuid:" + id, "00000000-0000-0000-0000-000000000000", "session-00000000-0000-0000-0000-000000000000", id + "/other", " " + id} {
		if ValidSessionID(invalid) {
			t.Fatal("noncanonical or unsafe Session was accepted")
		}
	}
	if _, err := (PostgresStore{}).AdoptNativeExecution(context.Background(), SessionScope{}, uuid.New(), id, uuid.New()); err == nil {
		t.Fatal("native admission accepted an absent task transaction")
	}
}

func TestAdoptNativeExecutionPreservesBrowserIdentityAndRollsBack(t *testing.T) {
	a, b := stores(t)
	ctx := context.Background()
	scope := SessionScope{Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Kind: "chat", ID: uuid.New()}
	task, request, session := uuid.New(), uuid.New(), uuid.NewString()
	adopt := func(scope SessionScope, task, request uuid.UUID, session string, commit bool) (Execution, error) {
		tx, err := a.DB.(*pgxpool.Pool).Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		binding, err := (PostgresStore{DB: tx}).AdoptNativeExecution(ctx, scope, task, session, request)
		if err == nil && commit {
			err = tx.Commit(ctx)
		}
		return binding, err
	}
	first, err := adopt(scope, task, request, session, true)
	if err != nil || first.SessionID != session || first.RequestID != request || first.Workdir != MountPath+"/workspaces/"+session {
		t.Fatalf("browser identity was changed: %v", err)
	}
	for range 2 {
		retry, err := adopt(scope, task, request, session, true)
		if err != nil || retry != first {
			t.Fatalf("same admission did not replay its exact identities: %v", err)
		}
	}
	launched, err := b.BindExecution(ctx, scope, task)
	if err != nil || launched != first {
		t.Fatalf("platform launcher did not reuse the browser identity: %v", err)
	}
	for _, conflicting := range []struct {
		task, request uuid.UUID
		session       string
	}{
		{task, uuid.New(), session}, {uuid.New(), request, session}, {task, request, uuid.NewString()},
	} {
		if _, err := adopt(scope, conflicting.task, conflicting.request, conflicting.session, true); err == nil {
			t.Fatal("conflicting browser/task identity was adopted")
		}
	}
	other := scope
	other.ID = uuid.New()
	if _, err := adopt(other, uuid.New(), uuid.New(), session, true); err == nil {
		t.Fatal("same native Session moved to another platform scope")
	}
	rollbackTask, rollbackSession := uuid.New(), uuid.NewString()
	if _, err := adopt(other, rollbackTask, uuid.New(), rollbackSession, false); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := b.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dsh_task_binding WHERE task_id=$1)
 OR EXISTS(SELECT 1 FROM dsh_employee_session WHERE session_id=$2)`, rollbackTask, rollbackSession).Scan(&exists); err != nil || exists {
		t.Fatalf("rolled-back task admission left native identities: %v", err)
	}
	other.AgentID = uuid.New()
	if _, err := adopt(other, uuid.New(), uuid.New(), session, true); err != nil {
		t.Fatalf("independent employee could not use its own Home namespace: %v", err)
	}
}

func TestConcurrentNativeRequestsCannotCreateTwoTaskBindings(t *testing.T) {
	a, b := stores(t)
	ctx := context.Background()
	scope := SessionScope{Key: Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Kind: "chat", ID: uuid.New()}
	request, session := uuid.New(), uuid.NewString()
	results := make(chan error, 2)
	for _, store := range []PostgresStore{a, b} {
		go func() {
			tx, err := store.DB.(*pgxpool.Pool).Begin(ctx)
			if err != nil {
				results <- err
				return
			}
			defer tx.Rollback(ctx)
			_, err = (PostgresStore{DB: tx}).AdoptNativeExecution(ctx, scope, uuid.New(), session, request)
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err
		}()
	}
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("same browser request committed %d task bindings", succeeded)
	}
}
