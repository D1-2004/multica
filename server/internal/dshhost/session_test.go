package dshhost

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
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
