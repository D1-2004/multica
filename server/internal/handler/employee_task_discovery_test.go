package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func employeeDiscoveryHost(t *testing.T, texts ...string) (employeeNoticeFixture, *employeeSceneHost, employeeloop.Identity, employeeSourceMessage) {
	t.Helper()
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	var endpoint, namespace string
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	f.h.EmployeeSceneWorker.TaskDiscoveryReady = func(context.Context) (bool, error) { return true, nil }
	message := f.command.Event.Data.Messages[0]
	message.OpenMsgID = "discovery-source"
	message.Text = "继续整理成表格"
	if len(texts) > 0 {
		message.Text = texts[0]
	}
	host, id, source := employeeMemoryHost(t, f.dingTalkResponseFixture, dc, []DispatchMessage{message})
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.CurrentTasks) != 0 || input.Input.TaskBrief != "" {
		t.Fatal("discovery input injected unrequested old task candidates or report")
	}
	if !strings.Contains(input.Config.Persona.DecisionRules, "explicit independent new Task") || strings.Index(employeeloop.BuildPrompt(input.Config.Persona), "REQUEST DECISION") > strings.Index(employeeloop.BuildPrompt(input.Config.Persona), "CONVERSATION STYLE") {
		t.Fatal("decision contract is not front-loaded")
	}
	find := false
	for _, tool := range input.Config.Tools {
		find = find || tool.Name == "find_tasks"
	}
	if !find {
		t.Fatal("new input has no native find_tasks")
	}
	raw, _ := json.Marshal(input)
	if _, err := host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	return f, host, id, source
}

func TestEmployeeTaskDiscoveryIndependentDispatch(t *testing.T) {
	f, host, id, source := employeeDiscoveryHost(t, "开独立新事项，实际运行Python计算，不复述旧结果")
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE employee_task SET definition=jsonb_build_object('goal',$2::text) WHERE agent_id=$1`, f.agentID, "实际运行Python计算"); err != nil {
		t.Fatal(err)
	}
	call := employeeloop.ToolCall{Name: "dispatch_task", NativeToolCallID: "new-independent", Arguments: map[string]any{"source_ref": source.SourceRef, "goal": "实际运行Python计算", "prompt": "实际运行Python计算1至5的平方并相加，不能口算", "reply": "我来运行，结果发你。"}}
	if result, err := host.Execute(ctx, id, call); err != nil || result.Receipt == "" {
		t.Fatal(result, err)
	}
	var tasks, runs, finds int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1),(SELECT count(*) FROM jsonb_each((SELECT tool_journal FROM employee_scene_job WHERE id=$2)) v WHERE v.value->'input'->>'name'='find_tasks')`, f.agentID, host.job.ID).Scan(&tasks, &runs, &finds); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 || runs != 2 || finds != 0 {
		t.Fatal("independent dispatch reused old task", tasks, runs, finds)
	}
	var prompt string
	if err := testPool.QueryRow(ctx, `SELECT q.context->>'direct_task_prompt' FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id WHERE r.agent_id=$1 AND r.id<>$2`, f.agentID, f.runID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "实际运行Python") || strings.Contains(prompt, "真实已存结果") {
		t.Fatal("new execution packet reused old result or lost method", prompt)
	}
}

func employeeFindCall(source employeeSourceMessage, id, query string) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "find_tasks", NativeToolCallID: id, Arguments: map[string]any{"source_ref": source.SourceRef, "query": query}}
}

func TestEmployeeTaskDiscoveryQuotedOwnMetadata(t *testing.T) {
	s, host, id, source := employeeQuoteHost(t, "succeeded", "notice", func(s *employeeQuoteSetup) {
		s.text = "继续整理成表格"
		s.notice.h.EmployeeSceneWorker.TaskDiscoveryReady = func(context.Context) (bool, error) { return true, nil }
	})
	ctx := context.Background()
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedInput
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.CurrentTasks) != 1 || saved.CurrentTasks[0].Ref != "q1" || saved.CurrentTasks[0].TaskID != employeeTaskOf(t, s) || strings.Contains(saved.Input.TaskBrief, "真实已存结果") || strings.Contains(saved.Input.TaskBrief, "result_report") {
		t.Fatal("new quote input lost exact own binding or injected report", saved.Input.TaskBrief, saved.CurrentTasks)
	}
	if _, err := host.Execute(ctx, id, employeeQuoteCall("read_task", "quote-read", source, "q1", nil)); err != nil {
		t.Fatal(err)
	}
	call := employeeQuoteCall("continue_task", "quote-continue", source, "q1", map[string]any{"read_ref": "quote-read", "instruction_quote": source.Message.Text, "prompt": "整理为表格", "reply": "我来继续整理。"})
	if result, err := host.Execute(ctx, id, call); err != nil || result.Receipt == "" {
		t.Fatal("discovery input cannot continue exact quoted own Task", result, err)
	}
}

