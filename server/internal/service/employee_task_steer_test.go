package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type directSteerFixture struct {
	directFixture
	control EmployeeTaskControl
	first   DirectTaskResult
}

func newDirectSteerFixture(t *testing.T) directSteerFixture {
	t.Helper()
	f := directDatabase(t)
	first, err := f.service.EnqueueDirectTask(context.Background(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	return directSteerFixture{directFixture: f, control: EmployeeTaskControl{Tasks: f.service}, first: first}
}

func (f directSteerFixture) claim(t *testing.T, queue db.AgentTaskQueue, session string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE agent_task_queue SET status='running', dispatched_at=now(), started_at=now(), session_id=$2, work_dir='/same/workdir' WHERE id=$1`, queue.ID, session); err != nil {
		t.Fatal(err)
	}
}

func (f directSteerFixture) steer(source, content string) (EmployeeTaskSteerResult, error) {
	return f.control.Steer(context.Background(), EmployeeTaskSteerRequest{
		Task:          f.request.Task,
		Source:        employeetask.Source{Namespace: "test_steer", Key: source},
		ActorRef:      "member:requester",
		Content:       content,
		SameRequester: true,
	})
}

func (f directSteerFixture) claimNext(t *testing.T) (db.AgentTaskQueue, bool) {
	t.Helper()
	agentID := util.MustParseUUID(f.request.Task.Scope.AgentID)
	agent, err := f.service.Queries.GetAgent(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.service.Queries.ClaimAgentTask(context.Background(), db.ClaimAgentTaskParams{AgentID: agentID, PrepareLeaseSecs: 60, EmployeeDirectRuntimeIds: []pgtype.UUID{agent.RuntimeID}})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return claimed, true
}

func (f directSteerFixture) entryKinds(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT kind || COALESCE(':' || NULLIF(body,''),'') FROM employee_task_entry WHERE task_id=$1::uuid AND kind IN ('result','writer_fenced','steer','run_started') ORDER BY seq`, f.request.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return kinds
}

func directPrompt(t *testing.T, q db.AgentTaskQueue) DirectTaskContext {
	t.Helper()
	c, ok := ParseDirectTaskContext(q)
	if !ok {
		t.Fatalf("not a valid Direct execution: %s", q.Context)
	}
	return c
}

// The correction cancels the claimed Run, and its successor stays unclaimable
// until the runtime proves the old process group exited.
func TestEmployeeTaskSteerInterruptsClaimedRunBehindExitBarrier(t *testing.T) {
	f := newDirectSteerFixture(t)
	ctx := context.Background()
	f.claim(t, f.first.Task, "provider-session-1")

	got, err := f.steer("c1", "use the signed date, not the created date")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != employeetask.SteerInterrupted || got.Interrupted == nil || got.Interrupted.ID != f.first.Task.ID {
		t.Fatalf("outcome: %+v", got)
	}
	old, err := f.service.Queries.GetAgentTask(ctx, f.first.Task.ID)
	if err != nil || old.Status != "cancelled" || !taskProcessStopPending(old) {
		t.Fatalf("predecessor must hold the exit barrier: %+v %v", old, err)
	}
	if got.Queue.Status != "queued" || got.Queue.Priority != 4 || got.Queue.ID == old.ID {
		t.Fatalf("successor: %+v", got.Queue)
	}
	var private struct {
		Steer       bool   `json:"task_steer"`
		Predecessor string `json:"steer_predecessor_task_id"`
		Pending     bool   `json:"process_stop_pending"`
	}
	if err = json.Unmarshal(got.Queue.Context, &private); err != nil || !private.Steer || private.Predecessor != util.UUIDToString(old.ID) || private.Pending {
		t.Fatalf("successor context: %s", got.Queue.Context)
	}
	prompt := directPrompt(t, got.Queue).Prompt
	if !strings.Contains(prompt, "use the signed date, not the created date") || !strings.Contains(prompt, "Produce exact direct result") || !strings.Contains(prompt, "CURRENT CORRECTIONS") {
		t.Fatalf("successor prompt lacks the correction or the original input:\n%s", prompt)
	}
	if got.Task.State != employeetask.StateRunning || got.Task.ActiveRunID != got.Run.ID || got.Run.QueueTaskID != util.UUIDToString(got.Queue.ID) {
		t.Fatalf("task/run mapping: %+v %+v", got.Task, got.Run)
	}
	kinds := f.entryKinds(t)
	want := []string{"run_started", "result:task cancelled", "writer_fenced:claim_barrier", "steer:use the signed date, not the created date", "run_started"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("ledger:\n got %v\nwant %v", kinds, want)
	}
	if _, ok := f.claimNext(t); ok {
		t.Fatal("successor claimed before the old process group exited")
	}
	if err = f.service.AcknowledgeTaskProcessStopped(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	claimed, ok := f.claimNext(t)
	if !ok || claimed.ID != got.Queue.ID {
		t.Fatalf("successor not claimable after exit proof: %+v %v", claimed, ok)
	}
}

// Corrections racing in separate transactions produce one successor that
// carries all of them; nothing else is cancelled or queued.
func TestEmployeeTaskSteerConcurrentCorrectionsShareOneSuccessor(t *testing.T) {
	f := newDirectSteerFixture(t)
	ctx := context.Background()
	f.claim(t, f.first.Task, "provider-session-1")
	const count = 6
	start := make(chan struct{})
	results := make(chan EmployeeTaskSteerResult, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			<-start
			r, err := f.steer(fmt.Sprintf("c%d", i), fmt.Sprintf("correction %d", i))
			results <- r
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	interrupted, merged := 0, 0
	var successor db.AgentTaskQueue
	for r := range results {
		switch r.Outcome {
		case employeetask.SteerInterrupted:
			interrupted++
		case employeetask.SteerMerged:
			merged++
		}
		if successor.ID.Valid && successor.ID != r.Queue.ID {
			t.Fatalf("duplicate successor %v vs %v", successor.ID, r.Queue.ID)
		}
		successor = r.Queue
	}
	if interrupted != 1 || merged != count-1 {
		t.Fatalf("interrupted=%d merged=%d", interrupted, merged)
	}
	var queued, runs int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid AND status='queued'), (SELECT count(*) FROM employee_task_run WHERE task_id=$2::uuid)`, f.request.Task.Scope.AgentID, f.request.Task.ID).Scan(&queued, &runs); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || runs != 2 {
		t.Fatalf("queued=%d runs=%d", queued, runs)
	}
	current, err := f.service.Queries.GetAgentTask(ctx, successor.ID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := directPrompt(t, current).Prompt
	for i := range count {
		if !strings.Contains(prompt, fmt.Sprintf("correction %d", i)) {
			t.Fatalf("correction %d lost:\n%s", i, prompt)
		}
	}
	if strings.Count(prompt, "CURRENT CORRECTIONS") != 1 {
		t.Fatalf("corrections rendered more than once:\n%s", prompt)
	}
	var private struct {
		Predecessor string `json:"steer_predecessor_task_id"`
	}
	if json.Unmarshal(current.Context, &private) != nil || private.Predecessor != util.UUIDToString(f.first.Task.ID) {
		t.Fatalf("merged successor lost its predecessor: %s", current.Context)
	}
}

func TestEmployeeTaskSteerReplayReturnsTheSameSuccessor(t *testing.T) {
	f := newDirectSteerFixture(t)
	f.claim(t, f.first.Task, "provider-session-1")
	first, err := f.steer("c1", "stop after the summary")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.steer("c1", "stop after the summary")
	if err != nil || !again.Replayed || again.Queue.ID != first.Queue.ID || again.Run.ID != first.Run.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if _, err = f.steer("c1", "a different correction"); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatalf("same source with a different correction: %v", err)
	}
}

// An unclaimed original Run absorbs the correction in place: nothing is
// cancelled and it is not marked as a steer successor.
func TestEmployeeTaskSteerMergesIntoUnclaimedOriginalRun(t *testing.T) {
	f := newDirectSteerFixture(t)
	ctx := context.Background()
	got, err := f.steer("c1", "only include signed contracts")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != employeetask.SteerMerged || got.Interrupted != nil || got.Queue.ID != f.first.Task.ID {
		t.Fatalf("merge: %+v", got)
	}
	current, err := f.service.Queries.GetAgentTask(ctx, f.first.Task.ID)
	if err != nil || current.Status != "queued" {
		t.Fatalf("original run: %+v %v", current, err)
	}
	if prompt := directPrompt(t, current).Prompt; !strings.Contains(prompt, "only include signed contracts") {
		t.Fatalf("merged prompt:\n%s", prompt)
	}
	var private map[string]json.RawMessage
	_ = json.Unmarshal(current.Context, &private)
	if _, ok := private["task_steer"]; ok {
		t.Fatal("the original run is not a steer successor")
	}
	if got.Run.ID != f.first.Run.ID || got.Run.InputSeq != got.Entry.Seq {
		t.Fatalf("merged run input boundary: %+v entry=%d", got.Run, got.Entry.Seq)
	}
}

// After success the correction continues the same task; there is no writer to
// wait for, so the successor is immediately claimable.
func TestEmployeeTaskSteerContinuesCompletedTask(t *testing.T) {
	f := newDirectSteerFixture(t)
	ctx := context.Background()
	f.claim(t, f.first.Task, "provider-session-1")
	if err := f.service.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now(), result='{"output":"draft"}' WHERE id=$1`, f.first.Task.ID); err != nil {
			return err
		}
		done, err := q.GetAgentTask(ctx, f.first.Task.ID)
		if err != nil {
			return err
		}
		return f.service.recordEmployeeRunInTx(ctx, tx, done, "completed", done.Result, "")
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.steer("c1", "also add totals")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != employeetask.SteerContinued || got.Interrupted != nil || got.Queue.Status != "queued" {
		t.Fatalf("continue: %+v", got)
	}
	claimed, ok := f.claimNext(t)
	if !ok || claimed.ID != got.Queue.ID {
		t.Fatalf("continuation not claimable: %+v %v", claimed, ok)
	}
}

// A human stop is never lifted by a correction, and a failed Run without exit
// evidence keeps the writer fence closed. Neither failed steer leaves records.
func TestEmployeeTaskSteerFailsClosedWithoutWriterEvidence(t *testing.T) {
	for _, terminal := range []string{"cancelled", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			f := newDirectSteerFixture(t)
			ctx := context.Background()
			f.claim(t, f.first.Task, "provider-session-1")
			if err := f.service.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `UPDATE agent_task_queue SET status=$2, completed_at=now() WHERE id=$1`, f.first.Task.ID, terminal); err != nil {
					return err
				}
				done, err := q.GetAgentTask(ctx, f.first.Task.ID)
				if err != nil {
					return err
				}
				return f.service.recordEmployeeRunInTx(ctx, tx, done, terminal, nil, "provider error")
			}); err != nil {
				t.Fatal(err)
			}
			before := f.entryKinds(t)
			want := employeetask.ErrStopped
			if terminal == "failed" {
				want = employeetask.ErrRunNotReady
			}
			if _, err := f.steer("c1", "continue anyway"); !errors.Is(err, want) {
				t.Fatalf("steer after %s: got %v, want %v", terminal, err, want)
			}
			if after := f.entryKinds(t); strings.Join(after, "|") != strings.Join(before, "|") {
				t.Fatalf("failed steer left records: %v -> %v", before, after)
			}
		})
	}
}

