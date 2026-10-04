package employeememory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func memoryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EMPLOYEE_MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EMPLOYEE_MEMORY_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "employee_memory_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); admin.Close() })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, "CREATE TABLE workspace (id uuid NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"9500_agent_scene", "9501_agent_scene_id_idx", "9502_agent_scene_locator_idx", "9511_agent_scene_kind_source", "9504_agent_scene_memory", "9505_agent_scene_memory_scene_idx", "9620_employee_memory", "9621_employee_memory_state_scope_idx", "9622_employee_learning_id_idx", "9623_employee_learning_replay_idx", "9624_employee_learning_scope_idx"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return pool
}
func testID() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
func memoryScope(t *testing.T, pool *pgxpool.Pool) Scope {
	t.Helper()
	ctx := context.Background()
	ws, agent := testID(), testID()
	if _, err := pool.Exec(ctx, "INSERT INTO workspace(id) VALUES ($1)", ws); err != nil {
		t.Fatal(err)
	}
	row, err := scene.Resolve(ctx, db.New(pool), scene.Owner{WorkspaceID: ws, AgentID: agent}, scene.DingTalkConversation("org-a", scene.KindDM, "cid"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	return Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: "org-a", Scene: scene.RefOf(row), Kind: ScopeScene}
}
func note(key, text string) LearningRecord {
	return LearningRecord{Type: LearningTypePreference, Key: key, Insight: text, Confidence: 8, Source: LearningSourceInferred}
}
func evidence(id string) TrustedEvidence {
	return TrustedEvidence{SourceID: "event-" + id, EvidenceID: "evidence-" + id, ActorID: "human-1"}
}
func record(t *testing.T, s *Store, scope Scope, r LearningRecord, e TrustedEvidence) LearningRecord {
	t.Helper()
	got, err := s.Record(context.Background(), scope, r, e)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func TestPostgresExactScopeIsolationAndReset(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	a := memoryScope(t, pool)
	ctx := context.Background()
	record(t, s, a, note("weekly", "employee-only alpha"), evidence("a"))
	if _, err := pool.Exec(ctx, "INSERT INTO agent_scene_memory(scene_id,workspace_id,agent_id,memory_text) VALUES ($1,$2,$3,'old-loop secret')", a.Scene.SceneID, a.WorkspaceID, a.AgentID); err != nil {
		t.Fatal(err)
	}
	brief, err := s.Brief(ctx, a, "", 5)
	if err != nil || !strings.Contains(brief, "employee-only alpha") || strings.Contains(brief, "old-loop") {
		t.Fatalf("brief %q: %v", brief, err)
	}
	for _, change := range []func(*Scope){func(x *Scope) { x.WorkspaceID = testID() }, func(x *Scope) { x.AgentID = testID() }, func(x *Scope) { x.TenantOrgID = "org-b" }, func(x *Scope) { x.Scene.SceneID = uuid.NewString() }, func(x *Scope) { x.Kind = ScopePrivate }} {
		bad := a
		change(&bad)
		if got, err := s.Brief(ctx, bad, "", 5); err == nil || got != "" {
			t.Fatalf("scope mismatch leaked: %q %v", got, err)
		}
	}
	p1 := a
	p1.Kind = ScopePrivate
	p1.PrincipalID = "person-1"
	p2 := p1
	p2.PrincipalID = "person-2"
	record(t, s, p1, note("weekly", "private-person-1"), evidence("private"))
	for _, scope := range []Scope{a, p2} {
		got, err := s.Brief(ctx, scope, "private-person-1", 5)
		if err != nil || got != "" {
			t.Fatalf("private leaked %q %v", got, err)
		}
	}
	if err := s.Reset(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Brief(ctx, a, "", 5)
	if err != nil || got != "" {
		t.Fatalf("reset %q %v", got, err)
	}
	got, err = s.Brief(ctx, p1, "", 5)
	if err != nil || !strings.Contains(got, "private-person-1") {
		t.Fatalf("private reset %q %v", got, err)
	}
	var old string
	if err := pool.QueryRow(ctx, "SELECT memory_text FROM agent_scene_memory WHERE scene_id=$1", a.Scene.SceneID).Scan(&old); err != nil || old != "old-loop secret" {
		t.Fatalf("old loop changed: %q %v", old, err)
	}
}
func TestPostgresHostTrustCorrectionAndReplay(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	claimed := note("format", "report weekly")
	claimed.Trusted = true
	claimed.Source = LearningSourceUserStated
	first := record(t, s, scope, claimed, evidence("one"))
	if first.Trusted || first.Source != LearningSourceInferred {
		t.Fatalf("model minted trust: %+v", first)
	}
	corrected := note("format", "report daily")
	corrected.Supersedes = first.ID
	e := evidence("two")
	e.HumanStated = true
	second := record(t, s, scope, corrected, e)
	if !second.Trusted || second.Source != LearningSourceUserStated || second.Supersedes != first.ID {
		t.Fatalf("correction: %+v", second)
	}
	if _, err := s.Record(ctx, scope, note("format", "report monthly"), evidence("untrusted")); !errors.Is(err, ErrUntrustedCorrection) {
		t.Fatalf("untrusted overwrite: %v", err)
	}
	got, err := s.Search(ctx, scope, "", 10)
	if err != nil || len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("search %+v %v", got, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			row, err := s.Record(ctx, scope, corrected, e)
			if err == nil && row.ID != second.ID {
				err = errors.New("replay changed ID")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM employee_learning").Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay count=%d err=%v", count, err)
	}
	other := memoryScope(t, pool)
	cross := note("report", "cross")
	cross.Supersedes = second.ID
	if _, err := s.Record(ctx, other, cross, evidence("cross")); !errors.Is(err, ErrInvalidLearning) {
		t.Fatalf("cross-scope correction: %v", err)
	}
}
func TestPostgresVerifiedDistillationAndWorkspaceDeletionFence(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	run := VerifiedRun{TaskID: uuid.NewString(), ExecutionID: uuid.NewString(), Title: "deploy", ProofKind: "tests", Proof: "5 passed", ActorID: "host", EvidenceID: "verification-1"}
	if _, err := s.Distill(ctx, scope, run); !errors.Is(err, ErrUnverified) {
		t.Fatalf("unverified: %v", err)
	}
	run.Passed = true
	first, err := s.Distill(ctx, scope, run)
	if err != nil || !first.Trusted || first.Source != LearningSourceExecution {
		t.Fatalf("distill %+v %v", first, err)
	}
	again, err := NewStore(pool).Distill(ctx, scope, run)
	if err != nil || again.ID != first.ID {
		t.Fatalf("restart replay %+v %v", again, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT id FROM workspace WHERE id=$1 FOR UPDATE", scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := s.Record(blocked, scope, note("blocked", "must wait"), evidence("blocked")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("workspace lock not respected: %v", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM employee_learning WHERE workspace_id=$1", scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM employee_memory_state WHERE workspace_id=$1", scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM workspace WHERE id=$1", scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Record(ctx, scope, note("orphan", "reject after delete"), evidence("orphan")); err == nil {
		t.Fatal("write after workspace deletion")
	}
}

func TestPostgresResetDoesNotResurrectLateReplay(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	r, e := note("preference", "weekly reports"), evidence("original")
	first := record(t, s, scope, r, e)
	if err := s.Reset(ctx, scope); err != nil {
		t.Fatal(err)
	}
	replay := record(t, NewStore(pool), scope, r, e)
	if replay.ID != first.ID {
		t.Fatalf("replay ID = %s", replay.ID)
	}
	if got, err := s.Brief(ctx, scope, "", 5); err != nil || got != "" {
		t.Fatalf("forgotten replay visible: %q %v", got, err)
	}
	record(t, s, scope, note("preference", "daily reports"), evidence("new"))
	if got, err := s.Brief(ctx, scope, "", 5); err != nil || !strings.Contains(got, "daily reports") || strings.Contains(got, "weekly reports") {
		t.Fatalf("new evidence after reset: %q %v", got, err)
	}
}

func TestPostgresConfidenceDecayUsesHostTrust(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	inferred := record(t, s, scope, note("inferred", "tentative preference"), evidence("inferred"))
	e := evidence("human")
	e.HumanStated = true
	human := record(t, s, scope, note("human", "confirmed preference"), e)
	old := time.Now().UTC().Add(-91 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := pool.Exec(ctx, `UPDATE employee_learning SET record=jsonb_set(record,'{created_at}',to_jsonb($1::text)) WHERE id=$2 OR id=$3`, old, inferred.ID, human.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Search(ctx, scope, "", 5)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if rows[0].ID != human.ID || rows[0].EffectiveConfidence != 8 || rows[1].EffectiveConfidence != 5 {
		t.Fatalf("confidence/trust order %+v", rows)
	}
}

func TestPostgresDistillKeepsDistinctChineseTasks(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	for _, title := range []string{"日报格式", "审批流程"} {
		run := VerifiedRun{TaskID: uuid.NewString(), ExecutionID: uuid.NewString(), Title: title, ProofKind: "tests", Proof: "checks passed", ActorID: "host", EvidenceID: uuid.NewString(), Passed: true}
		if _, err := s.Distill(ctx, scope, run); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.Search(ctx, scope, "", 5)
	if err != nil || len(rows) != 2 {
		t.Fatalf("distinct verified tasks were lost: %+v %v", rows, err)
	}
	if rows[0].Key == rows[1].Key || rows[0].Supersedes != "" || rows[1].Supersedes != "" {
		t.Fatalf("unrelated tasks superseded each other: %+v", rows)
	}
}

func TestPostgresResetReplayCannotChangeModelKeyOrType(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	e := evidence("stable-host-event")
	first := record(t, s, scope, note("original-key", "forgotten fact"), e)
	if err := s.Reset(ctx, scope); err != nil {
		t.Fatal(err)
	}
	changed := note("model-renamed-key", "forgotten fact repackaged")
	changed.Type = LearningTypeOperational
	replay := record(t, NewStore(pool), scope, changed, e)
	if replay.ID != first.ID {
		t.Errorf("same Host evidence got a new receipt: %s != %s", replay.ID, first.ID)
	}
	if got, err := s.Brief(ctx, scope, "", 5); err != nil || got != "" {
		t.Fatalf("model key/type resurrected reset evidence: %q %v", got, err)
	}
}

func TestPostgresVerifiedTaskBindingComesOnlyFromHost(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	model := note("verified-task", "a verified outcome")
	model.TaskID = "model-task"
	model.ExecutionID = "model-execution"
	absent := evidence("missing-host-task")
	absent.VerifiedExecution = true
	if _, err := s.Record(ctx, scope, model, absent); !errors.Is(err, ErrInvalidLearning) {
		t.Errorf("Host verified boolean accepted model task binding: %v", err)
	}
	host := evidence("host-task")
	host.VerifiedExecution = true
	host.TaskID = "host-task"
	host.ExecutionID = "host-execution"
	got := record(t, s, scope, model, host)
	if got.TaskID != host.TaskID || got.ExecutionID != host.ExecutionID || !got.Trusted {
		t.Fatalf("model task IDs survived Host verification: %+v", got)
	}
	model.Key = "different-model-key"
	model.TaskID = "another-task"
	model.ExecutionID = "another-execution"
	replay := record(t, NewStore(pool), scope, model, host)
	if replay.ID != got.ID {
		t.Fatalf("model execution changed verified replay identity: %+v", replay)
	}
}