func TestEmployeeTaskDiscoveryOwnRefAndReplay(t *testing.T) {
	f, host, id, source := employeeDiscoveryHost(t)
	ctx := context.Background()
	call := employeeFindCall(source, "discover-own", "feedback")
	first, err := host.Execute(ctx, id, call)
	if err != nil || !strings.Contains(first.Content, "discover-own:t1") || !strings.Contains(first.Content, "scene_and_sender") || strings.Contains(first.Content, "真实已存结果") {
		t.Fatal(first, err)
	}
	// A changed query must conflict rather than silently re-selecting a task.
	changed := employeeFindCall(source, call.NativeToolCallID, "different")
	if _, err := host.Execute(ctx, id, changed); err == nil {
		t.Fatal("changed find payload replayed")
	}
	replay, err := host.Execute(ctx, id, call)
	if err != nil || replay.Content != first.Content {
		t.Fatal("find replay changed frozen candidates", replay, err)
	}
	binding, err := host.currentTaskBinding(ctx, testPool, source, "discover-own:t1")
	if err != nil || binding.SharedReadOnly || binding.TaskID == "" {
		t.Fatal(binding, err)
	}
	foreign := source
	foreign.RequesterRef = "dingtalk:456:open_id:foreign"
	if _, err := host.currentTaskBinding(ctx, testPool, foreign, "discover-own:t1"); err == nil {
		t.Fatal("discovered ref crossed requester")
	}
	foreign = source
	foreign.SourceRef += "-other"
	if _, err := host.currentTaskBinding(ctx, testPool, foreign, "discover-own:t1"); err == nil {
		t.Fatal("discovered ref crossed source")
	}
	read := employeeloop.ToolCall{Name: "read_task", NativeToolCallID: "read-own", Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": "discover-own:t1"}}
	if result, err := host.Execute(ctx, id, read); err != nil || !strings.Contains(result.Content, "真实已存结果") {
		t.Fatal("own dynamic read did not reach report", result, err)
	}
	redo := employeeContinueCall(source)
	redo.Arguments["task_ref"], redo.Arguments["read_ref"] = "discover-own:t1", "read-own"
	if result, err := host.Execute(ctx, id, redo); err != nil || result.Receipt == "" {
		t.Fatal("dynamic own ref cannot continue", result, err)
	}
	var runs int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&runs); err != nil || runs != 2 {
		t.Fatal("continuation did not retain same task", runs, err)
	}
}