// Identity tokens never carry over, and personal connectors stay only for the
// task's own requester. An unclaimed Run keeps its dispatch-time overlay.
func TestEmployeeTaskSteerDropsCarriedCredentials(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_requester=%v", same), func(t *testing.T) {
			f := newDirectSteerFixture(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET context=context || '{"agent_identity_context_token":"old-token","dispatch_outbound_sent":true}'::jsonb, runtime_mcp_overlay='{"servers":{"personal":{}}}'::jsonb WHERE id=$1`, f.first.Task.ID); err != nil {
				t.Fatal(err)
			}
			request := func(key string) EmployeeTaskSteerRequest {
				return EmployeeTaskSteerRequest{
					Task: f.request.Task, Source: employeetask.Source{Namespace: "test_steer", Key: key},
					ActorRef: "member:someone", Content: "narrow the scope " + key, SameRequester: same,
					Context: json.RawMessage(`{"agent_identity_context_token":"fresh-token","employee_task_id":"forged"}`),
				}
			}
			merged, err := f.control.Steer(ctx, request("merge"))
			if err != nil || merged.Outcome != employeetask.SteerMerged {
				t.Fatalf("merge: %+v %v", merged, err)
			}
			if (len(merged.Queue.RuntimeMcpOverlay) > 0) != same {
				t.Fatalf("unclaimed overlay kept=%v for same_requester=%v", len(merged.Queue.RuntimeMcpOverlay) > 0, same)
			}
			f.claim(t, merged.Queue, "provider-session-1")
			got, err := f.control.Steer(ctx, request("interrupt"))
			if err != nil || got.Outcome != employeetask.SteerInterrupted {
				t.Fatalf("interrupt: %+v %v", got, err)
			}
			for _, q := range []db.AgentTaskQueue{merged.Queue, got.Queue} {
				var private map[string]any
				if err = json.Unmarshal(q.Context, &private); err != nil {
					t.Fatal(err)
				}
				if private["agent_identity_context_token"] != "fresh-token" || private["employee_task_id"] != f.request.Task.ID || private["dispatch_outbound_sent"] != nil {
					t.Fatalf("context carried stale or forged state: %s", q.Context)
				}
			}
			if len(got.Queue.RuntimeMcpOverlay) > 0 {
				t.Fatal("a terminal predecessor's overlay must be recomputed, never copied")
			}
		})
	}
}

// The Issue backend steers through the Issue primitive: one member comment, one
// exit-fenced successor mapped to a new Run of the same EmployeeTask.
func TestEmployeeTaskSteerIssueBackendMapsSuccessorRun(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	first, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now() WHERE id=$1`, first.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err = f.pool.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE issue_id=$1`, first.Issue.ID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	task, err := f.svc.TaskService.readEmployeeTask(ctx, p.Intent.Scope, taskID)
	if err != nil {
		t.Fatal(err)
	}
	control := EmployeeTaskControl{Tasks: f.svc.TaskService, Issues: backend}
	request := EmployeeTaskSteerRequest{Task: task, Source: employeetask.Source{Namespace: "human_steer", Key: "issue-correction"}, ActorRef: "member:" + util.UUIDToString(f.params.AuthorID), Content: "Only cover signed contracts", AuthorID: f.params.AuthorID}
	if _, err = control.Steer(ctx, EmployeeTaskSteerRequest{Task: task, Source: request.Source, ActorRef: request.ActorRef, Content: request.Content}); !errors.Is(err, ErrEmployeeTaskSteerUnsupported) {
		t.Fatalf("issue steer without a member author: %v", err)
	}
	got, err := control.Steer(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != employeetask.SteerInterrupted || got.Interrupted == nil || got.Interrupted.ID != first.EnqueuedTask.ID || !got.CommentID.Valid || got.Queue.Status != "queued" {
		t.Fatalf("issue steer: %+v", got)
	}
	old, err := f.svc.Queries.GetAgentTask(ctx, first.EnqueuedTask.ID)
	if err != nil || !taskProcessStopPending(old) {
		t.Fatalf("predecessor barrier: %+v %v", old, err)
	}
	if got.Run.QueueTaskID != util.UUIDToString(got.Queue.ID) || got.Task.ActiveRunID != got.Run.ID {
		t.Fatalf("successor run mapping: task=%+v run=%+v", got.Task, got.Run)
	}
	again, err := control.Steer(ctx, request)
	if err != nil || again.Queue.ID != got.Queue.ID || again.CommentID != got.CommentID {
		t.Fatalf("issue steer replay: %+v %v", again, err)
	}
}

// A completion holding the queue row and then taking the EmployeeTask (the
// RecordResult order) must not deadlock with a concurrent steer: steer takes
// the same order, waits, and then continues the finished task.
func TestEmployeeTaskSteerSharesCompletionLockOrder(t *testing.T) {
	f := newDirectSteerFixture(t)
	ctx := context.Background()
	f.claim(t, f.first.Task, "provider-session-1")
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, f.request.Task.Scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1`, f.first.Task.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := f.steer("concurrent", "add totals"); done <- e }()
	time.Sleep(300 * time.Millisecond)
	if _, err = tx.Exec(ctx, `SELECT id FROM employee_task WHERE id=$1::uuid FOR UPDATE`, f.request.Task.ID); err != nil {
		t.Fatalf("completion lost a lock-order race: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("steer did not finish after the completion committed")
	}
	if err != nil {
		t.Fatalf("steer after the concurrent completion: %v", err)
	}
}
