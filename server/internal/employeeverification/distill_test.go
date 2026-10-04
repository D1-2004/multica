package employeeverification

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

// verifiedRun creates a Task whose Run passes its human-derived checks and
// leaves a pending distill intent.
func verifiedRun(t *testing.T, f *fixture, namespace, requester string) (employeetask.Task, employeetask.Run) {
	t.Helper()
	task := f.task(t, namespace, requester, "整理本周人员状态表并导出 report.csv")
	if namespace == humanTaskSource {
		f.spec(t, task, OriginHumanCue, requester, reportChecks(t))
	}
	run := f.run(t, task, employeetask.StateSucceeded, "done")
	f.artifact(t, task.ID, run, "report.csv", []byte(reportCSV))
	return task, run
}

func mustVerify(t *testing.T, f *fixture, task employeetask.Task, run employeetask.Run) RunResult {
	t.Helper()
	result, err := f.verifier().VerifyRun(context.Background(), f.scope, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Intent {
		t.Fatalf("no distill intent: %+v", result.Gate)
	}
	return result
}

func TestVerifiedDistillCommitRecoveryIsExactlyOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, run := verifiedRun(t, f, humanTaskSource, alice)
	mustVerify(t, f, task, run)

	// Crash between the memory write and the commit: nothing survives, the
	// intent stays pending.
	crash := errors.New("crash before commit")
	opts := DistillOptions{Memory: f.memory, afterDistill: func() error { return crash }}
	if _, err := f.store.ProcessVerifiedDistill(ctx, 10, opts); !errors.Is(err, crash) {
		t.Fatalf("crash not surfaced: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_learning`); n != 0 {
		t.Fatalf("rolled-back distill left %d learnings", n)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_task_verified_distill WHERE state='pending'`); n != 1 {
		t.Fatalf("intent lost after crash: pending=%d", n)
	}

	// A restarted process and concurrent replicas consume it exactly once.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var captured []DistillOutcome
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			outcomes, err := NewStore(f.pool).ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: employeememory.NewStore(f.pool)})
			mu.Lock()
			captured = append(captured, outcomes...)
			mu.Unlock()
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
	if len(captured) != 1 || captured[0].State != "captured" || captured[0].LearningID == "" {
		t.Fatalf("consumed %+v", captured)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_learning`); n != 1 {
		t.Fatalf("learnings=%d", n)
	}
	// Re-verification and re-consumption never add a second learning.
	mustVerify(t, f, task, run)
	if outcomes, err := f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory}); err != nil || len(outcomes) != 0 {
		t.Fatalf("second pass %+v %v", outcomes, err)
	}
	if n := f.count(t, `SELECT count(*) FROM employee_learning`); n != 1 {
		t.Fatalf("learnings=%d", n)
	}

	private, err := f.memory.Search(ctx, f.privateScope(alice), "", 10)
	if err != nil || len(private) != 1 {
		t.Fatalf("requester private %+v %v", private, err)
	}
	l := private[0]
	if l.ID != captured[0].LearningID || !l.Trusted || l.Source != employeememory.LearningSourceExecution || l.Confidence != 7 || l.TaskID != task.ID || l.ExecutionID != run.ID {
		t.Fatalf("verified learning %+v", l.LearningRecord)
	}
	for _, want := range []string{"Verified outcome: 整理本周人员状态表并导出 report.csv", "Host-verified conditions:", "3 data rows", "研发部"} {
		if !strings.Contains(l.Insight, want) {
			t.Fatalf("insight lacks %q: %s", want, l.Insight)
		}
	}
	for _, scope := range []employeememory.Scope{f.privateScope(mallory), f.sceneScope()} {
		if rows, err := f.memory.Search(ctx, scope, "", 10); err != nil || len(rows) != 0 {
			t.Fatalf("verified learning leaked to %+v: %+v %v", scope.Kind, rows, err)
		}
	}
}

func TestVerifiedDistillPreResetEvidenceNoRevival(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	task, run := verifiedRun(t, f, humanTaskSource, alice)
	// The requester resets memory after the work ended but before verification
	// and distillation: the old evidence must not repopulate the namespace.
	time.Sleep(5 * time.Millisecond)
	if err := f.memory.Reset(ctx, f.privateScope(alice)); err != nil {
		t.Fatal(err)
	}
	mustVerify(t, f, task, run)
	outcomes, err := f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory})
	if err != nil || len(outcomes) != 1 || outcomes[0].State != "skipped" || outcomes[0].Reason != "pre_reset_evidence" {
		t.Fatalf("pre-reset evidence: %+v %v", outcomes, err)
	}
	if rows, err := f.memory.Search(ctx, f.privateScope(alice), "", 10); err != nil || len(rows) != 0 {
		t.Fatalf("revived %+v %v", rows, err)
	}

	// Forgetting a captured verified learning is final: a re-verified Run under
	// a changed contract replays the forgotten record, never a new active one.
	task2, run2 := verifiedRun(t, f, humanTaskSource, alice)
	mustVerify(t, f, task2, run2)
	outcomes, err = f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory})
	if err != nil || len(outcomes) != 1 || outcomes[0].State != "captured" {
		t.Fatalf("capture %+v %v", outcomes, err)
	}
	learningID := outcomes[0].LearningID
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.memory.ForgetPrivateTx(ctx, tx, f.privateScope(alice), learningID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.Spec(ctx, f.scope, task2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetSpec(ctx, f.scope, task2.ID, SetSpecParams{Origin: OriginHumanCue, SourceRef: "msg:narrower", AuthorRef: alice, ExpectedRevision: current.Revision, Checks: DeriveFromHumanText("完成标准：report.csv 共 3 行数据")}); err != nil {
		t.Fatal(err)
	}
	mustVerify(t, f, task2, run2)
	outcomes, err = f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory})
	if err != nil || len(outcomes) != 1 || outcomes[0].LearningID != learningID {
		t.Fatalf("replay after forget %+v %v", outcomes, err)
	}
	if rows, err := f.memory.Search(ctx, f.privateScope(alice), "", 10); err != nil || len(rows) != 0 {
		t.Fatalf("forgotten verified learning revived: %+v %v", rows, err)
	}
}

