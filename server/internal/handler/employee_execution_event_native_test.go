package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/dws"
)

// Use the actual native envelope decoder, account ownership, endpoint principal,
// default legacy admission, and Employee dispatch journal. Do not fabricate a
// unified receipt: routing and the selected work owner are independent.
func TestEmployeeExecutionEventNativeLegacyAdmissionReassessesV1Skip(t *testing.T) {
	for _, tc := range []struct {
		name           string
		valid, oldSkip bool
		rejectReason   string
	}{
		{"fresh_native_legacy", true, false, ""},
		{"reassess_v1_native_legacy", true, true, ""},
		{"invalid_journal_still_rejected", false, true, "source_dispatch_missing"},
		{"unmapped_source_still_rejected", false, true, "source_receipt_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeDBFixture(t)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `UPDATE agent SET coordination_mode='employee' WHERE id=$1::uuid`, f.agentID); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode='local',provider='codex',metadata='{"client_capabilities":["employee-direct-v1","dws_message_policy_v1"]}'::jsonb WHERE id=(SELECT runtime_id FROM agent WHERE id=$1::uuid)`, f.agentID); err != nil {
				t.Fatal(err)
			}
			f.h.EventRouteConfig = nil
			f.h.EmployeeLoopReady = func(context.Context, pgtype.UUID, pgtype.UUID) error { return nil }
			f.h.TaskService = &service.TaskService{Queries: f.h.Queries, TxStarter: testPool, Bus: f.h.Bus}
			model := &employeeTestModel{dispatch: true}
			f.h.EmployeeSceneWorker = NewEmployeeSceneWorker(f.h, model)
			f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
			t.Cleanup(func() {
				for _, table := range []string{"employee_run_notice", "employee_event_consumption", "employee_scene_job", "employee_task_entry", "employee_task_run", "employee_task", "scene_event_receipt", "agent_scene"} {
					if _, err := testPool.Exec(ctx, `DELETE FROM `+table+` WHERE agent_id=$1::uuid`, f.agentID); err != nil {
						t.Error(err)
					}
				}
			})
			line := nativeTestEventLine(dws.EventIMAllSingleChats, "cid-native-execution-"+uuid.NewString(), "msg-1", "")
			if err := f.h.HandleDWSNativeEvent(ctx, f.identity, line); err != nil {
				t.Fatal(err)
			}
			var jobID string
			var items []employeeentry.Item
			if err := testPool.QueryRow(ctx, `SELECT id::text,items FROM employee_scene_job WHERE agent_id=$1::uuid`, f.agentID).Scan(&jobID, &items); err != nil {
				t.Fatal(err)
			}
			var route, state, source string
			if err := testPool.QueryRow(ctx, `SELECT route,state,source FROM scene_event_receipt WHERE id=$1::uuid`, items[0].ReceiptID).Scan(&route, &state, &source); err != nil {
				t.Fatal(err)
			}
			if route != eventrouter.Legacy || state != eventrouter.Legacy || !strings.HasPrefix(source, "dws-native/") {
				t.Fatal("fixture did not exercise native legacy admission", route, state, source)
			}
			model.sourceRef = items[0].ReceiptID + "/msg-1"
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
			var runID, queueID string
			if err := testPool.QueryRow(ctx, `SELECT id::text,queue_task_id::text FROM employee_task_run WHERE agent_id=$1::uuid`, f.agentID).Scan(&runID, &queueID); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running' WHERE id=$1::uuid`, queueID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(queueID), []byte(`{"output":"NATIVE_EXECUTION_RESULT"}`), "", "", false, ""); err != nil {
				t.Fatal(err)
			}
			// Version 1 incorrectly classified all valid legacy receipts this way.
			if tc.oldSkip {
				if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('employee_execution_event_skip',jsonb_build_object('version',1,'run_id',$2::text,'reason','source_receipt_mismatch')) WHERE id=$1::uuid`, queueID, runID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rejectReason == "source_dispatch_missing" {
				if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET tool_journal='{}' WHERE id=$1::uuid`, jobID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rejectReason == "source_receipt_mismatch" {
				if _, err := testPool.Exec(ctx, `UPDATE scene_event_receipt SET route='unified',state='unmapped',scene_id=NULL,reason='missing_locator' WHERE id=$1::uuid`, items[0].ReceiptID); err != nil {
					t.Fatal(err)
				}
			}
			client, exporter := employeeTraceClient(t)
			f.h.EmployeeSceneWorker.Langfuse = client
			before := employeeExecutionCounts(t, f.agentID)
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 1 {
				t.Fatal("v1 skip was not re-evaluated", n, err)
			}
			if got := employeeExecutionCounts(t, f.agentID); got != before || model.calls != 1 {
				t.Fatal("fact recovery created work", before, got, model.calls)
			}
			var count int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE source='employee.execution' AND source_event_id=$1`, runID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if tc.valid {
				if count != 1 {
					t.Fatal("verified native legacy source produced no execution fact", count)
				}
				var consumed string
				var noJob bool
				var raw []byte
				if err := testPool.QueryRow(ctx, `SELECT r.route,r.state,r.envelope,c.state,c.job_id IS NULL FROM scene_event_receipt r JOIN employee_event_consumption c ON c.receipt_id=r.id WHERE r.source='employee.execution' AND r.source_event_id=$1`, runID).Scan(&route, &state, &raw, &consumed, &noJob); err != nil {
					t.Fatal(err)
				}
				var ev eventrouter.Event
				if err := json.Unmarshal(raw, &ev); err != nil {
					t.Fatal(err)
				}
				if count != 1 || route != eventrouter.Legacy || state != eventrouter.Legacy || consumed != "completed" || !noJob || ev.Category != eventrouter.RunCallback || len(employeeExecutionTraceEvents(exporter)) != 1 {
					t.Fatal("legacy work ownership or fact trace was lost", count, route, state, consumed, noJob)
				}
			} else {
				var version, proofVersion int
				var reason string
				var oldReaderSkips bool
				if err := testPool.QueryRow(ctx, `SELECT (context->'employee_execution_event_skip'->>'version')::int,(context->'employee_execution_event_skip'->>'proof_version')::int,context->'employee_execution_event_skip'->>'reason',COALESCE(context->'employee_execution_event_skip' @> jsonb_build_object('version',1,'run_id',$2::text) AND context->'employee_execution_event_skip'->>'reason'<>'',false) FROM agent_task_queue WHERE id=$1::uuid`, queueID, runID).Scan(&version, &proofVersion, &reason, &oldReaderSkips); err != nil {
					t.Fatal(err)
				}
				if count != 0 || version != 1 || proofVersion != 2 || !oldReaderSkips || reason != tc.rejectReason || len(employeeExecutionTraceEvents(exporter)) != 0 {
					t.Fatal("legacy route bypassed provenance or old reader can downgrade proof", count, version, proofVersion, oldReaderSkips, reason)
				}
			}
			if n, err := f.h.ReconcileEmployeeExecutionEvents(ctx, 100); err != nil || n != 0 {
				t.Fatal("replay repeated disposition", n, err)
			}
		})
	}
}
