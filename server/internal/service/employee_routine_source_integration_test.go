package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
)

// This exercises durable Host state and admission; provider delivery and an LLM
// are outside this local database test's boundary.
func TestEmployeeOnceSourceSurvivesCompletedOriginalRun(t *testing.T) {
	f := newEmployeeRoutineFixture(t)
	ctx := context.Background()
	scope := employeetask.Scope{WorkspaceID: f.ws, AgentID: f.agent, TenantOrgID: f.org, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: f.sceneID}}
	original, err := employeetask.NewStore(f.pool).Create(ctx, employeetask.CreateParams{
		Scope: scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect,
		RequesterRef: "dingtalk:staff:dx", Definition: employeetask.Definition{Goal: "提醒我查看客户摘要"}, Source: employeetask.Source{Namespace: "source-test", Key: uuid.NewString()}, Input: "Original human request",
	})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := employeetask.Compile(employeetask.CompileInput{Scope: scope, PrincipalID: f.agent, Definition: original.Definition,
		Source:     employeetask.PacketMaterial{Ref: "message:m-source", Scope: scope, PrincipalID: f.agent, Body: "冬翔: 15 分钟后提醒我看这份客户摘要"},
		History:    employeetask.PacketHistory{State: employeetask.HistoryAvailable, Items: []employeetask.PacketMaterial{{Ref: "history:h1", Scope: scope, PrincipalID: f.agent, Body: "Earlier history: customer A needs a follow-up"}}},
		References: []employeetask.PacketMaterial{{Ref: "result:summary1", Scope: scope, PrincipalID: f.agent, Body: "Upstream result: selected customer summary"}}, ReturnAddress: "scene:" + f.sceneID,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch, _ := json.Marshal(map[string]any{"employee_source_ref": "m-source", "external_identity": map[string]any{"dws": map[string]string{"orgId": f.org}}, "dispatch_event_data": map[string]any{
		"sender":   map[string]string{"staffId": "dx", "displayName": "冬翔", "openDingTalkId": "odt-dx"},
		"messages": []map[string]any{{"openMsgId": "m-source", "occurredAt": 1791096000123, "text": "15 分钟后提醒我看这份客户摘要", "senderStaffId": "dx", "senderOpenDingTalkId": "odt-dx"}},
	}, "agent_identity_context_token": "ORIGINAL_TOKEN_NEVER_PERSIST", "runtime_mcp_overlay": map[string]string{"credential": "ORIGINAL_SECRET_NEVER_PERSIST"}})
	queueID := uuid.NewString()
	queueContext, err := directTaskContext(DirectTaskRequest{Task: original, Prompt: packet.Text, PrincipalID: f.uuid(f.agent), Context: dispatch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context) VALUES($1::uuid,$2::uuid,$3::uuid,'queued',$4)`, queueID, f.agent, f.runtime, queueContext); err != nil {
		t.Fatal(err)
	}
	originalRun, err := employeetask.NewStore(f.pool).StartRun(ctx, scope, original.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "source-test", Key: queueID}, QueueTaskID: queueID, ExpectedVersion: original.Version})
	if err != nil {
		t.Fatal(err)
	}
	source, err := CaptureRoutineSource(ctx, f.pool, f.queue(t, queueID), f.ws, f.agent, f.org, f.sceneID)
	if err != nil {
		t.Fatal(err)
	}
	if source.EmployeeRunID != originalRun.ID || source.RequesterRef != original.RequesterRef || len(source.Messages) != 1 || source.Messages[0].OccurredAt != 1791096000123 {
		t.Fatalf("capture lost original: %+v", source)
	}
	// Persist the source via the ordinary insert, then finish the original work.
	if err = contextcap.DeleteRoutine(ctx, f.pool, f.routine.ID); err != nil {
		t.Fatal(err)
	}
	replacement := f.routine
	replacement.Source = source
	replacement.CreatedTaskID = queueID
	f.routine, err = contextcap.InsertRoutine(ctx, f.pool, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE context_scope_routine SET source='{}'::jsonb WHERE id=$1::uuid`, f.routine.ID); err == nil || !strings.Contains(err.Error(), "source is immutable") {
		t.Fatalf("immutable trigger failed: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, queueID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task_run SET state='succeeded',finished_at=now() WHERE id=$1::uuid`, originalRun.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET state='succeeded',active_run_id=NULL WHERE id=$1::uuid`, original.ID); err != nil {
		t.Fatal(err)
	}
	at := f.slot(0)
	if _, err = f.pool.Exec(ctx, `UPDATE autopilot_trigger SET kind='once',cron_expression=NULL,run_at=$2,next_run_at=$2 WHERE id=$1`, f.trigger.ID, at); err != nil {
		t.Fatal(err)
	}
	run := f.fire(t, at)
	occurrence := f.occurrence(t, run.ID)
	if occurrence.State != "accepted" {
		t.Fatalf("completed original blocked once: %+v", occurrence)
	}
	queue := f.queue(t, occurrence.QueueID)
	direct, ok := ParseDirectTaskContext(queue)
	if !ok || direct.AutomationOrigin == nil || direct.AutomationOrigin.ReceiptID != occurrence.ID {
		t.Fatalf("invalid fresh direct origin: %+v", direct)
	}
	for _, keep := range []string{"冬翔", "15 分钟后提醒我看这份客户摘要", "Earlier history: customer A", "Upstream result: selected customer summary", queueID, original.ID, originalRun.ID, "dingtalk:staff:dx"} {
		if !strings.Contains(direct.Prompt, keep) {
			t.Fatalf("new packet lost %q: %s", keep, direct.Prompt)
		}
	}
	var input routineOccurrenceInput
	if err = json.Unmarshal(occurrence.Input, &input); err != nil {
		t.Fatal(err)
	}
	if input.SourceContext == nil || input.SourceContext.Messages[0].OccurredAt != 1791096000123 || input.SourceContext.EmployeeRunID != originalRun.ID {
		t.Fatalf("receipt lost source: %+v", input)
	}
	for _, bad := range []string{"ORIGINAL_TOKEN_NEVER_PERSIST", "ORIGINAL_SECRET_NEVER_PERSIST", "runtime_mcp_overlay", "agent_identity_context_token"} {
		if strings.Contains(string(queue.Context), bad) || strings.Contains(string(occurrence.Input), bad) {
			t.Fatalf("inherited secret %q", bad)
		}
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(queue.Context, &top)
	if top["dispatch_event_data"] != nil {
		t.Fatal("restored dispatch would inherit personal capabilities")
	}
	sc := contextcap.ScopeFromTaskContext(queue.Context)
	if sc.SceneID != f.sceneID || sc.DispatchOrgID != f.org || sc.PersonKey != "" {
		t.Fatalf("new run scope changed: %+v", sc)
	}
	origin, err := LoadAutomationOrigin(ctx, f.pool, queue)
	if err != nil || origin.Scope().Scene.SceneID != f.sceneID || origin.RunID() != occurrence.RunID {
		t.Fatalf("origin detached: %+v %v", origin, err)
	}
	loaded, err := contextcap.GetRoutine(ctx, f.pool, f.ws, f.agent, f.routine.ID)
	if err != nil || loaded.Source == nil || loaded.Source.QueueTaskID != util.UUIDToString(f.uuid(queueID)) {
		t.Fatalf("source roundtrip failed: %+v %v", loaded, err)
	}
}
