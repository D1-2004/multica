package handler

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/employeeverification"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

func ensureEmployeeVerificationSchema(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "997*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("verification migrations: %v %v", files, err)
	}
	sort.Strings(files)
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = testPool.Exec(context.Background(), string(raw)); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
	}
}

// The Host adapter reads the sealed Storage object of a real uploaded Run
// artifact, verifies it deterministically and distills exactly once into the
// configured automation scope, never into the creator's private namespace.
func TestEmployeeVerificationReadsSealedRunArtifactAndDistillsOnce(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	ensureEmployeeVerificationSchema(t)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_verified_distill", "employee_task_verification", "employee_task_verification_spec", "employee_learning", "employee_memory_state"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, f.scope.WorkspaceID)
		}
	})
	previousMemory := testHandler.EmployeeMemory
	testHandler.EmployeeMemory = employeememory.NewStore(testPool)
	t.Cleanup(func() { testHandler.EmployeeMemory = previousMemory })

	if w := f.upload(t, "report.csv", []byte("区域,金额\n华东,12\n华北,8\n")); w.Code != http.StatusOK {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	store := employeetask.NewStore(testPool)
	if _, _, err := store.RecordResult(ctx, f.scope, f.task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: f.access.owned}, RunID: f.run.ID, State: employeetask.StateSucceeded, Result: "PASS"}); err != nil {
		t.Fatal(err)
	}
	if _, err := employeeverification.NewStore(testPool).SetSpec(ctx, f.scope, f.task.ID, employeeverification.SetSpecParams{Origin: employeeverification.OriginHostFixture, SourceRef: "fixture:" + uuid.NewString(), AuthorRef: "host:fixture", LearningScope: employeeverification.LearningScopeScene,
		Checks: []employeeverification.Check{{Kind: employeeverification.KindArtifactContents, File: "report.csv", DataRows: intPointer(2), Columns: []string{"区域", "金额"}}}}); err != nil {
		t.Fatal(err)
	}

	// A swapped Storage object fails to open; nothing is recorded.
	f.storage.mu.Lock()
	saved := map[string][]byte{}
	for key, data := range f.storage.files {
		saved[key] = data
		f.storage.files[key] = append([]byte("x"), data[1:]...)
	}
	f.storage.mu.Unlock()
	if _, err := testHandler.VerifyEmployeeRun(ctx, f.scope, f.task.ID, f.run.ID); !errors.Is(err, errEmployeeArtifactInvalid) {
		t.Fatalf("tampered object verified: %v", err)
	}
	f.storage.mu.Lock()
	for key, data := range saved {
		f.storage.files[key] = data
	}
	f.storage.mu.Unlock()
	var records int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_verification WHERE workspace_id=$1::uuid`, f.scope.WorkspaceID).Scan(&records); err != nil || records != 0 {
		t.Fatalf("records after failed read=%d %v", records, err)
	}

	// The durable trigger finds the Run without any notification.
	if n, err := testHandler.ReconcileEmployeeVerifications(ctx, 100); err != nil || n < 1 {
		t.Fatalf("pending verification n=%d %v", n, err)
	}
	result, err := testHandler.VerifyEmployeeRun(ctx, f.scope, f.task.ID, f.run.ID)
	if err != nil || result.Gate.Status != employeeverification.GatePassed || !result.Intent {
		t.Fatalf("verify %+v %v", result, err)
	}
	for range 2 {
		if _, err = testHandler.ReconcileEmployeeVerifiedDistill(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	memoryScope := employeememory.Scope{WorkspaceID: parseUUID(f.scope.WorkspaceID), AgentID: parseUUID(f.scope.AgentID), TenantOrgID: f.scope.TenantOrgID, Scene: f.scope.Scene, Kind: employeememory.ScopeScene}
	rows, err := testHandler.EmployeeMemory.Search(ctx, memoryScope, "", 10)
	if err != nil || len(rows) != 1 || !rows[0].Trusted || rows[0].Source != employeememory.LearningSourceExecution || rows[0].ExecutionID != f.run.ID {
		t.Fatalf("scene learning %+v %v", rows, err)
	}
	private := memoryScope
	private.Kind, private.PrincipalID = employeememory.ScopePrivate, f.task.RequesterRef
	if rows, err = testHandler.EmployeeMemory.Search(ctx, private, "", 10); err != nil || len(rows) != 0 {
		t.Fatalf("automation learning reached the creator's private namespace: %+v %v", rows, err)
	}
}

func intPointer(v int) *int { return &v }
