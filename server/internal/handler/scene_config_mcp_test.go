package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// sceneConfigCall calls the config-qwen-tag-scene server at path as task
// (headers as the auth middleware stamps them for its task token).
func (f *ctxcapFixture) sceneConfigCall(t *testing.T, path string, task db.AgentTaskQueue, method string, params any) (int, map[string]any) {
	t.Helper()
	router := chi.NewRouter()
	router.Handle("/api/scene-config/mcp/{sceneToken}", http.HandlerFunc(f.h.SceneConfigMCP))
	r := mcpRequest(t, method, 1, params)
	r.URL.Path = path
	r.Header.Set("X-Workspace-ID", testWorkspaceID)
	r.Header.Set("X-Agent-ID", uuidToString(f.agent))
	r.Header.Set("X-Task-ID", uuidToString(task.ID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	return w.Code, decodeMCPResponse(t, w)
}

func (f *ctxcapFixture) sceneConfigTool(t *testing.T, path string, task db.AgentTaskQueue, tool string, args map[string]any) (map[string]any, string) {
	t.Helper()
	params := map[string]any{"name": tool}
	if args != nil {
		params["arguments"] = args
	}
	code, got := f.sceneConfigCall(t, path, task, "tools/call", params)
	if code != http.StatusOK {
		t.Fatalf("%s: status %d", tool, code)
	}
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %#v", tool, got)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("%s: isError result %q (refusals must be ok=false results the agent can read)", tool, text)
	}
	structured, _ := result["structuredContent"].(map[string]any)
	if ok, present := structured["ok"]; present && ok == false {
		return nil, fmt.Sprintf("%v: %v", structured["refused"], structured["message"])
	}
	return structured, ""
}

func (f *ctxcapFixture) sceneConfigPath(t *testing.T, task db.AgentTaskQueue) string {
	t.Helper()
	path, ok := f.h.sceneConfigMCPRoute(context.Background(), f.ws, task)
	if !ok {
		t.Fatal("task has no scene configuration route")
	}
	return path
}

// The route exists only for a task with a current scene, and carries a fresh
// signed token in its path; tasks without a scene get no server or skill.
func TestSceneConfigMCPRouteOnlyForATaskWithAScene(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	group := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff))
	path := f.sceneConfigPath(t, group)
	again := f.sceneConfigPath(t, group)
	if !strings.HasPrefix(path, protocol.SceneConfigMCPPathPrefix+"sct_") || path == again {
		t.Fatalf("route = %q / %q", path, again)
	}
	if protocol.RedactSceneConfigMCPPath(path) != protocol.SceneConfigMCPPathPrefix+"[redacted]" {
		t.Fatal("the route's token must be redacted from logs")
	}
	if !f.h.taskHasConfigScene(ctx, f.ws, group) {
		t.Fatal("a group task gets the skill")
	}
	for name, raw := range map[string][]byte{
		"no dispatch":   []byte(`{"issue_id":"x"}`),
		"unknown scene": ctxcapDispatch("group", "cidNotRegistered==", ctxcapStaff),
	} {
		task := f.task(t, raw)
		if _, ok := f.h.sceneConfigMCPRoute(ctx, f.ws, task); ok || f.h.taskHasConfigScene(ctx, f.ws, task) {
			t.Errorf("%s: mounted", name)
		}
	}
}

// Every call needs the token of the calling task, an active task, and the
// task's scene unchanged; the token alone or another task's token fails.
func TestSceneConfigMCPRefusesForeignOrStaleTokens(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	groupA := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff))
	groupB := f.task(t, ctxcapDispatch("group", ctxcapOtherScene, ctxcapStaff))
	pathA := f.sceneConfigPath(t, groupA)

	if code, _ := f.sceneConfigCall(t, pathA, groupB, "tools/list", nil); code != http.StatusForbidden {
		t.Fatalf("another task's token: status %d", code)
	}
	tampered := pathA[:len(pathA)-2] + "xx"
	if code, _ := f.sceneConfigCall(t, tampered, groupA, "tools/list", nil); code != http.StatusForbidden {
		t.Fatalf("tampered token: status %d", code)
	}
	code, list := f.sceneConfigCall(t, pathA, groupA, "tools/list", nil)
	if code != http.StatusOK {
		t.Fatalf("own token: status %d", code)
	}
	tools := list["result"].(map[string]any)["tools"].([]any)
	for _, tool := range tools {
		schema := tool.(map[string]any)["inputSchema"].(map[string]any)
		for property := range schema["properties"].(map[string]any) {
			if strings.Contains(property, "scene") || property == "scope" || property == "org_id" || property == "task_id" {
				t.Errorf("tool %v takes a scope argument %q", tool.(map[string]any)["name"], property)
			}
		}
	}

	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, uuidToString(groupA.ID)); err != nil {
		t.Fatal(err)
	}
	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolGet, nil); !strings.HasPrefix(refusal, "task_not_active") {
		t.Fatalf("ended task: %q", refusal)
	}
}

