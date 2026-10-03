package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEmployeeExecutionEventTerminalFactIsDurableWithoutWork(t *testing.T) {
	for _, state := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := employeeNoticeDatabase(t, state, false, false)
			ctx := context.Background()
			before := employeeExecutionCounts(t, f.agentID)
			client, exporter := employeeTraceClient(t)
			f.h.EmployeeSceneWorker.Langfuse = client
			logs := captureNoticeLogs(t)
			var wg sync.WaitGroup
			errs := make(chan error, 6)
			for range 6 {
				wg.Go(func() { _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); errs <- err })
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			var raw []byte
			var count int
			var consumed, reason, principal, job string
			if err := testPool.QueryRow(ctx, `SELECT r.envelope,c.state,c.reason,c.principal_id::text,COALESCE(c.job_id::text,'') FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id WHERE r.source='employee.execution' AND r.source_event_id=$1`, f.runID).Scan(&raw, &consumed, &reason, &principal, &job); err != nil {
				t.Fatal(err)
			}
			var ev eventrouter.Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if consumed != "completed" || reason != "current_goal_revision" || principal != testUserID || job != "" || ev.Category != eventrouter.RunCallback || payload["run_id"] != f.runID || payload["queue_task_id"] != f.queueID || payload["state"] != state || ev.OccurredAt.IsZero() {
				t.Fatalf("invalid terminal fact: %s state=%s reason=%s principal=%s job=%s", raw, consumed, reason, principal, job)
			}
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			if got := employeeExecutionCounts(t, f.agentID); got != before || f.model.calls != 1 {
				t.Fatalf("fact created work: before=%v after=%v model=%d", before, got, f.model.calls)
			}
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); worked || err != nil {
				t.Fatal("fact became model work", worked, err)
			}
			if got := employeeTraceKind(exporter, "generation"); len(got) != 0 {
				t.Fatal("fact created generation", len(got))
			}
			if got := employeeExecutionTraceEvents(exporter); len(got) != 1 {
				t.Fatal("expected one committed fact event", len(got))
			} else if got[0].SpanContext.TraceID().String() != strings.ReplaceAll(f.jobID, "-", "") || !got[0].StartTime.Equal(got[0].EndTime) || employeeTraceAttr(got[0], "langfuse.observation.metadata.queue_task_id") != f.queueID || employeeTraceAttr(got[0], "langfuse.observation.metadata.source_receipt_id") == "" || employeeTraceAttr(got[0], "langfuse.observation.usage_details") != "" {
				t.Fatal("fact trace lost lineage or fabricated model duration/usage", got[0])
			} else {
				for key, want := range map[string]string{"state": "completed", "run_state": state, "result_ref": "agent_task_queue:" + f.queueID} {
					if value := employeeTraceAttr(got[0], "langfuse.observation.metadata."+key); value != want {
						t.Errorf("Langfuse %s=%q want %q", key, value, want)
					}
				}
			}
			for key, value := range map[string]string{"employee_run_id": f.runID, "queue_task_id": f.queueID, "employee_job_id": f.jobID} {
				found := 0
				for _, span := range exporter.GetSpans() {
					if span.Name == langfuse.IndexObservationName(key, value) {
						found++
					}
				}
				if found != 1 {
					t.Fatalf("missing or duplicate lookup index %s=%s: %d", key, value, found)
				}
			}
			if got := noticeLogEvents(t, logs, "employee_execution_event_recorded"); len(got) != 1 {
				t.Fatal("duplicate or missing committed fact log", len(got))
			} else {
				for key, want := range map[string]string{"state": "completed", "run_state": state, "result_ref": "agent_task_queue:" + f.queueID} {
					if value := got[0][key]; value != want {
						t.Errorf("SLS %s=%v want %q", key, value, want)
					}
				}
			}
			if strings.Contains(string(raw), "真实已存结果") || strings.Contains(logs.snapshot(), "真实已存结果") {
				t.Fatal("fact duplicated result body")
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); n != 0 || err != nil {
				t.Fatal("replay changed", n, err)
			}
			if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
				t.Fatal(err)
			}
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&count); err != nil || count != 1 {
				t.Fatal("original notice lost", count, err)
			}
		})
	}
}

