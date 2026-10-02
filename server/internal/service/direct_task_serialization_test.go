package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func enqueueAnotherDirectTask(t *testing.T, f directFixture) DirectTaskResult {
	t.Helper()
	ctx := context.Background()
	task, err := employeetask.NewStore(f.pool).Create(ctx, employeetask.CreateParams{
		Scope: f.request.Task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: f.request.Task.RequesterRef, Definition: employeetask.Definition{Goal: "Independent work"},
		Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Input: "Independent work",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := f.request
	request.Task, request.Prompt = task, "Independent work"
	result, err := f.service.EnqueueDirectTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDirectTasksClaimIndependentlyWithinAgentCapacity(t *testing.T) {
	for _, targeted := range []bool{false, true} {
		name := "runtime"
		if targeted {
			name = "run_once"
		}
		t.Run(name, func(t *testing.T) {
			f := directDatabase(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE agent SET max_concurrent_tasks=2 WHERE id=$1::uuid`, f.request.Task.Scope.AgentID); err != nil {
				t.Fatal(err)
			}
			first, err := f.service.EnqueueDirectTask(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			second := enqueueAnotherDirectTask(t, f)
			third := enqueueAnotherDirectTask(t, f)
			claim := func(want db.AgentTaskQueue) *db.AgentTaskQueue {
				t.Helper()
				var got *db.AgentTaskQueue
				var err error
				if targeted {
					got, err = f.service.ClaimTaskByIDForRuntime(ctx, want.RuntimeID, want.ID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{want.RuntimeID}})
				} else {
					got, err = f.service.ClaimTaskForRuntime(ctx, want.RuntimeID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{want.RuntimeID}})
				}
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			if got := claim(first.Task); got == nil || got.ID != first.Task.ID {
				t.Fatal("first Direct task was not claimed", got)
			}
			if got := claim(second.Task); got == nil || got.ID != second.Task.ID {
				t.Fatal("different EmployeeTask blocked despite free capacity", got)
			}
			if got := claim(third.Task); got != nil {
				t.Fatal("Direct task bypassed agent capacity", got.ID)
			}
		})
	}
}

func TestDirectClaimSeparatesQuickCreateAndStillSerializesSameTask(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	first, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.service.ClaimTaskForRuntime(ctx, first.Task.RuntimeID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{first.Task.RuntimeID}}); err != nil || got == nil {
		t.Fatal(got, err)
	}
	// A second queue mapping must not bypass the same-EmployeeTask guard even
	// if an upstream replay/admission defect ever creates it.
	duplicateID := util.MustParseUUID(uuid.NewString())
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context,trigger_evidence_kind,trigger_evidence_ref_id) VALUES($1,$2,$3,'queued',$4,'employee_task',$5)`, duplicateID, first.Task.AgentID, first.Task.RuntimeID, first.Task.Context, first.Task.TriggerEvidenceRefID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.service.ClaimTaskByIDForRuntime(ctx, first.Task.RuntimeID, duplicateID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{first.Task.RuntimeID}}); err != nil || got != nil {
		t.Fatal("same EmployeeTask claimed twice", got, err)
	}
	quickID, laterQuickID := util.MustParseUUID(uuid.NewString()), util.MustParseUUID(uuid.NewString())
	for _, id := range []pgtype.UUID{quickID, laterQuickID} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context) VALUES($1,$2,$3,'queued','{"type":"quick_create"}')`, id, first.Task.AgentID, first.Task.RuntimeID); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := f.service.ClaimTaskByIDForRuntime(ctx, first.Task.RuntimeID, quickID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{first.Task.RuntimeID}}); err != nil || got == nil {
		t.Fatal("Direct task incorrectly blocked quick-create", got, err)
	}
	if got, err := f.service.ClaimTaskByIDForRuntime(ctx, first.Task.RuntimeID, laterQuickID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{first.Task.RuntimeID}}); err != nil || got != nil {
		t.Fatal("quick-create serialization changed", got, err)
	}
	other := enqueueAnotherDirectTask(t, f)
	if got, err := f.service.ClaimTaskByIDForRuntime(ctx, first.Task.RuntimeID, other.Task.ID, TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{first.Task.RuntimeID}}); err != nil || got == nil {
		t.Fatal("quick-create incorrectly blocked independent Direct task", got, err)
	}
}

func TestDirectFCE2BSerializationAndFilesystemScope(t *testing.T) {
	agentID := util.MustParseUUID(uuid.NewString())
	workspaceID := uuid.NewString()
	employeeTaskID := uuid.NewString()
	makeTask := func(employeeID string) db.AgentTaskQueue {
		raw, _ := json.Marshal(DirectTaskContext{Type: DirectTaskContextType, WorkspaceID: workspaceID, EmployeeTaskID: employeeID, Prompt: "Do the work"})
		return db.AgentTaskQueue{ID: util.MustParseUUID(uuid.NewString()), AgentID: agentID, Context: raw, Status: "queued", CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	}
	target := makeTask(employeeTaskID)
	other, same := makeTask(uuid.NewString()), makeTask(employeeTaskID)
	quick := db.AgentTaskQueue{ID: util.MustParseUUID(uuid.NewString()), AgentID: agentID}
	for _, tc := range []struct {
		name      string
		candidate db.AgentTaskQueue
		blocked   bool
	}{
		{"different employee task", other, false},
		{"same employee task", same, true},
		{"quick create", quick, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory"} {
				candidate := tc.candidate
				candidate.Status = status
				candidate.CreatedAt = pgtype.Timestamptz{Time: target.CreatedAt.Time.Add(-time.Minute), Valid: true}
				if sameTaskSerializationGroup(target, candidate) != tc.blocked || sameTaskSerializationGroup(candidate, target) != tc.blocked {
					t.Fatalf("wrong serialization group for %s", status)
				}
				if _, _, blocked := fcE2BTaskLaunchBlocker(target, []db.AgentTaskQueue{candidate}); blocked != tc.blocked {
					t.Fatalf("wrong FC launch blocker for %s", status)
				}
				if blocked := fcE2BTaskHasActiveBlocker(target, []db.AgentTaskQueue{candidate}); blocked != (tc.blocked && status != "queued") {
					t.Fatalf("wrong FC claim blocker for %s", status)
				}
			}
		})
	}
	// Existing persistent filesystem locking stays per execution scope. Removing
	// the quick-create barrier must not make Direct tasks share a native workdir.
	key := dshhost.Key{WorkspaceID: uuid.MustParse(workspaceID), AgentID: uuid.UUID(agentID.Bytes)}
	a, b := dshExecutionScope(key, target), dshExecutionScope(key, other)
	if a.Kind != "task" || b.Kind != "task" || a.ID == b.ID || employeeFilesystemScopeID(a) == employeeFilesystemScopeID(b) {
		t.Fatal("independent Direct tasks share a writable filesystem scope", a, b)
	}
}

func TestDirectBulkCancellationReconcilesAfterRestart(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	admitted, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CancelTasksForAgent(ctx, admitted.Task.AgentID); err != nil {
		t.Fatal(err)
	}
	// No process-local event state survives into this replacement service.
	restarted := &TaskService{Queries: db.New(f.pool), TxStarter: f.pool}
	if _, err = restarted.ReconcileEmployeeRuns(ctx, 100); err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(f.pool).Get(ctx, f.request.Task.Scope, f.request.Task.ID)
	if err != nil || task.State != employeetask.StateCancelled || task.ActiveRunID != "" {
		t.Fatal(task, err)
	}
	if _, err = restarted.ReconcileEmployeeRuns(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var results int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_entry WHERE task_id=$1::uuid AND kind='result'`, task.ID).Scan(&results); err != nil || results != 1 {
		t.Fatal("bulk cancellation reconciliation lost or duplicated result", results, err)
	}
}