// The tools read and change the task's own scene only: a prompt written
// from group A lands in group A and is invisible from group B; unoffered
// items are refused.
func TestSceneConfigMCPToolsActOnTheTaskScene(t *testing.T) {
	f, _ := routineFixture(t)
	ctx := context.Background()
	agentID := uuidToString(f.agent)
	groupA := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff))
	groupB := f.task(t, ctxcapDispatch("group", ctxcapOtherScene, ctxcapStaff))
	pathA, pathB := f.sceneConfigPath(t, groupA), f.sceneConfigPath(t, groupB)

	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolPromptUpsert, map[string]any{"name": "Tone", "text": "Answer in short sentences."}); refusal != "" {
		t.Fatal(refusal)
	}
	stored, err := contextcap.ListPromptComponents(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene)
	if err != nil || len(stored) != 1 || stored[0].Name != "Tone" {
		t.Fatalf("group A prompts = %+v %v", stored, err)
	}
	other, _ := contextcap.ListPromptComponents(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapOtherScene)
	if len(other) != 0 {
		t.Fatalf("group B got group A's prompt: %+v", other)
	}
	got, refusal := f.sceneConfigTool(t, pathB, groupB, sceneConfigToolGet, nil)
	if refusal != "" || len(got["prompts"].([]any)) != 0 || got["scene"].(map[string]any)["scene_id"] != ctxcapOtherScene {
		t.Fatalf("group B view = %#v %q", got, refusal)
	}

	// A group adds and re-points remote MCP servers from the chat. Each
	// change notice names who asked and the masked address; stored headers
	// survive a switch; header values and URL secrets stay out of the chat.
	got, refusal = f.sceneConfigTool(t, pathA, groupA, sceneConfigToolMCPUpsert, map[string]any{
		"name": "docs", "url": "https://docs.safe.example.test/mcp?key=s3cret", "headers": map[string]string{"X-Team": "hidden-a"},
	})
	if refusal != "" || got["url"] != "https://docs.safe.example.test/mcp?…" {
		t.Fatalf("group add = %#v %q", got, refusal)
	}
	got, _ = f.sceneConfigTool(t, pathA, groupA, sceneConfigToolGet, nil)
	servers := got["mcp_servers"].([]any)
	if len(servers) != 1 || servers[0].(map[string]any)["name"] != "docs" || strings.Contains(sceneJSON(t, got), "hidden-a") || strings.Contains(sceneJSON(t, got), "s3cret") {
		t.Fatalf("group A servers (header values and url secrets must stay hidden) = %s", sceneJSON(t, got))
	}
	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolMCPUpsert, map[string]any{"name": "docs", "disabled": true}); refusal != "" {
		t.Fatal(refusal)
	}
	config, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene)
	if err != nil || !strings.Contains(string(config.MCPConfig), "hidden-a") || !strings.Contains(strings.ReplaceAll(string(config.MCPConfig), " ", ""), `"disabled":true`) {
		t.Fatalf("switched server lost its headers: %s %v", config.MCPConfig, err)
	}
	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolMCPUpsert, map[string]any{"name": "docs", "url": "https://elsewhere.example.test/mcp"}); refusal != "" {
		t.Fatal(refusal)
	}
	notices := sceneNoticeTexts(t, agentID)
	want := []string{
		"场域配置已更新（Alice 提出）：新增 MCP 服务器「docs」，地址 https://docs.safe.example.test/mcp?…",
		"停用 MCP 服务器「docs」（https://docs.safe.example.test/mcp?…）",
		"修改 MCP 服务器「docs」的地址：https://docs.safe.example.test/mcp?… → https://elsewhere.example.test/mcp",
	}
	for _, text := range want {
		if !strings.Contains(strings.Join(notices, "\n"), text) {
			t.Errorf("no notice %q in %q", text, notices)
		}
	}
	if joined := strings.Join(notices, "\n"); strings.Contains(joined, "s3cret") || strings.Contains(joined, "hidden-a") {
		t.Fatalf("a notice leaks a secret: %q", notices)
	}
	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolCapabilitySet, map[string]any{
		"kind": "connector", "id": f.notOffered, "enabled": true,
	}); !strings.HasPrefix(refusal, "not_offered") {
		t.Fatalf("unoffered connector: %q", refusal)
	}
	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolCapabilitySet, map[string]any{
		"kind": "connector", "id": f.scene, "enabled": false,
	}); refusal != "" {
		t.Fatal(refusal)
	}
	if _, refusal := f.sceneConfigTool(t, pathA, groupA, sceneConfigToolPromptDelete, map[string]any{"name": "Missing"}); !strings.HasPrefix(refusal, "prompt_not_found") {
		t.Fatalf("delete missing prompt: %q", refusal)
	}
}