func employeeExecutionCounts(t *testing.T, agent string) [4]int {
	t.Helper()
	var n [4]int
	for i, table := range []string{"employee_scene_job", "employee_task", "agent_task_queue", "response_action"} {
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE agent_id=$1::uuid`, agent).Scan(&n[i]); err != nil {
			t.Fatal(err)
		}
	}
	return n
}

func employeeExecutionTraceEvents(exporter *tracetest.InMemoryExporter) []tracetest.SpanStub {
	var spans []tracetest.SpanStub
	for _, span := range exporter.GetSpans() {
		if span.Name == "employee_execution_event" {
			spans = append(spans, span)
		}
	}
	return spans
}

func TestEmployeeExecutionEventKeepsOriginalOwnershipAndRevision(t *testing.T) {
	for _, change := range []string{"mode", "revoked_principal", "old_goal"} {
		t.Run(change, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			ctx := context.Background()
			switch change {
			case "mode":
				if _, err := testPool.Exec(ctx, `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
			case "revoked_principal":
				revokeEmployeeNoticePrincipal(t)
			case "old_goal":
				request := employeeExecutionReplayRequest(t, f)
				definition := request.Task.Definition
				definition.Goal = "The corrected goal must not be completed by the old run"
				if _, _, err := employeetask.NewStore(testPool).AppendInput(ctx, request.Task.Scope, request.Task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, ActorRef: request.Task.RequesterRef, Body: "Correct the goal", Correction: &definition, ExpectedVersion: request.Task.Version}); err != nil {
					t.Fatal(err)
				}
			}
			before := employeeExecutionCounts(t, f.agentID)
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			var owner, route, reason, principal string
			if err := testPool.QueryRow(ctx, `SELECT c.owner_loop,r.route,c.reason,c.principal_id::text FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id WHERE r.source='employee.execution' AND r.source_event_id=$1`, f.runID).Scan(&owner, &route, &reason, &principal); err != nil {
				t.Fatal(err)
			}
			want := "current_goal_revision"
			if change == "old_goal" {
				want = "historical_goal_revision"
			}
			if owner != "employee" || route != eventrouter.Unified || reason != want || principal != testUserID {
				t.Fatal(owner, route, reason, principal)
			}
			if got := employeeExecutionCounts(t, f.agentID); got != before {
				t.Fatal("terminal fact changed work", before, got)
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestEmployeeExecutionEventHoldsUnusableSceneWithoutRemapping(t *testing.T) {
	for _, change := range []string{"rebound", "missing_scene", "missing_identity"} {
		t.Run(change, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			ctx := context.Background()
			if change == "rebound" {
				if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id='another-org' WHERE agent_id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
			} else if change == "missing_identity" {
				if _, err := testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := testPool.Exec(ctx, `DELETE FROM agent_scene WHERE agent_id=$1::uuid`, f.agentID); err != nil {
					t.Fatal(err)
				}
			}
			var scenesBefore, scenesAfter int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_scene WHERE agent_id=$1::uuid`, f.agentID).Scan(&scenesBefore); err != nil {
				t.Fatal(err)
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			var state, reason string
			var sceneNull, jobNull bool
			if err := testPool.QueryRow(ctx, `SELECT c.state,c.reason,c.scene_id IS NULL,c.job_id IS NULL FROM employee_event_consumption c JOIN scene_event_receipt r ON r.id=c.receipt_id WHERE r.source='employee.execution' AND r.source_event_id=$1`, f.runID).Scan(&state, &reason, &sceneNull, &jobNull); err != nil {
				t.Fatal(err)
			}
			if state != "held" || reason == "" || !sceneNull || !jobNull {
				t.Fatal(state, reason, sceneNull, jobNull)
			}
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_scene WHERE agent_id=$1::uuid`, f.agentID).Scan(&scenesAfter); err != nil || scenesAfter != scenesBefore {
				t.Fatal("remapped or registered scene", scenesBefore, scenesAfter, err)
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestEmployeeExecutionEventUsesRegisteredSecondaryTenant(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	request := employeeExecutionReplayRequest(t, f)
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id='another-primary-org' WHERE agent_id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_tenant(workspace_id,agent_id,org_id,name) VALUES($1::uuid,$2::uuid,'456','Registered secondary tenant')`, testWorkspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_tenant WHERE workspace_id=$1::uuid AND agent_id=$2::uuid`, testWorkspaceID, f.agentID)
	})
	// The same authoritative scene fence used by the foreground accepts this
	// explicitly registered tenant even though it is not the primary identity org.
	if _, err := fencedScene(ctx, f.h.Queries, &request.Task.Scope.Scene, scene.Owner{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}, "456"); err != nil {
		t.Fatal("invalid secondary tenant fixture", err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var state, reason, sceneID string
	if err := testPool.QueryRow(ctx, `SELECT c.state,c.reason,COALESCE(c.scene_id::text,'') FROM employee_event_consumption c JOIN scene_event_receipt e ON e.id=c.receipt_id WHERE e.source='employee.execution' AND e.source_event_id=$1`, f.runID).Scan(&state, &reason, &sceneID); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || reason != "current_goal_revision" || sceneID != request.Task.Scope.Scene.SceneID {
		t.Fatal("registered secondary tenant was treated as a rebound", state, reason, sceneID)
	}
}

func TestEmployeeExecutionEventTenantLookupCancellationRollsBack(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id='another-primary-org' WHERE agent_id=$1::uuid`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_tenant(workspace_id,agent_id,org_id,name) VALUES($1::uuid,$2::uuid,'456','Registered secondary tenant')`, testWorkspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM agent_tenant WHERE agent_id=$1::uuid`, f.agentID) })
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `LOCK TABLE agent_tenant IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	writer, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h := *f.h
	h.TxStarter = writer
	client, exporter := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = client
	logs := captureNoticeLogs(t)
	finished := make(chan error, 1)
	go func() {
		n, err := h.ReconcileEmployeeExecutionEvents(readCtx, 100)
		if n != 0 {
			err = fmt.Errorf("lookup failure committed %d facts: %w", n, err)
		}
		finished <- err
	}()
	for {
		var blocked bool
		var query string
		if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids(pid))>0,query FROM pg_stat_activity WHERE pid=$1`, writer.Conn().PgConn().PID()).Scan(&blocked, &query); err != nil {
			t.Fatal(err)
		}
		if blocked {
			if !strings.Contains(query, "FROM agent_tenant") {
				t.Fatal("blocked before the authoritative tenant lookup", query)
			}
			break
		}
		select {
		case err := <-finished:
			t.Fatal("did not reach the tenant fence", err)
		case <-time.After(5 * time.Millisecond):
		case <-readCtx.Done():
			t.Fatal(readCtx.Err())
		}
	}
	// An unbind must not slip between the digital-employee existence check
	// and fencedScene's second identity read (which also supports robots).
	deleteCtx, deleteCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, deleteErr := testPool.Exec(deleteCtx, `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	deleteCancel()
	if !errors.Is(deleteErr, context.DeadlineExceeded) {
		t.Fatal("identity was not protected during the tenant fence", deleteErr)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("transient tenant failure became a disposition", err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failure persisted a held receipt", count, err)
	}
	if len(employeeExecutionTraceEvents(exporter)) != 0 || len(noticeLogEvents(t, logs, "employee_execution_event_recorded")) != 0 {
		t.Fatal("failure exported a committed fact")
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
		t.Fatal("tenant lookup could not recover", n, err)
	}
}

func employeeExecutionReplayRequest(t *testing.T, f employeeNoticeFixture) service.DirectTaskRequest {
	t.Helper()
	ctx := context.Background()
	queue, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.queueID))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := service.ParseDirectTaskContext(queue)
	if !ok {
		t.Fatal("fixture lost Direct context")
	}
	var contextFields struct {
		Input json.RawMessage `json:"employee_direct_input"`
		Scene scene.Ref       `json:"agent_scene"`
	}
	if err := json.Unmarshal(queue.Context, &contextFields); err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(testPool).Get(ctx, employeetask.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "456", Kind: employeetask.ScopeScene, Scene: contextFields.Scene}, c.EmployeeTaskID)
	if err != nil {
		t.Fatal(err)
	}
	var source employeetask.Source
	if err := testPool.QueryRow(ctx, `SELECT source_namespace,source_key FROM employee_task_entry WHERE run_id=$1::uuid AND kind='run_started'`, f.runID).Scan(&source.Namespace, &source.Key); err != nil {
		t.Fatal(err)
	}
	return service.DirectTaskRequest{Task: task, Source: source, Prompt: c.Prompt, PrincipalID: parseUUID(c.PrincipalID), Context: contextFields.Input}
}

func TestEmployeeExecutionEventSkipIsVersionedAndPreservesDirectReplay(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	request := employeeExecutionReplayRequest(t, f)
	if _, err := f.h.TaskService.EnqueueDirectTask(ctx, request); err != nil {
		t.Fatal(err)
	}
	// A historical queue has lost its source job. Reassess the old proof without
	// changing the wrapper that lets old replicas recognize a settled skip.
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=(context-'employee_job_id') || jsonb_build_object('keep_existing','sentinel','employee_execution_event_skip',jsonb_build_object('version',1,'run_id',$2::text,'reason','old')) WHERE id=$1::uuid`, f.queueID, f.runID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
		t.Fatal("other version hid historical skip", n, err)
	}
	var state []byte
	var unchanged bool
	if err := testPool.QueryRow(ctx, `SELECT context->'employee_execution_event_skip',context->>'keep_existing'='sentinel' AND context->'employee_direct_input'=$2::jsonb FROM agent_task_queue WHERE id=$1::uuid`, f.queueID, request.Context).Scan(&state, &unchanged); err != nil {
		t.Fatal(err)
	}
	var skip map[string]any
	if err := json.Unmarshal(state, &skip); err != nil {
		t.Fatal(err)
	}
	if !unchanged || skip["version"] != float64(1) || skip["proof_version"] != float64(employeeExecutionProofVersion) || skip["run_id"] != f.runID || skip["reason"] != "source_job_missing" || len(skip) != 4 {
		t.Fatal(unchanged, string(state))
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 0 {
		t.Fatal("permanent skip rescanned", n, err)
	}
	replayed, err := f.h.TaskService.EnqueueDirectTask(ctx, request)
	if err != nil || uuid.UUID(replayed.Task.ID.Bytes).String() != f.queueID || replayed.Run.ID != f.runID {
		t.Fatal("skip enrichment broke Direct replay", err)
	}
	var receipts int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("skip fabricated a source", receipts, err)
	}
}

func TestEmployeeExecutionEventDoesNotDowngradeSettledProof(t *testing.T) {
	for _, proof := range []int{employeeExecutionProofVersion, employeeExecutionProofVersion + 1} {
		t.Run(fmt.Sprint(proof), func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('employee_execution_event_skip',jsonb_build_object('version',1,'proof_version',$3::int,'run_id',$2::text,'reason','source_dispatch_missing')) WHERE id=$1::uuid`, f.queueID, f.runID, proof); err != nil {
				t.Fatal(err)
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 0 {
				t.Fatal("settled current/newer proof was reopened", n, err)
			}
			var preserved bool
			if err := testPool.QueryRow(ctx, `SELECT context->'employee_execution_event_skip'->'proof_version'=to_jsonb($2::int) FROM agent_task_queue WHERE id=$1::uuid`, f.queueID, proof).Scan(&preserved); err != nil || !preserved {
				t.Fatal("proof downgraded", preserved, err)
			}
		})
	}
}

