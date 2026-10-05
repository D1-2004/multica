package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// Exercise the actual Direct admission, MCP mutation and terminal outbox.
// Model and DingTalk transport are fixtures; PostgreSQL effects are real.
func TestSceneConfigMCPDirectOnceCreationHasOnlyHostResult(t *testing.T) {
	f := employeeNoticeDatabase(t, "", false, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
		f.command.Event.Data.Conversation.Type = "single"
		f.command.Event.Data.Messages[0].Text = "5分钟后和我讲个笑话"
	})
	ctx := context.Background()
	f.h.AutopilotService = service.NewAutopilotService(f.h.Queries, testPool, f.h.Bus, f.h.TaskService)
	f.h.AutopilotService.SceneRoutines = f.h
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM context_scope_routine WHERE agent_id=$1::uuid`,
			`DELETE FROM autopilot_trigger WHERE autopilot_id IN (SELECT id FROM autopilot WHERE assignee_id=$1::uuid)`,
			`DELETE FROM autopilot_rule_version WHERE autopilot_id IN (SELECT id FROM autopilot WHERE assignee_id=$1::uuid)`,
			`DELETE FROM autopilot WHERE assignee_id=$1::uuid`,
		} {
			if _, err := testPool.Exec(ctx, statement, f.agentID); err != nil {
				t.Error(err)
			}
		}
	})
	queue, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.queueID))
	if err != nil {
		t.Fatal(err)
	}
	mcp := &ctxcapFixture{h: f.h, ws: parseUUID(testWorkspaceID), agent: parseUUID(f.agentID)}
	path := mcp.sceneConfigPath(t, queue)
	acceptedNotices := len(sceneNoticeTexts(t, f.agentID))
	args := map[string]any{
		"title": "讲一个笑话", "instructions": "在原单聊讲一个笑话。",
		"trigger": map[string]any{"kind": "once", "run_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)},
	}
	created, refusal := mcp.sceneConfigTool(t, path, queue, sceneConfigToolRoutineCreate, args)
	if refusal != "" {
		t.Fatal(refusal)
	}
	id := created["routine"].(map[string]any)["id"].(string)
	replayed, refusal := mcp.sceneConfigTool(t, path, queue, sceneConfigToolRoutineCreate, args)
	if refusal != "" || replayed["routine"].(map[string]any)["id"] != id || replayed["updated"] != true {
		t.Fatalf("creation replay duplicated the resource: %#v %q", replayed, refusal)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM context_scope_routine WHERE agent_id=$1::uuid`, f.agentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("routine count = %d: %v", count, err)
	}
	if notices := sceneNoticeTexts(t, f.agentID); len(notices) != acceptedNotices {
		t.Fatalf("Direct once creation emitted extra configuration replies: %v", notices)
	}
	// Security change notices must remain independent of final result ownership.
	if _, refusal := mcp.sceneConfigTool(t, path, queue, sceneConfigToolMCPUpsert, map[string]any{
		"name": "docs", "url": "https://docs.example.test/mcp?key=private-key",
	}); refusal != "" {
		t.Fatal(refusal)
	}
	if notices := sceneNoticeTexts(t, f.agentID); len(notices) != acceptedNotices+1 || !strings.Contains(notices[len(notices)-1], "MCP") || strings.Contains(notices[len(notices)-1], "private-key") {
		t.Fatalf("remote MCP security notice lost or leaked secrets: %v", notices)
	}
	const result = "定时任务已建好：到点讲一个笑话，一次性。"
	output, _ := json.Marshal(map[string]string{"output": result})
	if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(f.queueID), output, "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := reconcileEmployeeNotice(t, f.h); err != nil {
			t.Fatal(err)
		}
	}
	var body, state string
	if err := testPool.QueryRow(ctx, `SELECT n.body,n.state,count(*) OVER () FROM employee_run_notice n JOIN response_action a ON a.id=n.action_id WHERE n.run_id=$1::uuid`, f.runID).Scan(&body, &state, &count); err != nil || count != 1 || body != result || state != "enqueued" {
		t.Fatalf("final Host result = %q %q count=%d: %v", body, state, count, err)
	}
}

func TestSceneConfigMCPOrdinaryOnceCreationKeepsChangeNotice(t *testing.T) {
	x := newEmployeeRoutineHandlerFixture(t, "fixture")
	f := x.f
	var payload map[string]any
	if err := json.Unmarshal(ctxcapDispatch("group", ctxcapScene, ctxcapStaff), &payload); err != nil {
		t.Fatal(err)
	}
	payload["dispatch_context_prompt"] = "Arrange a one-shot reminder in this scene."
	raw, _ := json.Marshal(payload)
	queue := f.task(t, raw)
	if _, refusal := f.sceneConfigTool(t, f.sceneConfigPath(t, queue), queue, sceneConfigToolRoutineCreate, map[string]any{
		"title": "Legacy reminder", "instructions": "Remind here once.",
		"trigger": map[string]any{"kind": "once", "run_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)},
	}); refusal != "" {
		t.Fatal(refusal)
	}
	if notices := sceneNoticeTexts(t, uuidToString(f.agent)); len(notices) != 1 || !strings.Contains(notices[0], "Legacy reminder") {
		t.Fatalf("ordinary task lost its change notice: %v", notices)
	}
}