// A routine run reads its scene but changes nothing: its input may come from
// a webhook.
func TestSceneConfigMCPRoutineRunIsReadOnly(t *testing.T) {
	f, a := routineFixture(t)
	created := createGroupRoutine(t, f, a, sceneRoutineInput{
		Title: "Digest", Instructions: "Digest.", Trigger: sceneRoutineTrigger{Kind: "webhook"},
	})
	ap, err := f.h.Queries.GetAutopilot(context.Background(), parseUUID(created.Routine.AutopilotID))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.h.RoutineRuntimeContext(context.Background(), ap)
	if err != nil {
		t.Fatal(err)
	}
	run := f.task(t, raw)
	path := f.sceneConfigPath(t, run)
	got, refusal := f.sceneConfigTool(t, path, run, sceneConfigToolGet, nil)
	if refusal != "" || got["read_only"] != true {
		t.Fatalf("routine run view = %#v %q", got, refusal)
	}
	if _, refusal := f.sceneConfigTool(t, path, run, sceneConfigToolPromptUpsert, map[string]any{"name": "x", "text": "y"}); !strings.HasPrefix(refusal, "routine_run_read_only") {
		t.Fatalf("write in a routine run: %q", refusal)
	}
	// Nor does it hand out access: no configuration link from either tool.
	if _, refusal := f.sceneConfigTool(t, path, run, sceneConfigToolConnectLink, nil); !strings.HasPrefix(refusal, "routine_run_read_only") {
		t.Fatalf("link in a routine run: %q", refusal)
	}
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, run, nil)); !isError || !strings.Contains(text, "routine run") {
		t.Fatalf("create_context_config_link in a routine run: isError=%v %q", isError, text)
	}
	routines, refusal := f.sceneConfigTool(t, path, run, sceneConfigToolRoutineList, nil)
	if refusal != "" || len(routines["routines"].([]any)) != 1 || strings.Contains(sceneJSON(t, routines), `"webhook_url":`) {
		t.Fatalf("routine list (no webhook token) = %s %q", sceneJSON(t, routines), refusal)
	}
}

// A routine created from a conversation is the agent's, keeps the full
// webhook URL out of the chat and is bound to the task's own scene.
func TestSceneConfigMCPCreatesARoutineInTheTaskScene(t *testing.T) {
	f, _ := routineFixture(t)
	group := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff))
	path := f.sceneConfigPath(t, group)
	got, refusal := f.sceneConfigTool(t, path, group, sceneConfigToolRoutineCreate, map[string]any{
		"title": "Deploy digest", "instructions": "Summarize the payload.", "trigger": map[string]any{"kind": "webhook"},
	})
	if refusal != "" {
		t.Fatal(refusal)
	}
	routine := got["routine"].(map[string]any)
	if routine["scene_id"] != ctxcapScene || routine["created_by_type"] != "agent" || strings.Contains(sceneJSON(t, got), `"webhook_url":`) {
		t.Fatalf("created = %s", sceneJSON(t, got))
	}
	// Anyone in the chat can ask for a run, so the chat keeps the minimum
	// interval between runs.
	if _, refusal := f.sceneConfigTool(t, path, group, sceneConfigToolRoutineRun, map[string]any{"routine_id": routine["id"]}); refusal != "" {
		t.Fatalf("first run from the chat: %q", refusal)
	}
	if _, refusal := f.sceneConfigTool(t, path, group, sceneConfigToolRoutineRun, map[string]any{"routine_id": routine["id"]}); !strings.HasPrefix(refusal, "routine_run_too_soon") {
		t.Fatalf("second run from the chat: %q", refusal)
	}
	if _, refusal := f.sceneConfigTool(t, path, group, sceneConfigToolRoutineUpdate, map[string]any{
		"routine_id": routine["id"], "enabled": false,
	}); refusal != "" {
		t.Fatal(refusal)
	}
	other := f.task(t, ctxcapDispatch("group", ctxcapOtherScene, ctxcapStaff))
	if _, refusal := f.sceneConfigTool(t, f.sceneConfigPath(t, other), other, sceneConfigToolRoutineDelete, map[string]any{
		"routine_id": routine["id"],
	}); !strings.HasPrefix(refusal, "routine_not_found") {
		t.Fatalf("another scene's routine: %q", refusal)
	}
}