func TestEmployeeTaskDiscoverySharedReadOnly(t *testing.T) {
	f, host, id, source := employeeDiscoveryHost(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_scene SET scene_kind='group',kind_source='observed' WHERE id=$1;`, host.job.Scope.SceneID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_task SET requester_ref='foreign-person' WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	find := employeeFindCall(source, "discover-shared", "feedback")
	result, err := host.Execute(ctx, id, find)
	if err != nil || !strings.Contains(result.Content, "scene_shared_read_only") || !strings.Contains(result.Content, "Analyze feedback") {
		t.Fatal(result, err)
	}
	for _, forbidden := range []string{"真实已存结果", "foreign-person", "result_report", "prompt", "entries"} {
		if strings.Contains(result.Content, forbidden) {
			t.Fatal("shared metadata exposed material", forbidden, result.Content)
		}
	}
	ref := "discover-shared:t1"
	read := employeeloop.ToolCall{Name: "read_task", NativeToolCallID: "read-shared", Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": ref}}
	result, err = host.Execute(ctx, id, read)
	if err != nil || !strings.Contains(result.Content, `"shared_read_only":true`) || strings.Contains(result.Content, "真实已存结果") {
		t.Fatal(result, err)
	}
	for _, name := range []string{"read_task_history", "continue_task", "stop_task", "cancel_collection", "steer_task"} {
		call := employeeloop.ToolCall{Name: name, NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "task_ref": ref}}
		if name == "continue_task" {
			call = employeeContinueCall(source)
			call.Arguments["task_ref"], call.Arguments["read_ref"] = ref, "read-shared"
		}
		if name == "stop_task" || name == "cancel_collection" {
			call.Arguments["instruction_quote"], call.Arguments["read_ref"] = source.Message.Text, "read-shared"
			call.Arguments["reply"] = "停止"
		}
		if name == "steer_task" {
			call.Arguments["correction"], call.Arguments["reply"] = "调整", "调整"
		}
		if result, err := host.Execute(ctx, id, call); err == nil || result.Receipt != "" {
			t.Fatal("shared candidate gained control/material", name, result, err)
		}
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err := host.dispatchUpstream(ctx, tx, source, host.envelopes[0], employeeloop.ToolCall{Arguments: map[string]any{"builds_on": []string{ref}}}); err == nil {
		t.Fatal("shared candidate used as execution material")
	}
	if replay, err := host.Execute(ctx, id, find); err != nil || !strings.Contains(replay.Content, "discover-shared:t1") {
		t.Fatal("shared discovery replay failed", replay, err)
	}
}

func TestEmployeeTaskDiscoveryLayerIsolation(t *testing.T) {
	_, host, _, source := employeeDiscoveryHost(t)
	ctx := context.Background()
	store := employeetask.NewStore(testPool)
	if _, err := testPool.Exec(ctx, `UPDATE agent_scene SET scene_kind='group',kind_source='observed' WHERE id=$1`, host.job.Scope.SceneID); err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(ctx, employeetask.CreateParams{Scope: host.taskScope(), OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "foreign-person", Definition: employeetask.Definition{Goal: "shared blue"}, Source: employeetask.Source{Namespace: "test", Key: uuid.NewString()}, Input: "secret foreign material"})
	if err != nil {
		t.Fatal(err)
	}
	// Existing fixture own goal is unrelated to blue. New test task needs to be
	// older than the fixed wake watermark, just like production candidates.
	if _, err := testPool.Exec(ctx, `UPDATE employee_task SET created_at=$2 WHERE id=$1`, other.ID, host.job.CreatedAt); err != nil {
		t.Fatal(err)
	}
	rows, shared, err := store.Find(ctx, host.taskScope(), source.RequesterRef, "feedback", host.job.CreatedAt, 6)
	if err != nil || shared || len(rows) != 1 {
		t.Fatal("own relevant layer lost priority", rows, shared, err)
	}
	rows, shared, err = store.Find(ctx, host.taskScope(), source.RequesterRef, "blue", host.job.CreatedAt, 6)
	if err != nil || !shared || len(rows) != 1 || rows[0].ID != other.ID {
		t.Fatal("irrelevant own layer blocked shared match", rows, shared, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_scene SET scene_kind='dm' WHERE id=$1`, host.job.Scope.SceneID); err != nil {
		t.Fatal(err)
	}
	rows, shared, err = store.Find(ctx, host.taskScope(), source.RequesterRef, "blue", host.job.CreatedAt, 6)
	if err != nil || shared || len(rows) != 0 {
		t.Fatal("DM expanded requester", rows, shared, err)
	}
	if _, err := store.ReadSharedMetadata(ctx, host.taskScope(), source.RequesterRef, other.ID); err == nil {
		t.Fatal("shared ref remained readable in DM")
	}
	scope := host.taskScope()
	scope.TenantOrgID = "another-org"
	rows, shared, err = store.Find(ctx, scope, source.RequesterRef, "feedback", host.job.CreatedAt, 6)
	if err != nil || len(rows) != 0 || shared {
		t.Fatal("lookup crossed tenant org", rows, shared, err)
	}
	if _, err := store.ReadSharedMetadata(ctx, scope, source.RequesterRef, other.ID); err == nil {
		t.Fatal("shared lookup crossed tenant org")
	}
}
