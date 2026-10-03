package eventrouter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func fixtureEvent() Event {
	return Event{Version: Version, ID: "event-1", Source: "provider/verified-endpoint", Type: "im.message",
		Category: UserMessage, PayloadSchema: "provider/1", Payload: json.RawMessage(`{"author":"actor-a","text":"hello"}`)}
}

func TestEventVersionAndPayloadBoundary(t *testing.T) {
	for _, category := range []string{UserMessage, Observation, Control, RunCallback, Wake} {
		e := fixtureEvent()
		e.Category = category
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for name, mutate := range map[string]func(*Event){
		"major": func(e *Event) { e.Version = 2 }, "category": func(e *Event) { e.Category = "authorized" },
		"missing_id": func(e *Event) { e.ID = "" }, "missing_source": func(e *Event) { e.Source = "" },
		"malformed": func(e *Event) { e.Payload = []byte(`{`) },
		"oversized": func(e *Event) { e.Payload = []byte(`"` + strings.Repeat("a", MaxPayloadBytes) + `"`) },
	} {
		t.Run(name, func(t *testing.T) {
			e := fixtureEvent()
			mutate(&e)
			if !errors.Is(e.Validate(), ErrInvalidEvent) {
				t.Fatal("accepted invalid event")
			}
		})
	}
}

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required for receipt integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func fixtureHost(t *testing.T, pool *pgxpool.Pool) Host {
	t.Helper()
	ws, _ := util.ParseUUID(uuid.NewString())
	agent, _ := util.ParseUUID(uuid.NewString())
	principal, _ := util.ParseUUID(uuid.NewString())
	if _, err := pool.Exec(context.Background(), `INSERT INTO workspace(id,name,slug) VALUES($1,'Event admission fixture',$2)`, ws, "event-router-"+util.UUIDToString(ws)); err != nil {
		t.Fatal(err)
	}
	h := Host{Owner: scene.Owner{WorkspaceID: ws, AgentID: agent}, PrincipalID: principal, TenantOrgID: "org-a",
		Locator:     scene.DingTalkConversation("org-a", scene.KindGroup, "cid-"+uuid.NewString()),
		Observation: scene.Observation{KindStated: true, ActiveAt: time.Now()}, Route: Unified, ConfigVersion: "sha:v1", Fingerprint: "sha:payload1"}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM scene_event_receipt WHERE workspace_id=$1`, ws)
		_, _ = pool.Exec(context.Background(), `DELETE FROM agent_scene WHERE workspace_id=$1`, ws)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, ws)
	})
	return h
}

func TestAdmissionConcurrentReplayDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	e := fixtureEvent()
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, _, err := Admit(context.Background(), p, e, h)
			if err != nil {
				errs <- err
				return
			}
			ids <- util.UUIDToString(row.ID)
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	id := ""
	for got := range ids {
		if id != "" && got != id {
			t.Fatal("multiple receipts")
		}
		id = got
	}
	h.Route = Legacy
	h.ConfigVersion = "sha:v2"
	h.Locator.ExternalID = "cid-other"
	row, replay, err := Admit(context.Background(), p, e, h)
	if err != nil || !replay || row.Route != Unified || row.ConfigVersion != "sha:v1" || util.UUIDToString(row.ID) != id {
		t.Fatalf("sticky replay: %+v, %v", row, err)
	}
	h.Fingerprint = "sha:different"
	if _, _, err := Admit(context.Background(), p, e, h); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
}

func TestAdmissionUnmappedAndAtomicRollbackDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	e := fixtureEvent()
	h.Locator.Kind = ""
	row, _, err := Admit(context.Background(), p, e, h)
	if err != nil || row.State != Unmapped || row.SceneID.Valid || row.Reason != "unknown_kind" {
		t.Fatalf("unknown kind: %+v, %v", row, err)
	}
	h.Locator.Kind = scene.KindGroup
	replayed, replay, err := Admit(context.Background(), p, e, h)
	if err != nil || !replay || replayed.SceneID.Valid {
		t.Fatalf("implicitly remapped held receipt: %+v, %v", replayed, err)
	}
	h2 := fixtureHost(t, p)
	h2.ConfigVersion = "invalid\x00text"
	e.ID = "insert-failure"
	if _, _, err := Admit(context.Background(), p, e, h2); err == nil {
		t.Fatal("expected SQL failure")
	}
	if _, err := scene.Lookup(context.Background(), db.New(p), h2.Owner, h2.Locator); !errors.Is(err, scene.ErrNotFound) {
		t.Fatalf("scene survived failed receipt: %v", err)
	}
}

func TestAdmissionOwnerTenantKindIsolationDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	e := fixtureEvent()
	row, _, err := Admit(context.Background(), p, e, h)
	if err != nil {
		t.Fatal(err)
	}
	h.TenantOrgID = "org-b"
	if _, _, err := Admit(context.Background(), p, e, h); !errors.Is(err, scene.ErrStaleTenant) {
		t.Fatalf("tenant rebound replay: %v", err)
	}
	h.TenantOrgID = "org-a"
	h.PrincipalID, _ = util.ParseUUID(uuid.NewString())
	if _, _, err := Admit(context.Background(), p, e, h); !errors.Is(err, ErrConflict) {
		t.Fatalf("principal changed: %v", err)
	}
	other := fixtureHost(t, p)
	other.Locator = h.Locator
	row2, _, err := Admit(context.Background(), p, e, other)
	if err != nil || row2.SceneID == row.SceneID || row2.ID == row.ID {
		t.Fatalf("owner leak: %+v, %v", row2, err)
	}
	h.PrincipalID = row.PrincipalID
	h.Locator.Kind = scene.KindDM
	e.ID = "other-kind"
	conflict, _, err := Admit(context.Background(), p, e, h)
	if err != nil || conflict.State != Unmapped || conflict.Reason != "kind_conflict" {
		t.Fatalf("kind conflict: %+v, %v", conflict, err)
	}
	e.ID = "orgless"
	h.TenantOrgID = ""
	h.Locator.TenantOrgID = ""
	missing, _, err := Admit(context.Background(), p, e, h)
	if err != nil || missing.State != Unmapped || missing.Reason != "missing_locator" {
		t.Fatalf("org missing: %+v, %v", missing, err)
	}
}

func waitForAdmissionParentLock(t *testing.T, pool *pgxpool.Pool, pid uint32, finished <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		var query string
		if err := pool.QueryRow(context.Background(), `SELECT cardinality(pg_blocking_pids(pid))>0,query FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&blocked, &query); err != nil {
			t.Fatal(err)
		}
		if blocked {
			if !strings.Contains(query, "FROM workspace") {
				t.Fatalf("locked child before parent: %s", query)
			}
			return
		}
		select {
		case <-finished:
			t.Fatal("event admission bypassed workspace deletion lock")
		case <-deadline.C:
			t.Fatal("event admission never reached parent lock")
		case <-tick.C:
		}
	}
}
func deleteAdmissionFixture(ctx context.Context, tx pgx.Tx, h Host) error {
	for _, table := range []string{"scene_event_receipt", "agent_scene"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1`, h.Owner.WorkspaceID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, h.Owner.WorkspaceID)
	return err
}
func TestAdmissionWaitsForWorkspaceDeletionDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	ctx := context.Background()
	deletion, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deletion.Rollback(ctx) })
	if _, err = deletion.Exec(ctx, `SELECT id FROM workspace WHERE id=$1 FOR UPDATE`, h.Owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	writer, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := writer.Conn().PgConn().PID()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	t.Cleanup(func() { cancel(); <-finished })
	go func() {
		defer close(finished)
		defer writer.Release()
		_, _, err := Admit(writeCtx, writer, fixtureEvent(), h)
		result <- err
	}()
	waitForAdmissionParentLock(t, p, pid, finished)
	if err = deleteAdmissionFixture(ctx, deletion, h); err != nil {
		t.Fatal(err)
	}
	if err = deletion.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted workspace accepted event: %v", err)
	}
	var rows int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE workspace_id=$1`, h.Owner.WorkspaceID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("orphan receipts=%d %v", rows, err)
	}
}
func TestAdmissionOuterTransactionBlocksWorkspaceDeletionDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	ctx := context.Background()
	admission, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Rollback(ctx) })
	if _, _, err = Admit(ctx, admission, fixtureEvent(), h); err != nil {
		t.Fatal(err)
	}
	deleter, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := deleter.Conn().PgConn().PID()
	deleteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	finished := make(chan struct{})
	result := make(chan error, 1)
	t.Cleanup(func() { cancel(); <-finished })
	go func() {
		defer close(finished)
		defer deleter.Release()
		tx, err := deleter.Begin(deleteCtx)
		if err != nil {
			result <- err
			return
		}
		defer tx.Rollback(deleteCtx)
		_, err = tx.Exec(deleteCtx, `SELECT id FROM workspace WHERE id=$1 FOR UPDATE`, h.Owner.WorkspaceID)
		if err == nil {
			err = deleteAdmissionFixture(deleteCtx, tx, h)
		}
		if err == nil {
			err = tx.Commit(deleteCtx)
		}
		result <- err
	}()
	waitForAdmissionParentLock(t, p, pid, finished)
	if err = admission.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = p.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE workspace_id=$1`, h.Owner.WorkspaceID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("receipt escaped teardown=%d %v", rows, err)
	}
}

func TestAdmissionHookFailureRollsBackReceiptAndSceneDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	ctx := context.Background()
	failure := errors.New("consumer checkpoint rejected")
	_, _, err := AdmitWithHook(ctx, p, fixtureEvent(), h, func(ctx context.Context, tx pgx.Tx, receipt db.SceneEventReceipt) error {
		if _, err := tx.Exec(ctx, `UPDATE workspace SET description='consumer checkpoint' WHERE id=$1`, receipt.WorkspaceID); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("hook failure not propagated: %v", err)
	}
	var rows int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE workspace_id=$1`, h.Owner.WorkspaceID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("receipt escaped failed consumer tx: %d %v", rows, err)
	}
	if _, err := scene.Lookup(ctx, db.New(p), h.Owner, h.Locator); !errors.Is(err, scene.ErrNotFound) {
		t.Fatalf("scene escaped failed consumer tx: %v", err)
	}
}
func TestAdmissionHookCommitsOnceAndNeverUpgradesHistoricalReplayDatabase(t *testing.T) {
	p := database(t)
	h := fixtureHost(t, p)
	ctx := context.Background()
	calls := 0
	hook := func(ctx context.Context, tx pgx.Tx, receipt db.SceneEventReceipt) error {
		calls++
		_, err := tx.Exec(ctx, `UPDATE workspace SET description='owner frozen' WHERE id=$1`, receipt.WorkspaceID)
		return err
	}
	if _, _, err := AdmitWithHook(ctx, p, fixtureEvent(), h, hook); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := AdmitWithHook(ctx, p, fixtureEvent(), h, hook); err != nil || !replay || calls != 1 {
		t.Fatalf("hook replay: calls=%d replay=%v err=%v", calls, replay, err)
	}
	old := fixtureHost(t, p)
	if _, _, err := Admit(ctx, p, fixtureEvent(), old); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := AdmitWithHook(ctx, p, fixtureEvent(), old, hook); err != nil || !replay || calls != 1 {
		t.Fatalf("historical fact got a new consumer: calls=%d replay=%v err=%v", calls, replay, err)
	}
}