// sceneNoticeTexts lists the texts of the scene change notices enqueued for
// the agent.
func sceneNoticeTexts(t *testing.T, agentID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT input->>'text' FROM response_action
		WHERE agent_id = $1 AND request_id LIKE 'scene-notice:%' ORDER BY created_at`, agentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, text)
	}
	return texts
}

// sceneConfigDMTask is a running task of Alice's 1:1 chat whose dispatch
// sender carries her openDingTalkId.
func (f *ctxcapFixture) sceneConfigDMTask(t *testing.T) db.AgentTaskQueue {
	t.Helper()
	var payload map[string]any
	_ = json.Unmarshal(ctxcapDispatch("single", ctxcapDirectScene, ctxcapStaff, ctxcapStaff), &payload)
	payload["dispatch_event_data"].(map[string]any)["sender"].(map[string]any)["openDingTalkId"] = "$:LWCP_v1:$alice"
	raw, _ := json.Marshal(payload)
	return f.task(t, raw)
}

// In a 1:1 chat the person adds and changes remote MCP servers; fields left
// out of a change (headers above all) keep their stored values, and
// reserved names or local servers are refused. A routine created there is
// bound to the 1:1 chat.
func TestSceneConfigMCPDMServersAndRoutines(t *testing.T) {
	f, _ := routineFixture(t)
	f.registerDirectScene(t)
	ctx := context.Background()
	dm := f.sceneConfigDMTask(t)
	path := f.sceneConfigPath(t, dm)
	if _, refusal := f.sceneConfigTool(t, path, dm, sceneConfigToolMCPUpsert, map[string]any{
		"name": "notes", "url": "https://notes.example.test/mcp", "headers": map[string]string{"X-Key": "hidden-k"},
	}); refusal != "" {
		t.Fatal(refusal)
	}
	got, refusal := f.sceneConfigTool(t, path, dm, sceneConfigToolMCPUpsert, map[string]any{"name": "notes", "disabled": true})
	if refusal != "" || got["disabled"] != true {
		t.Fatalf("switch off = %#v %q", got, refusal)
	}
	config, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, uuidToString(f.agent), contextcap.ScopeScene, ctxcapOrg, ctxcapDirectScene)
	if err != nil || !strings.Contains(string(config.MCPConfig), "hidden-k") || !strings.Contains(string(config.MCPConfig), "notes.example.test") {
		t.Fatalf("switched server lost its fields: %s %v", config.MCPConfig, err)
	}
	for name, args := range map[string]map[string]any{
		"reserved name": {"name": sceneConfigMCPServerName, "url": "https://docs.safe.example.test/mcp"},
		"local url":     {"name": "local", "url": "file:///bin/sh"},
		"no url":        {"name": "empty"},
	} {
		if _, refusal := f.sceneConfigTool(t, path, dm, sceneConfigToolMCPUpsert, args); !strings.HasPrefix(refusal, "invalid_mcp_config") {
			t.Errorf("%s: %q", name, refusal)
		}
	}
	created, refusal := f.sceneConfigTool(t, path, dm, sceneConfigToolRoutineCreate, map[string]any{
		"title": "Evening plan", "instructions": "Plan my evening.", "trigger": map[string]any{"kind": "schedule", "cron": "0 18 * * *"},
	})
	if refusal != "" || created["routine"].(map[string]any)["scene_id"] != ctxcapDirectScene {
		t.Fatalf("Alice's routine = %#v %q", created, refusal)
	}
}

func TestSceneConfigSkillAndReservedName(t *testing.T) {
	skill := service.SceneConfigSkill()
	if skill.Name != sceneConfigMCPServerName || !strings.Contains(skill.Content, "scene_config_get") || len(skill.Files) == 0 {
		t.Fatalf("skill = %+v", skill)
	}
	list := service.WithSceneConfigSkill([]service.AgentSkillData{{ID: "w1", Name: sceneConfigMCPServerName}, {ID: "w2", Name: "other"}})
	if len(list) != 2 || list[0].Name != "other" || list[1].ID != "" {
		t.Fatalf("the workspace skill of the same name must give way: %+v", list)
	}
	if _, reason := normalizeRemoteScopeMCPConfig(json.RawMessage(`{"mcpServers":{"config-qwen-tag-scene":{"url":"https://x.example.test/mcp"}}}`)); reason == "" {
		t.Fatal("the scene configuration server name must be reserved")
	}
}

func sceneJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
