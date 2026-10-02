package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
)

type fixture struct {
	pool      *pgxpool.Pool
	store     *Store
	admission Admission
}

func database(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required for employee entry PostgreSQL tests")
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	name := "employee_entry_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{name}.Sanitize()+` CASCADE`)
		if err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "migrations")
	files := []string{"9500_agent_scene.up.sql", "9501_agent_scene_id_idx.up.sql", "9502_agent_scene_locator_idx.up.sql", "9511_agent_scene_kind_source.up.sql", "9513_scene_event_receipt.up.sql", "9514_scene_event_receipt_id_idx.up.sql", "9515_scene_event_receipt_source_idx.up.sql"}
	owned, err := filepath.Glob(filepath.Join(dir, "964*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(owned)
	for _, name := range files {
		apply(t, pool, filepath.Join(dir, name))
	}
	for _, path := range owned {
		apply(t, pool, path)
	}
	if _, err = pool.Exec(ctx, `CREATE TABLE workspace(id uuid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ws, agent, principal := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO workspace VALUES($1)`, ws); err != nil {
		t.Fatal(err)
	}
	wsID, _ := util.ParseUUID(ws)
	agentID, _ := util.ParseUUID(agent)
	principalID, _ := util.ParseUUID(principal)
	ev := eventrouter.Event{Version: 1, ID: "message-a", Source: "test-router", Type: "channel.message.created", Category: eventrouter.UserMessage, OccurredAt: time.Now(), PayloadSchema: "test", Payload: json.RawMessage(`{"text":"work"}`)}
	receipt, _, err := eventrouter.Admit(ctx, pool, ev, eventrouter.Host{Owner: scene.Owner{WorkspaceID: wsID, AgentID: agentID}, PrincipalID: principalID, TenantOrgID: "org-a", Locator: scene.DingTalkConversation("org-a", scene.KindGroup, "cid-test"), Observation: scene.Observation{KindStated: true}, Route: eventrouter.Unified, ConfigVersion: "one", Fingerprint: "payload-a"})
	if err != nil {
		t.Fatal(err)
	}
	a := Admission{Scope: Scope{ws, agent, "org-a", util.UUIDToString(receipt.SceneID)}, Item: Item{ReceiptID: util.UUIDToString(receipt.ID), PrincipalID: principal, Payload: json.RawMessage(`{"trusted_command":"work","actor":"alice"}`), MessageCount: 1}, Owner: Employee, ConfigRevision: "one"}
	return fixture{pool, NewStore(pool), a}
}
func apply(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(raw)); err != nil {
		t.Fatalf("migration %s: %v", path, err)
	}
}
func admit(t *testing.T, f fixture) Consumption {
	t.Helper()
	c, err := f.store.Admit(context.Background(), f.admission)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func claim(t *testing.T, f fixture) Job {
	t.Helper()
	j, err := f.store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func secondReceipt(t *testing.T, f fixture) Admission {
	t.Helper()
	a := f.admission
	a.Item.ReceiptID = uuid.NewString()
	a.Item.Payload = json.RawMessage(`{"trusted_command":"another","actor":"bob"}`)
	_, err := f.pool.Exec(context.Background(), `INSERT INTO scene_event_receipt SELECT $1,workspace_id,agent_id,principal_id,tenant_org_id,source,source_event_id||'-next',fingerprint,envelope,scene_id,route,state,reason,config_version,created_at FROM scene_event_receipt WHERE id=$2`, a.Item.ReceiptID, f.admission.Item.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestEntryOwnerFrozenAndConcurrentReplay(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan Consumption, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { c, err := f.store.Admit(ctx, f.admission); results <- c; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for c := range results {
		if id != "" && id != c.JobID {
			t.Fatal("duplicate jobs")
		}
		id = c.JobID
		if c.Owner != Employee {
			t.Fatal(c)
		}
	}
	changed := f.admission
	changed.Owner = Coordinator
	changed.ConfigRevision = "two"
	c, err := f.store.Admit(ctx, changed)
	if err != nil || c.Owner != Employee || c.JobID != id {
		t.Fatalf("owner remapped on retry: %+v %v", c, err)
	}
	changed.Item.Payload = json.RawMessage(`{"different":true}`)
	if _, err = f.store.Admit(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting payload accepted: %v", err)
	}
}
func TestEntryCoordinatorDoesNotCreateEmployeeJob(t *testing.T) {
	f := database(t)
	f.admission.Owner = Coordinator
	c := admit(t, f)
	if c.Owner != Coordinator || c.JobID != "" {
		t.Fatal(c)
	}
	f.admission.Owner = Employee
	c = admit(t, f)
	if c.Owner != Coordinator || c.JobID != "" {
		t.Fatal("changed owner on replay")
	}
	if _, err := f.store.Claim(context.Background()); !errors.Is(err, ErrNoJob) {
		t.Fatal(err)
	}
}
func TestEntryWindowSealsAtClaimAndRetainsMessageIdentity(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	first := admit(t, f)
	next := secondReceipt(t, f)
	c, err := f.store.Admit(ctx, next)
	if err != nil || c.JobID != first.JobID {
		t.Fatalf("pending window not merged: %+v %v", c, err)
	}
	job := claim(t, f)
	if len(job.Items) != 2 || string(job.Items[0].Payload) == string(job.Items[1].Payload) {
		t.Fatalf("message identities lost: %+v", job)
	}
	next.Item.ReceiptID = uuid.NewString()
	_, err = f.pool.Exec(ctx, `INSERT INTO scene_event_receipt SELECT $1,workspace_id,agent_id,principal_id,tenant_org_id,source,source_event_id||'-later',fingerprint,envelope,scene_id,route,state,reason,config_version,created_at FROM scene_event_receipt WHERE id=$2`, next.Item.ReceiptID, f.admission.Item.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	c, err = f.store.Admit(ctx, next)
	if err != nil || c.JobID == job.ID {
		t.Fatalf("claimed window reopened: %+v %v", c, err)
	}
	if _, err = f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("same scene has two active jobs: %v", err)
	}
}
func TestEntryLeaseStealFencesWritesAndOutcomeRecovery(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	admit(t, f)
	old := claim(t, f)
	outcome := json.RawMessage(`{"kind":"reply","reply":"ready"}`)
	if err := f.store.SaveOutcome(ctx, old, outcome); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET lease_until=now()-interval '1 second' WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	current := claim(t, f)
	if current.Generation <= old.Generation || string(current.Outcome) == "" {
		t.Fatal("lost generation/outcome")
	}
	if err := f.store.SaveOutcome(ctx, old, json.RawMessage(`{"kind":"quiet"}`)); !errors.Is(err, ErrLease) {
		t.Fatalf("stale lease wrote: %v", err)
	}
	if err := f.store.Complete(ctx, current, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("completed job reclaimed: %v", err)
	}
}
func TestEntryModelJournalBudgetSurvivesReclaim(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	admit(t, f)
	job := claim(t, f)
	req := json.RawMessage(`{"messages":["first"]}`)
	resp := json.RawMessage(`{"tool_call_id":"call-original"}`)
	if cached, err := f.store.BeginModel(ctx, job, 0, req); err != nil || len(cached) != 0 {
		t.Fatalf("first call: %s %v", cached, err)
	}
	if err := f.store.SaveModel(ctx, job, 0, resp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	job = claim(t, f)
	cached, err := f.store.BeginModel(ctx, job, 0, req)
	if err != nil || !strings.Contains(string(cached), "call-original") {
		t.Fatalf("replay lost original completion: %s %v", cached, err)
	}
	for range 2 {
		if _, err = f.store.BeginModel(ctx, job, 1, json.RawMessage(`{"next":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.store.BeginModel(ctx, job, 1, json.RawMessage(`{"next":true}`)); !errors.Is(err, ErrModelBudget) {
		t.Fatalf("fourth network attempt allowed: %v", err)
	}
	if _, err = f.store.BeginModel(ctx, job, 0, json.RawMessage(`{"different":true}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay request accepted: %v", err)
	}
}
func TestEntryScopeAndMissingWorkspaceRejected(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	bad := f.admission
	bad.Scope.TenantOrgID = "other"
	if _, err := f.store.Admit(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant accepted: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, f.admission.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Admit(ctx, f.admission); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan admission: %v", err)
	}
}

func TestEntryToolJournalReplaysCommittedResult(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	admit(t, f)
	job := claim(t, f)
	calls := 0
	execute := func(pgx.Tx) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"receipt":"run-one","text":"accepted"}`), nil
	}
	input := json.RawMessage(`{"name":"dispatch_task","goal":"work"}`)
	first, err := f.store.ExecuteTool(ctx, job, "native-one", input, nil, execute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	job = claim(t, f)
	second, err := f.store.ExecuteTool(ctx, job, "native-one", input, nil, execute)
	var same bool
	if compareErr := f.pool.QueryRow(ctx, `SELECT $1::jsonb=$2::jsonb`, first, second).Scan(&same); compareErr != nil {
		t.Fatal(compareErr)
	}
	if err != nil || !same || calls != 1 {
		t.Fatalf("tool replay: %s %s calls=%d %v", first, second, calls, err)
	}
	if _, err = f.store.ExecuteTool(ctx, job, "native-one", json.RawMessage(`{"goal":"other"}`), nil, execute); !errors.Is(err, ErrConflict) {
		t.Fatalf("same call changed effect: %v", err)
	}
}

func TestEntryUnsupportedSourceIsDurablyHeldByOriginalOwner(t *testing.T) {
	f := database(t)
	f.admission.HoldReason = "unsupported_source"
	consumption := admit(t, f)
	if consumption.State != "held" || consumption.Reason != "unsupported_source" || consumption.JobID != "" {
		t.Fatalf("unsupported event executed: %+v", consumption)
	}
	f.admission.Owner = Coordinator
	f.admission.HoldReason = ""
	retry := admit(t, f)
	if retry.Owner != Employee || retry.State != "held" || retry.Reason != "unsupported_source" {
		t.Fatalf("held owner changed on replay: %+v", retry)
	}
	if _, err := f.store.Claim(context.Background()); !errors.Is(err, ErrNoJob) {
		t.Fatalf("held input created job: %v", err)
	}
}

func TestEntryHeldJobStopsWithoutDiscardingAcceptedFacts(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	admit(t, f)
	job := claim(t, f)
	if err := f.store.SaveOutcome(ctx, job, json.RawMessage(`{"receipt":"already-accepted"}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Hold(ctx, job, "agent_archived"); err != nil {
		t.Fatal(err)
	}
	consumption, err := f.store.Lookup(ctx, job.Scope, job.Items[0].ReceiptID)
	if err != nil || consumption.State != "held" || consumption.Reason != "agent_archived" {
		t.Fatalf("hold: %+v %v", consumption, err)
	}
	if _, err = f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("held job reclaimed: %v", err)
	}
	var outcome json.RawMessage
	if err = f.pool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1`, job.ID).Scan(&outcome); err != nil || !strings.Contains(string(outcome), "already-accepted") {
		t.Fatalf("accepted facts lost: %s %v", outcome, err)
	}
}

func TestEntryUnmappedHeldReceiptDoesNotInventScene(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	a := f.admission
	a.Scope.SceneID = ""
	a.HoldReason = "unknown_kind"
	if _, err := f.pool.Exec(ctx, `UPDATE scene_event_receipt SET scene_id=NULL,state='unmapped',reason='unknown_kind' WHERE id=$1`, a.Item.ReceiptID); err != nil {
		t.Fatal(err)
	}
	consumption, err := f.store.Admit(ctx, a)
	if err != nil || consumption.State != "held" || consumption.JobID != "" {
		t.Fatalf("unmapped receipt not held: %+v %v", consumption, err)
	}
	a.Owner = Coordinator
	a.HoldReason = ""
	prior, err := f.store.Admit(ctx, a)
	if err != nil || prior.Owner != Employee || prior.Reason != "unknown_kind" {
		t.Fatalf("lost unmapped owner: %+v %v", prior, err)
	}
	var synthetic int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_event_consumption WHERE receipt_id=$1 AND scene_id IS NOT NULL`, a.Item.ReceiptID).Scan(&synthetic); err != nil || synthetic != 0 {
		t.Fatalf("invented scene: %d %v", synthetic, err)
	}
	if _, err = f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("unmapped started a job: %v", err)
	}
}

func TestEntryUnmappedConcurrentReplayIsOneHeldConsumption(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	a := f.admission
	a.Scope.SceneID = ""
	a.HoldReason = "unknown_kind"
	if _, err := f.pool.Exec(ctx, `UPDATE scene_event_receipt SET scene_id=NULL,state='unmapped',reason='unknown_kind' WHERE id=$1`, a.Item.ReceiptID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			<-start
			c, err := f.store.Admit(ctx, a)
			if err == nil && (c.State != "held" || c.JobID != "") {
				err = errors.New("unmapped receipt created work")
			}
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_event_consumption WHERE receipt_id=$1`, a.Item.ReceiptID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate held consumption: %d %v", count, err)
	}
}

func TestEntryModelFailureReplaysWithoutConsumingNewAttempt(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	admit(t, f)
	job := claim(t, f)
	request := json.RawMessage(`{"first":true}`)
	if _, err := f.store.BeginModel(ctx, job, 0, request); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveModelFailure(ctx, job, 0, "provider temporary failure"); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.BeginModel(ctx, job, 0, request)
	var failure *ModelFailure
	if !errors.As(err, &failure) || failure.Message != "provider temporary failure" {
		t.Fatalf("failure replay: %v", err)
	}
	var attempts int
	if err := f.pool.QueryRow(ctx, `SELECT model_attempts FROM employee_scene_job WHERE id=$1`, job.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("error replay counted a new network call: %d %v", attempts, err)
	}
}