// An ExistingSceneOnly admission (observations) never registers or touches a
// scene: an unknown locator is unmapped, a registered one is used as is.
func TestExistingSceneOnlyNeverRegistersDatabase(t *testing.T) {
	p := database(t)
	ctx := context.Background()
	h := fixtureHost(t, p)
	h.ExistingSceneOnly = true
	e := fixtureEvent()
	e.Category = Observation
	row, _, err := Admit(ctx, p, e, h)
	if err != nil || row.State != Unmapped || row.SceneID.Valid || row.Reason != UnregisteredScene {
		t.Fatalf("unknown scene: %+v, %v", row, err)
	}
	if _, err := scene.Lookup(ctx, db.New(p), h.Owner, h.Locator); !errors.Is(err, scene.ErrNotFound) {
		t.Fatalf("observation registered a scene: %v", err)
	}
	registered, err := scene.Resolve(ctx, db.New(p), h.Owner, h.Locator, scene.Observation{KindStated: true, Title: "before", ActiveAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	e.ID = "observation-2"
	h.Observation = scene.Observation{KindStated: true, Title: "after", ActiveAt: time.Now()}
	row, _, err = Admit(ctx, p, e, h)
	if err != nil || row.State != Ready || row.SceneID != registered.ID {
		t.Fatalf("registered scene: %+v, %v", row, err)
	}
	after, err := scene.Get(ctx, db.New(p), h.Owner, registered.ID)
	if err != nil || after.Title != "before" || !after.LastActiveAt.Time.Equal(registered.LastActiveAt.Time) {
		t.Fatalf("observation touched the scene: %+v, %v", after, err)
	}
	e.ID = "observation-3"
	h.Locator.Kind = scene.KindDM
	row, _, err = Admit(ctx, p, e, h)
	if err != nil || row.State != Unmapped || row.Reason != "kind_conflict" {
		t.Fatalf("kind conflict: %+v, %v", row, err)
	}
}