func TestVerifiedDistillAutomationScopeNoHumanPrivate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// An automation Task recorded a human creator as requester; that person's
	// private namespace must never receive the automation's learning.
	unconfigured, run := verifiedRun(t, f, "employee_routine", alice)
	if _, err := f.store.SetSpec(ctx, f.scope, unconfigured.ID, SetSpecParams{Origin: OriginHumanCue, SourceRef: "msg:x", AuthorRef: alice, Checks: reportChecks(t)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("human cue governed an automation task: %v", err)
	}
	f.spec(t, unconfigured, OriginAutomation, "routine:daily-report", reportChecks(t))
	mustVerify(t, f, unconfigured, run)

	sceneTask, sceneRun := verifiedRun(t, f, "employee_routine", alice)
	if _, err := f.store.SetSpec(ctx, f.scope, sceneTask.ID, SetSpecParams{Origin: OriginAutomation, SourceRef: "routine:weekly@1", AuthorRef: "routine:weekly", LearningScope: LearningScopeScene, Checks: reportChecks(t)}); err != nil {
		t.Fatal(err)
	}
	mustVerify(t, f, sceneTask, sceneRun)

	outcomes, err := f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory})
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("%+v %v", outcomes, err)
	}
	byRun := map[string]DistillOutcome{}
	for _, o := range outcomes {
		byRun[o.RunID] = o
	}
	if o := byRun[run.ID]; o.State != "skipped" || o.Reason != "automation_scope_unconfigured" {
		t.Fatalf("unconfigured automation %+v", o)
	}
	if o := byRun[sceneRun.ID]; o.State != "captured" {
		t.Fatalf("scene automation %+v", o)
	}
	if rows, err := f.memory.Search(ctx, f.privateScope(alice), "", 10); err != nil || len(rows) != 0 {
		t.Fatalf("automation wrote a human private namespace: %+v %v", rows, err)
	}
	if rows, err := f.memory.Search(ctx, f.sceneScope(), "", 10); err != nil || len(rows) != 1 || rows[0].TaskID != sceneTask.ID {
		t.Fatalf("scene learning %+v %v", rows, err)
	}
}

func TestVerifiedDistillFencesCancelledAndChangedGoal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cancelled, run := verifiedRun(t, f, humanTaskSource, alice)
	mustVerify(t, f, cancelled, run)
	if _, err := f.pool.Exec(ctx, `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	amended, run2 := verifiedRun(t, f, humanTaskSource, alice)
	mustVerify(t, f, amended, run2)
	current, err := f.tasks.Get(ctx, f.scope, amended.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.tasks.AppendInput(ctx, f.scope, amended.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, ActorRef: alice, Body: "目标变了",
		Correction: &employeetask.Definition{Goal: "完全不同的目标"}, ExpectedVersion: current.Version}); err != nil {
		t.Fatal(err)
	}
	archived, run3 := verifiedRun(t, f, humanTaskSource, alice)
	mustVerify(t, f, archived, run3)
	fence := func(_ context.Context, _ pgx.Tx, intent Intent) (string, error) {
		if intent.TaskID == archived.ID {
			return "agent_archived", nil
		}
		return "", nil
	}
	outcomes, err := f.store.ProcessVerifiedDistill(ctx, 10, DistillOptions{Memory: f.memory, Fence: fence})
	if err != nil || len(outcomes) != 3 {
		t.Fatalf("%+v %v", outcomes, err)
	}
	want := map[string]string{run.ID: "task_cancelled", run2.ID: "stale_goal_revision", run3.ID: "agent_archived"}
	for _, o := range outcomes {
		if o.State != "skipped" || o.Reason != want[o.RunID] {
			t.Fatalf("fence outcome %+v, want %s", o, want[o.RunID])
		}
	}
	if n := f.count(t, `SELECT count(*) FROM employee_learning`); n != 0 {
		t.Fatalf("fenced intents wrote %d learnings", n)
	}
}