func TestEmployeeExecutionEventRechecksNewerProofAfterSceneWait(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	request := employeeExecutionReplayRequest(t, f)
	ctx := context.Background()
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT id FROM agent_scene WHERE id=$1::uuid FOR UPDATE`, request.Task.Scope.Scene.SceneID); err != nil {
		t.Fatal(err)
	}
	writer, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h := *f.h
	h.TxStarter = writer
	client, exporter := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = client
	logs := captureNoticeLogs(t)
	finished := make(chan error, 1)
	go func() {
		n, err := h.ReconcileEmployeeExecutionEvents(readCtx, 100)
		if n != 0 {
			err = fmt.Errorf("older proof created %d facts: %w", n, err)
		}
		finished <- err
	}()
	for {
		var blocked bool
		var query string
		if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids(pid))>0,query FROM pg_stat_activity WHERE pid=$1`, writer.Conn().PgConn().PID()).Scan(&blocked, &query); err != nil {
			t.Fatal(err)
		}
		if blocked {
			if !strings.Contains(query, "FROM agent_scene") {
				t.Fatal("blocked before scene lock", query)
			}
			break
		}
		select {
		case err := <-finished:
			t.Fatal("candidate never reached scene lock", err)
		case <-time.After(5 * time.Millisecond):
		case <-readCtx.Done():
			t.Fatal(readCtx.Err())
		}
	}
	if _, err := holder.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('employee_execution_event_skip',jsonb_build_object('version',1,'proof_version',3,'run_id',$2::text,'reason','newer_proof_rejected')) WHERE id=$1::uuid`, f.queueID, f.runID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&count); err != nil || count != 0 {
		t.Fatal("older proof bypassed newer rejection", count, err)
	}
	if len(employeeExecutionTraceEvents(exporter)) != 0 || len(noticeLogEvents(t, logs, "employee_execution_event_recorded")) != 0 {
		t.Fatal("older proof exported a fact")
	}
}

func TestEmployeeExecutionEventWaitsForSourceCommitAndRepairsLaunchFailure(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',error='runtime launch failed' WHERE id=$1::uuid`, f.queueID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 0 {
		t.Fatal("uncommitted Run result admitted", n, err)
	}
	if _, err := f.h.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET daemon_id=$2 WHERE id=(SELECT runtime_id FROM agent WHERE id=$1::uuid)`, f.agentID, "execution-test-"+f.agentID); err != nil {
		t.Fatal(err)
	}
	ready := employeeNoticeDatabase(t, "succeeded", false, false)
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending' WHERE id=$1::uuid`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if n, err := ready.h.ReconcileEmployeeExecutionEvents(ctx, 1); err != nil || n != 1 {
		t.Fatal("pending source starved a later terminal run", n, err)
	}
	var next int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, ready.runID).Scan(&next); err != nil || next != 1 {
		t.Fatal("later run was not consumed", next, err)
	}
	var skipped bool
	if err := testPool.QueryRow(ctx, `SELECT context ? 'employee_execution_event_skip' FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&skipped); err != nil || skipped {
		t.Fatal("recoverable source permanently skipped", skipped, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='completed' WHERE id=$1::uuid`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestEmployeeExecutionEventRollbackLeavesNoConsumptionOrSuccessTrace(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	client, exporter := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = client
	logs := captureNoticeLogs(t)
	if _, err := testPool.Exec(ctx, `CREATE FUNCTION execution_fact_test_reject() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.job_id IS NULL THEN RAISE EXCEPTION 'injected fact failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DROP FUNCTION execution_fact_test_reject() CASCADE`) })
	if _, err := testPool.Exec(ctx, `CREATE TRIGGER execution_fact_test_reject BEFORE INSERT ON employee_event_consumption FOR EACH ROW EXECUTE FUNCTION execution_fact_test_reject()`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err == nil || n != 0 {
		t.Fatal("failed transaction reported success", n, err)
	}
	var receipts int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("receipt escaped rollback", receipts, err)
	}
	if len(employeeTraceKind(exporter, "event")) != 0 || len(noticeLogEvents(t, logs, "employee_execution_event_recorded")) != 0 {
		t.Fatal("rollback recorded successful consumption")
	}
	if _, err := testPool.Exec(ctx, `DROP TRIGGER execution_fact_test_reject ON employee_event_consumption`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
		t.Fatal("restart failed to repair", n, err)
	}
}

