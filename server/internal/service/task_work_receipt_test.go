package service

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"testing"
)

func TestSynchronousWorkReceiptPreservesFrozenPayload(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	agent := pgtype.UUID{Bytes: [16]byte{71}, Valid: true}
	callback := "/api/v1/dispatch-tasks/work-receipt-frozen/execution-result"
	key := "multica-terminal:sync-completed:work-receipt-frozen"
	t.Cleanup(func() { pool.Exec(ctx, "DELETE FROM task_completion_outbox WHERE request_id=$1", key) })
	if err := svc.EnqueueSynchronousCompleted(ctx, callback, taskCompletionTestTarget, agent, "original acknowledgement"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE task_completion_outbox SET status='delivered',attempt_count=3 WHERE request_id=$1", key); err != nil {
		t.Fatal(err)
	}
	before, err := svc.Queries.GetTaskCompletionByRequestID(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnqueueSynchronousWorkReceipt(ctx, callback, taskCompletionTestTarget, agent, "normalized work acknowledgement"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Queries.GetTaskCompletionByRequestID(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if after.ResultMessage != before.ResultMessage || after.Status != before.Status || after.AttemptCount != before.AttemptCount || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("frozen payload/state changed: before=%+v after=%+v", before, after)
	}
	if err := svc.EnqueueSynchronousCompleted(ctx, callback, taskCompletionTestTarget, agent, "ordinary reply"); err == nil {
		t.Fatal("ordinary reply accepted conflicting payload")
	}
	wrapupKey := "multica-terminal:sync-wrapup:task:work-receipt-frozen"
	t.Cleanup(func() { pool.Exec(ctx, "DELETE FROM task_completion_outbox WHERE request_id=$1", wrapupKey) })
	if err := svc.EnqueueSynchronousWrapup(ctx, callback, taskCompletionTestTarget, agent, "original wrapup", "task"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnqueueSynchronousWrapup(ctx, callback, taskCompletionTestTarget, agent, "changed wrapup", "task"); err == nil {
		t.Fatal("wrapup accepted conflicting payload")
	}
	other := agent
	other.Bytes[0]++
	if err := svc.EnqueueSynchronousWorkReceipt(ctx, callback, taskCompletionTestTarget, other, "normalized"); err == nil {
		t.Fatal("accepted another agent")
	}
	for _, column := range []string{"callback_url", "target_identity", "execution_status"} {
		if _, err := pool.Exec(ctx, "UPDATE task_completion_outbox SET "+column+"=$2 WHERE request_id=$1", key, map[string]string{"callback_url": "/api/v1/dispatch-tasks/other/execution-result", "target_identity": "router-target:v1:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "execution_status": "failed"}[column]); err != nil {
			t.Fatal(err)
		}
		if err := svc.EnqueueSynchronousWorkReceipt(ctx, callback, taskCompletionTestTarget, agent, "normalized"); err == nil {
			t.Fatalf("accepted conflicting %s", column)
		}
		if _, err := pool.Exec(ctx, "UPDATE task_completion_outbox SET callback_url=$2,target_identity=$3,execution_status='completed' WHERE request_id=$1", key, callback, taskCompletionTestTarget); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoordinatorIssueAckPreservesFrozenPayloadAndScope(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{72}, Valid: true}, AgentID: pgtype.UUID{Bytes: [16]byte{73}, Valid: true}}
	issue := db.Issue{ID: pgtype.UUID{Bytes: [16]byte{74}, Valid: true}}
	callback := "/api/v1/dispatch-tasks/work-update-frozen/execution-update"
	t.Cleanup(func() { pool.Exec(ctx, "DELETE FROM task_execution_update_outbox WHERE root_task_id=$1", task.ID) })
	if err := svc.EnqueueCoordinatorIssueAck(ctx, task, issue, "MUL-1", callback, taskCompletionTestTarget, "original acknowledgement"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE task_execution_update_outbox SET status='delivered',attempt_count=3 WHERE root_task_id=$1", task.ID); err != nil {
		t.Fatal(err)
	}
	before, err := svc.Queries.GetTaskExecutionUpdateByRootTaskID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnqueueCoordinatorIssueAck(ctx, task, issue, "MUL-1", callback, taskCompletionTestTarget, "normalized acknowledgement"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Queries.GetTaskExecutionUpdateByRootTaskID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ResultMessage != before.ResultMessage || after.Status != before.Status || after.AttemptCount != before.AttemptCount || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("frozen update changed: before=%+v after=%+v", before, after)
	}
	for _, tc := range []struct {
		name                         string
		task                         db.AgentTaskQueue
		issue                        db.Issue
		identifier, callback, target string
	}{
		{"agent", db.AgentTaskQueue{ID: task.ID, AgentID: issue.ID}, issue, "MUL-1", callback, taskCompletionTestTarget},
		{"issue", task, db.Issue{ID: task.AgentID}, "MUL-1", callback, taskCompletionTestTarget},
		{"identifier", task, issue, "MUL-2", callback, taskCompletionTestTarget},
		{"callback", task, issue, "MUL-1", "/api/v1/dispatch-tasks/other/execution-update", taskCompletionTestTarget},
		{"target", task, issue, "MUL-1", callback, "router-target:v1:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		if err := svc.EnqueueCoordinatorIssueAck(ctx, tc.task, tc.issue, tc.identifier, tc.callback, tc.target, "normalized"); err == nil {
			t.Fatalf("accepted conflicting %s", tc.name)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE task_execution_update_outbox SET result_message_frozen=false,status='waiting_result',result_message=NULL WHERE root_task_id=$1", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnqueueCoordinatorIssueAck(ctx, task, issue, "MUL-1", callback, taskCompletionTestTarget, "normalized"); err == nil {
		t.Fatal("accepted unfrozen update")
	}
}
