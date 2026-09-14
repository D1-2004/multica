package dshprofile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestBuildRetryRejectsInvalidIdentityBeforeAccess(t *testing.T) {
	if err := (Store{}).RetryBuild(context.Background(), dshhost.Key{}, "template", nil, 0, uuid.Nil); !errors.Is(err, ErrChanged) {
		t.Fatal("invalid identity admitted", err)
	}
}

func TestBuildRetryPostgresPreservesAttemptAndFencesConcurrentReplay(t *testing.T) {
	a, b := profilePools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	for _, statement := range []string{`INSERT INTO workspace(id) VALUES($1)`, `INSERT INTO agent(workspace_id,id) VALUES($1,$2)`} {
		args := []any{key.WorkspaceID}
		if strings.Contains(statement, "agent") {
			args = append(args, key.AgentID)
		}
		if _, err := a.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	source := fixtureSource()
	read := func(context.Context, *db.Queries, dshhost.Key, string) (Source, error) { return source, nil }
	store := Store{DB: a}
	revision, err := store.Prepare(ctx, key, source.TemplateID, read)
	if err != nil {
		t.Fatal(err)
	}
	var oldID uuid.UUID
	if err := a.QueryRow(ctx, `SELECT id FROM dsh_plugin_build WHERE workspace_id=$1`, key.WorkspaceID).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	intent := uuid.New()
	if _, err := a.Exec(ctx, `UPDATE dsh_plugin_build SET state='failed',worker_phase='cleanup',create_intent=$2,sandbox_id='old-sandbox',worker_error='source_download_http_403' WHERE workspace_id=$1`, key.WorkspaceID, intent); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryBuild(ctx, key, source.TemplateID, read, revision.ID, oldID); !errors.Is(err, ErrChanged) {
		t.Fatal("unconfirmed cleanup admitted", err)
	}
	status, err := store.Status(ctx, key)
	if err != nil || len(status.Builds) != 1 || status.Builds[0].CanRetry {
		t.Fatal("cleanup was offered for retry", err)
	}
	if _, err := a.Exec(ctx, `UPDATE dsh_plugin_build SET worker_phase='done' WHERE workspace_id=$1`, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	status, err = store.Status(ctx, key)
	if err != nil || !status.Builds[0].CanRetry || status.Builds[0].ID != oldID.String() {
		t.Fatal("cleaned attempt missing retry identity", err)
	}
	if err := store.RetryBuild(ctx, key, source.TemplateID, read, revision.ID+1, oldID); !errors.Is(err, ErrChanged) {
		t.Fatal("stale revision admitted", err)
	}
	changed := source
	changed.TemplateID = "changed-template"
	changedRead := func(context.Context, *db.Queries, dshhost.Key, string) (Source, error) { return changed, nil }
	if err := store.RetryBuild(ctx, key, source.TemplateID, changedRead, revision.ID, oldID); !errors.Is(err, ErrChanged) {
		t.Fatal("changed configuration admitted", err)
	}
	if err := store.RetryBuild(ctx, key, source.TemplateID, read, revision.ID, uuid.New()); !errors.Is(err, ErrChanged) {
		t.Fatal("unrelated build admitted", err)
	}
	stores := []Store{{DB: a}, {DB: b}}
	results := make(chan error, 8)
	var group sync.WaitGroup
	for i := range 8 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			results <- stores[i%2].RetryBuild(ctx, key, source.TemplateID, read, revision.ID, oldID)
		}(i)
	}
	group.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrChanged) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("concurrent requests retried more than once", success)
	}
	var id uuid.UUID
	var phase, state, history string
	var historyCount int
	if err := a.QueryRow(ctx, `SELECT id,state,worker_phase,attempt_history::text,jsonb_array_length(attempt_history) FROM dsh_plugin_build WHERE workspace_id=$1`, key.WorkspaceID).Scan(&id, &state, &phase, &history, &historyCount); err != nil {
		t.Fatal(err)
	}
	if id == oldID || state != "queued" || phase != "queued" || historyCount != 1 || !strings.Contains(history, oldID.String()) || !strings.Contains(history, intent.String()) || !strings.Contains(history, "old-sandbox") {
		t.Fatal("retry lost provenance or kept old identity")
	}
	if strings.Contains(history, "synthetic-private-value") {
		t.Fatal("employee configuration entered build attempt history")
	}
	// A delayed request for attempt A must not retry a new failed attempt B.
	if _, err := a.Exec(ctx, `UPDATE dsh_plugin_build SET state='failed',worker_phase='done' WHERE workspace_id=$1`, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryBuild(ctx, key, source.TemplateID, read, revision.ID, oldID); !errors.Is(err, ErrChanged) {
		t.Fatal("replayed token retried a later failure", err)
	}
}