func TestEmployeeExecutionEventPeriodicRecoveryDoesNotWakeModel(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx, cancel := context.WithCancel(context.Background())
	worker := f.h.EmployeeSceneWorker
	go worker.Run(ctx)
	t.Cleanup(func() {
		cancel()
		if !worker.WaitWithTimeout(3 * time.Second) {
			t.Error("worker did not stop")
		}
	})
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var count int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, f.runID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("existing recovery loop never admitted terminal fact")
		case <-tick.C:
		}
	}
	cancel()
	if !worker.WaitWithTimeout(3 * time.Second) {
		t.Fatal("worker did not stop")
	}
	if f.model.calls != 1 {
		t.Fatal("terminal recovery invoked model", f.model.calls)
	}
}

func TestEmployeeExecutionEventSkipCASPreservesConcurrentContextRepair(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=context-'employee_job_id' WHERE id=$1::uuid`, f.queueID); err != nil {
		t.Fatal(err)
	}
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT id FROM agent_task_queue WHERE id=$1::uuid FOR UPDATE`, f.queueID); err != nil {
		t.Fatal(err)
	}
	writer, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	h := *f.h
	h.TxStarter = writer
	finished := make(chan error, 1)
	go func() {
		n, err := h.ReconcileEmployeeExecutionEvents(ctx, 100)
		if err == nil && n != 0 {
			err = fmt.Errorf("stale skip committed: %d", n)
		}
		finished <- err
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids(pid))>0 FROM pg_stat_activity WHERE pid=$1`, writer.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-finished:
			t.Fatal("reconciler did not reach CAS", err)
		case <-deadline.C:
			t.Fatal("reconciler never blocked on queue CAS")
		case <-tick.C:
		}
	}
	if _, err := holder.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('employee_job_id',$2::text,'new_enrichment','kept') WHERE id=$1::uuid`, f.queueID, f.jobID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var preserved bool
	if err := testPool.QueryRow(ctx, `SELECT NOT(context ? 'employee_execution_event_skip') AND context->>'new_enrichment'='kept' FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&preserved); err != nil || !preserved {
		t.Fatal("CAS overwrote repaired context", preserved, err)
	}
	if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
		t.Fatal("repaired source was not admitted", n, err)
	}
}

func TestEmployeeExecutionEventDoesNotAcceptEnrichedSourceAsOriginal(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(fmt.Sprint(both), func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
				f.command.Event.Data.Messages[0].SenderUID = "same-sender"
				f.command.Event.Data.Messages[0].SenderOpenDingTalkID = "same-open"
				second := f.command.Event.Data.Messages[0]
				second.OpenMsgID = "message-2"
				second.Text = "Another request from the same sender"
				f.command.Event.Data.Messages = append(f.command.Event.Data.Messages, second)
			})
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_source_ref}',to_jsonb(split_part(context->>'employee_source_ref','/',1)||'/message-2')) WHERE id=$1::uuid`, f.queueID); err != nil {
				t.Fatal(err)
			}
			if both {
				if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,employee_source_ref}',context->'employee_source_ref') WHERE id=$1::uuid`, f.queueID); err != nil {
					t.Fatal(err)
				}
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			var reason string
			want := "execution_input_mismatch"
			if both {
				want = "source_dispatch_missing"
			}
			if err := testPool.QueryRow(ctx, `SELECT COALESCE(context->'employee_execution_event_skip'->>'reason','') FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&reason); err != nil || reason != want {
				t.Fatal("enriched source replaced original authorization", reason, err)
			}
		})
	}
}

func TestEmployeeExecutionEventRechecksGoalAfterWaitingForScene(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	request := employeeExecutionReplayRequest(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT id FROM agent_scene WHERE id=$1::uuid FOR UPDATE`, request.Task.Scope.Scene.SceneID); err != nil {
		t.Fatal(err)
	}
	writer, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	h := *f.h
	h.TxStarter = writer
	finished := make(chan error, 1)
	go func() { _, err := h.ReconcileEmployeeExecutionEvents(ctx, 100); finished <- err }()
	for {
		var blocked bool
		if err := testPool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids(pid))>0 FROM pg_stat_activity WHERE pid=$1`, writer.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-finished:
			t.Fatal("reconciler did not take scene lock", err)
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	definition := request.Task.Definition
	definition.Goal = "A new goal committed while the terminal consumer waited"
	if _, _, err := employeetask.NewStore(holder).AppendInput(ctx, request.Task.Scope, request.Task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, ActorRef: request.Task.RequesterRef, Body: "Correct goal", Correction: &definition, ExpectedVersion: request.Task.Version}); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := testPool.QueryRow(ctx, `SELECT c.reason FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id WHERE r.source='employee.execution' AND r.source_event_id=$1`, f.runID).Scan(&reason); err != nil || reason != "historical_goal_revision" {
		t.Fatal("late correction lost", reason, err)
	}
}

func TestEmployeeExecutionEventPreservesVerifiedFileSilence(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	ctx := context.Background()
	noticeReceipt(t, f, &fileNoticeProvider{file: true}, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
	before := employeeExecutionCounts(t, f.agentID)
	if _, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := testPool.QueryRow(ctx, `SELECT state,reason FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "suppressed" || reason != "native_file_delivered" || employeeExecutionCounts(t, f.agentID) != before || f.model.calls != 1 {
		t.Fatal("terminal fact changed verified file silence", state, reason)
	}
}
