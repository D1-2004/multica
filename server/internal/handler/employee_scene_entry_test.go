package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

type employeeTestModel struct {
	calls            int
	sourceRef        string
	dispatch         bool
	partial          bool
	quiet            bool
	definition       *employeetask.Definition
	completionNotice map[string]any
	buildsOn         []string
}

func (m *employeeTestModel) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	message := map[string]any{"role": "assistant", "content": "在，需要我帮你做什么？"}
	finish := "stop"
	if m.dispatch {
		arguments := map[string]any{"goal": "Analyze feedback", "prompt": "Analyze feedback and report the evidence", "reply": "我来分析这些反馈。", "source_ref": m.sourceRef}
		if m.completionNotice != nil {
			arguments["completion_notice_policy"] = m.completionNotice
		}
		if m.buildsOn != nil {
			arguments["builds_on"] = m.buildsOn
		}
		if m.definition != nil {
			arguments["deliverables"] = m.definition.Deliverables
			arguments["success_criteria"] = m.definition.SuccessCriteria
			arguments["access_needed"] = m.definition.AccessNeeded
		}
		args, _ := json.Marshal(arguments)
		calls := []any{map[string]any{"id": "call-first", "type": "function", "function": map[string]any{"name": "dispatch_task", "arguments": string(args)}}}
		if m.partial {
			bad, _ := json.Marshal(map[string]any{"goal": " ", "prompt": "missing real goal", "reply": "Second accepted", "source_ref": m.sourceRef})
			calls = append(calls, map[string]any{"id": "call-second", "type": "function", "function": map[string]any{"name": "dispatch_task", "arguments": string(bad)}})
		}
		message = map[string]any{"role": "assistant", "tool_calls": calls}
		finish = "tool_calls"
	}
	if m.quiet {
		message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-quiet", "type": "function", "function": map[string]any{"name": "stay_quiet", "arguments": "{}"}}}}
		finish = "tool_calls"
	}
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}}})
	var result openai.ChatCompletion
	err := json.Unmarshal(raw, &result)
	return &result, err
}
func employeeFixture(t *testing.T) (*dingTalkResponseFixture, *employeeTestModel, agentDispatchContext) {
	t.Helper()
	f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET coordination_mode='employee',inbound_coordinator=true WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET runtime_mode='local',provider='codex',daemon_id='employee-test-daemon',metadata='{"client_capabilities":["employee-direct-v1"]}'::jsonb WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)`, f.agentID); err != nil {
		t.Fatal(err)
	}
	f.h.EventRouteConfig = func(string, string, string) (string, string) { return eventrouter.Unified, "test" }
	f.h.EmployeeLoopReady = func(context.Context, pgtype.UUID, pgtype.UUID) error { return nil }
	f.h.TaskService = &service.TaskService{Queries: f.h.Queries, TxStarter: testPool, Bus: f.h.Bus}
	model := &employeeTestModel{}
	f.h.EmployeeSceneWorker = NewEmployeeSceneWorker(f.h, model)
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	dc := agentDispatchContext{EndpointID: "employee-test", EndpointNamespaceID: parseUUID(uuid.NewString()), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	t.Cleanup(func() {
		for _, table := range []string{"employee_event_consumption", "employee_scene_job", "employee_task_run", "employee_task_entry", "employee_task", "scene_event_receipt", "agent_scene"} {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1`, f.agentID); err != nil {
				t.Error(err)
			}
		}
	})
	return f, model, dc
}
func employeeHTTP(t *testing.T, f *dingTalkResponseFixture, dc agentDispatchContext, key string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(f.command)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dispatch", strings.NewReader(string(raw)))
	req.Header.Set("Idempotency-Key", key)
	result := httptest.NewRecorder()
	f.h.handleAgentDispatchV2(result, req, raw, dc)
	return result
}
func TestEmployeeSceneCallbacklessAdmissionIsAsyncAndSticky(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	key := uuid.NewString()
	response := employeeHTTP(t, f, dc, key)
	if response.Code != http.StatusAccepted {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	if model.calls != 0 {
		t.Fatal("HTTP invoked model")
	}
	var id, owner string
	if err := testPool.QueryRow(context.Background(), `SELECT job_id::text,owner_loop FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&id, &owner); err != nil {
		t.Fatal(err)
	}
	if owner != "employee" || id == "" {
		t.Fatalf("owner=%s job=%s", owner, id)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	f.h.TaskCompletionTargetIdentity = "router-target:v1:sha256:" + strings.Repeat("b", 64)
	response = employeeHTTP(t, f, dc, key)
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay jobs=%d %v", count, err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM inbound_coordinator_job WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old loop also accepted: %d %v", count, err)
	}
}
func TestEmployeeSceneReplyCheckpointAndOutbox(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worker: %v %v", worked, err)
	}
	var state string
	var outcome []byte
	if err := testPool.QueryRow(context.Background(), `SELECT state,outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || !strings.Contains(string(outcome), "在，需要") || model.calls != 1 {
		t.Fatalf("checkpoint=%s %s model=%d", state, outcome, model.calls)
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("outbox=%d %v", count, err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET state='pending',available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("recovery: %v %v", worked, err)
	}
	if model.calls != 1 {
		t.Fatal("persisted outcome reran model")
	}
}
func TestEmployeeSceneDispatchUsesFrozenActorAndRecoversModelJournal(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.dispatch = true
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/message-1"
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("dispatch worker: %v %v", worked, err)
	}
	var requester string
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT requester_ref FROM employee_task WHERE agent_id=$1`, f.agentID).Scan(&requester); err != nil {
		t.Fatal(err)
	}
	if requester != "dingtalk:456:open_id:requester-open-id" {
		t.Fatalf("requester borrowed endpoint actor: %q", requester)
	}
	if model.calls != 1 {
		t.Fatalf("dispatch required %d calls", model.calls)
	}
	var queueContext []byte
	if err := testPool.QueryRow(context.Background(), `SELECT q.context FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.agent_id=$1`, f.agentID).Scan(&queueContext); err != nil {
		t.Fatal(err)
	}
	var compiled struct {
		Prompt      string   `json:"direct_task_prompt"`
		ContextUsed []string `json:"employee_context_used"`
	}
	if err := json.Unmarshal(queueContext, &compiled); err != nil {
		t.Fatal(err)
	}
	if len(compiled.ContextUsed) != 1 || compiled.ContextUsed[0] != model.sourceRef || !strings.Contains(compiled.Prompt, "Summarize the findings") {
		t.Fatalf("execution lost original source/manifest: refs=%v prompt=%q", compiled.ContextUsed, compiled.Prompt)
	}
	// Simulate a restart after the effect committed but before its outcome was saved.
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("effect recovery: %v %v", worked, err)
	}
	if model.calls != 1 {
		t.Fatal("recovery regenerated native tool IDs")
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate runs=%d %v", count, err)
	}
	var body string
	if err := testPool.QueryRow(context.Background(), `SELECT body FROM employee_task_entry WHERE agent_id=$1 AND kind='request'`, f.agentID).Scan(&body); err != nil || !strings.Contains(body, "message-1") {
		t.Fatalf("request evidence=%q %v", body, err)
	}
}

func TestEmployeeSceneTaskReadNeedsRequesterAndExplicitAnchor(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	job, err := f.h.EmployeeSceneWorker.store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env employeeDispatchEnvelope
	if err = json.Unmarshal(job.Items[0].Payload, &env); err != nil {
		t.Fatal(err)
	}
	source := employeeSourceMessages(job.Items[0], env)[0]
	host := employeeSceneHost{worker: f.h.EmployeeSceneWorker, job: job, envelopes: []employeeDispatchEnvelope{env}}
	task, err := employeetask.NewStore(testPool).Create(ctx, employeetask.CreateParams{Scope: host.taskScope(), OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: source.RequesterRef, Definition: employeetask.Definition{Goal: "Private goal"}, Source: employeetask.Source{Namespace: "test", Key: "private"}, Input: "Private request"})
	if err != nil {
		t.Fatal(err)
	}
	call := employeeloop.ToolCall{Name: "read_task", Arguments: map[string]any{"task_id": task.ID}}
	if _, err = host.read(ctx, source, call); err == nil {
		t.Fatal("a model-named task ID became read authority")
	}
	source.Message.Text = "Show my task " + task.ID
	if result, err := host.read(ctx, source, call); err != nil || !strings.Contains(result.Content, "Private goal") {
		t.Fatalf("explicit owner read: %s %v", result.Content, err)
	}
	source.RequesterRef = "dingtalk:456:open_id:other-person"
	if _, err = host.read(ctx, source, call); err == nil {
		t.Fatal("another requester read private task contents")
	}
}

func TestEmployeeSceneRuntimeReadinessRequiresAuthenticatedCapability(t *testing.T) {
	f, _, dc := employeeFixture(t)
	configureEmployeeReadyDependencies(f)
	ctx := context.Background()
	if err := f.h.EmployeeSceneWorker.Ready(ctx, dc.WorkspaceID, dc.AgentID); err != nil {
		t.Fatalf("verified runtime rejected: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET metadata='{}'::jsonb WHERE id=(SELECT runtime_id FROM agent WHERE id=$1)`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.EmployeeSceneWorker.Ready(ctx, dc.WorkspaceID, dc.AgentID); err == nil {
		t.Fatal("online-only runtime was treated as Direct-capable")
	}
}

func TestEmployeeSceneUnsupportedCalendarIsHeldWithoutFallback(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	start := int64(1700000000)
	f.command.Event.Domain = "calendar"
	f.command.Event.Type = "calendar.started"
	f.command.Event.Data.CalendarID = "calendar-fixture"
	f.command.Event.Data.Subject = "Calendar trigger"
	f.command.Event.Data.StartTime = &start
	f.command.Event.Data.AIReadableContent = "Calendar trigger"
	f.command.Event.Data.Messages = nil
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "unsupported_source") {
		t.Fatalf("unsupported source: %d %s", response.Code, response.Body.String())
	}
	var owner, state, reason string
	if err := testPool.QueryRow(context.Background(), `SELECT owner_loop,state,reason FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&owner, &state, &reason); err != nil {
		t.Fatal(err)
	}
	if owner != "employee" || state != "held" || reason != "unsupported_source" || model.calls != 0 {
		t.Fatalf("held=%s/%s/%s model=%d", owner, state, reason, model.calls)
	}
	var jobs int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("unsupported event queued: %d %v", jobs, err)
	}
}

func TestEmployeeSceneArchiveStopsPendingJobBeforeModel(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET archived_at=now() WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("archive handling: %v %v", worked, err)
	}
	var state, reason string
	if err := testPool.QueryRow(context.Background(), `SELECT state,reason FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "held" || reason != "agent_archived" || model.calls != 0 {
		t.Fatalf("archived employee ran: %s %s calls=%d", state, reason, model.calls)
	}
}

func TestEmployeeScenePartialDispatchKeepsBothToolOutcomes(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.dispatch = true
	model.partial = true
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/message-1"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw, toolJournal json.RawMessage
	if err := testPool.QueryRow(ctx, `SELECT outcome,tool_journal FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw, &toolJournal); err != nil {
		t.Fatal(err)
	}
	var outcome employeeSavedOutcome
	if err := json.Unmarshal(raw, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Failure == "" || len(outcome.Outcome.Receipts) != 1 || !strings.Contains(outcome.Outcome.Reply, "其余请求暂未完成受理") {
		t.Fatalf("partial batch misreported: %s", raw)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(toolJournal, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("lost accepted/failed tool outcomes: %s", toolJournal)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("partial recovery reran model %d times", model.calls)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed committed effect: %d %v", count, err)
	}
}

func TestEmployeeSceneReplicaGatePrecedesBusinessOwnerFreeze(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return errors.New("old replica") }
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("mixed replicas admitted work: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 0 || model.calls != 0 {
		t.Fatalf("mixed replica side effects=%d model=%d %v", count, model.calls, err)
	}
}

func TestEmployeeSceneBuilderCarriesRoleAndSkillCatalog(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	const instructions = "Analyze customer feedback. Do not send results outside the source conversation."
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions=$2 WHERE id=$1`, f.agentID, instructions); err != nil {
		t.Fatal(err)
	}
	var skillID string
	if err := testPool.QueryRow(ctx, `INSERT INTO skill(workspace_id,name,description,content,config,created_by) VALUES($1,'employee-role-fixture','Analyze customer reports','PRIVATE_EXECUTOR_SKILL_BODY','{}',$2) RETURNING id::text`, testWorkspaceID, testUserID).Scan(&skillID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_skill WHERE skill_id=$1`, skillID)
		_, _ = testPool.Exec(ctx, `DELETE FROM skill WHERE id=$1`, skillID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_skill(agent_id,skill_id,enabled) VALUES($1,$2,true)`, f.agentID, skillID); err != nil {
		t.Fatal(err)
	}
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var snapshot employeeSavedInput
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(snapshot.Config.Persona.Instructions, instructions+"\n\n") {
		t.Fatalf("agent constraints missing from model input: %+v", snapshot.Config.Persona)
	}
	catalog := strings.Join(snapshot.Config.Persona.Expertise, "\n")
	if !strings.Contains(catalog, "employee-role-fixture") || !strings.Contains(catalog, "Analyze customer reports") || strings.Contains(string(raw), "PRIVATE_EXECUTOR_SKILL_BODY") {
		t.Fatalf("skill catalog scope/content: %s", raw)
	}
}

func TestEmployeeSceneLegacyUnmappedStillFreezesHeldOwner(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Conversation.Type = "unrecognized"
	f.h.EventRouteConfig = func(string, string, string) (string, string) { return eventrouter.Legacy, "legacy-test" }
	key := uuid.NewString()
	response := employeeHTTP(t, f, dc, key)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "unknown_kind") {
		t.Fatalf("legacy unmapped fell through: %d %s", response.Code, response.Body.String())
	}
	var owner, state string
	var sceneID *string
	if err := testPool.QueryRow(ctx, `SELECT owner_loop,state,scene_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&owner, &state, &sceneID); err != nil {
		t.Fatal(err)
	}
	if owner != "employee" || state != "held" || sceneID != nil || model.calls != 0 {
		t.Fatalf("bad unmapped owner=%s state=%s scene=%v calls=%d", owner, state, sceneID, model.calls)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	response = employeeHTTP(t, f, dc, key)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "held") {
		t.Fatalf("mode change released unmapped receipt: %d %s", response.Code, response.Body.String())
	}
}

type employeeFlakyModel struct {
	base  *employeeTestModel
	calls int
}

func (m *employeeFlakyModel) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	if m.calls == 1 {
		return nil, errors.New("provider temporarily unavailable")
	}
	return m.base.Chat(ctx, p)
}

type employeeCancelAfterCommit struct{ cancel context.CancelFunc }

func (n employeeCancelAfterCommit) NotifyTaskAvailable(string, string) { n.cancel() }

func TestEmployeeSceneProviderFailureJournalRetainsLaterDispatch(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.dispatch = true
	flaky := &employeeFlakyModel{base: model}
	f.h.EmployeeSceneWorker.model = flaky
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/message-1"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	model.dispatch = false
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var outcome employeeSavedOutcome
	if err := json.Unmarshal(raw, &outcome); err != nil {
		t.Fatal(err)
	}
	if flaky.calls != 2 || len(outcome.Outcome.Receipts) != 1 || outcome.Outcome.Kind != employeeloop.Dispatched {
		t.Fatalf("provider error regenerated work trajectory: network=%d outcome=%s", flaky.calls, raw)
	}
}

func TestEmployeeSceneCommitReceiptSurvivesJournalCancellation(t *testing.T) {
	f, model, dc := employeeFixture(t)
	background := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.dispatch = true
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(background, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/message-1"
	ctx, cancel := context.WithCancel(background)
	defer cancel()
	f.h.TaskService.Wakeup = employeeCancelAfterCommit{cancel: cancel}
	_, _ = f.h.EmployeeSceneWorker.ProcessNext(ctx)
	var raw []byte
	var runs int
	if err := testPool.QueryRow(background, `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(background, `SELECT count(*) FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	var outcome employeeSavedOutcome
	if err := json.Unmarshal(raw, &outcome); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || len(outcome.Outcome.Receipts) != 1 || outcome.Outcome.Kind != employeeloop.Dispatched || strings.Contains(outcome.Outcome.Reply, "没能完成受理") {
		t.Fatalf("denied a committed action: runs=%d outcome=%s", runs, raw)
	}
}

func TestEmployeeSceneQuietIsDurableWithoutUserVisibleSend(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	model.quiet = true
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var sends int
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&sends); err != nil {
		t.Fatal(err)
	}
	var outcome employeeSavedOutcome
	if err := json.Unmarshal(raw, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome.Kind != employeeloop.Quiet || sends != 0 || model.calls != 1 {
		t.Fatalf("quiet decision became a reply: sends=%d model=%d outcome=%s", sends, model.calls, raw)
	}
}

func TestEmployeeSceneHistoricalReceiptKeepsLegacyAcceptance(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.ResponsePolicy = nil
	key := uuid.NewString()
	worker := f.h.EmployeeSceneWorker
	f.h.EmployeeSceneWorker = nil
	if _, err := testPool.Exec(ctx, `UPDATE agent SET coordination_mode='coordinator' WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.command)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/dispatch", nil)
	request.Header.Set("Idempotency-Key", key)
	command := f.command
	if f.h.admitDispatchEvent(httptest.NewRecorder(), request, raw, &command, dc) {
		t.Fatal("legacy callback admission was unexpectedly held")
	}
	accepted, replay, err := f.h.claimAgentDispatchAcceptance(ctx, command, dc, key)
	if err != nil || replay {
		t.Fatalf("legacy acceptance: %v %v", replay, err)
	}
	prior := newBufferedDispatchResponse()
	writeJSON(prior, http.StatusAccepted, map[string]string{"taskId": f.taskID, "legacy": "already-accepted"})
	if err = f.h.completeAgentDispatchAcceptance(ctx, accepted, prior); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dispatch_acceptance WHERE agent_id=$1`, f.agentID)
	})
	if _, err = testPool.Exec(ctx, `UPDATE agent SET coordination_mode='employee' WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	f.h.EmployeeSceneWorker = worker
	response := employeeHTTP(t, f, dc, key)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "already-accepted") {
		t.Fatalf("historical receipt was rerouted: %d %s", response.Code, response.Body.String())
	}
	var jobs int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&jobs); err != nil || jobs != 0 || model.calls != 0 {
		t.Fatalf("historic replay duplicated work: jobs=%d models=%d err=%v", jobs, model.calls, err)
	}
}

func TestEmployeeSceneReceiptAndConsumerRollbackTogether(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	name := "employee_fault_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(ctx, `ALTER TABLE employee_event_consumption ADD CONSTRAINT `+name+` CHECK(agent_id<>'`+f.agentID+`'::uuid)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `ALTER TABLE employee_event_consumption DROP CONSTRAINT `+name) })
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("fault status: %d %s", response.Code, response.Body.String())
	}
	var receipts int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM scene_event_receipt WHERE agent_id=$1`, f.agentID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipt committed without a business owner: %d %v", receipts, err)
	}
}

func TestEmployeeSceneRouterSelfEchoIsHeldBeforeWake(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Sender.UID = f.command.ExternalIdentity.DWS.UID
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), "ignored_self") {
		t.Fatalf("self echo not suppressed: %d %s", response.Code, response.Body.String())
	}
	var jobs int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&jobs); err != nil || jobs != 0 || model.calls != 0 {
		t.Fatalf("self echo woke loop: jobs=%d models=%d %v", jobs, model.calls, err)
	}
}

func TestEmployeeSceneOversizedWindowGetsOneExplicitResponse(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Messages[0].Text = strings.Repeat("x", 300<<10)
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatalf("admission: %d %s", response.Code, response.Body.String())
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT state,outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || model.calls != 0 {
		t.Fatalf("permanent oversized input kept retrying: state=%s calls=%d", state, model.calls)
	}
	var saved employeeSavedOutcome
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Outcome.Reply, "过长") || !strings.Contains(saved.Outcome.Reply, "文件") {
		t.Fatalf("missing actionable response: %+v", saved.Outcome.Decision)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || worked {
		t.Fatalf("completed oversized window reclaimed: %v %v", worked, err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeat/missing oversized response: %d %v", count, err)
	}
}

func TestEmployeeSceneDispatchPreservesStructuredDefinition(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "structured-request", Text: "Analyze feedback; deliver a CSV report, preserve every original row; request finance.read only if needed."}}
	model.dispatch = true
	model.definition = &employeetask.Definition{Deliverables: []string{" CSV report "}, SuccessCriteria: []string{" Preserve every original row "}, AccessNeeded: []string{" finance.read "}}
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = receipt + "/structured-request"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var definitionRaw, contextRaw []byte
	if err := testPool.QueryRow(ctx, `SELECT t.definition,q.context FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE t.agent_id=$1`, f.agentID).Scan(&definitionRaw, &contextRaw); err != nil {
		t.Fatal(err)
	}
	var definition employeetask.Definition
	var queueContext map[string]any
	if err := json.Unmarshal(definitionRaw, &definition); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contextRaw, &queueContext); err != nil {
		t.Fatal(err)
	}
	if len(definition.Deliverables) != 1 || definition.Deliverables[0] != "CSV report" || len(definition.SuccessCriteria) != 1 || definition.SuccessCriteria[0] != "Preserve every original row" || len(definition.AccessNeeded) != 1 || definition.AccessNeeded[0] != "finance.read" {
		t.Fatalf("structured user contract lost: %s", definitionRaw)
	}
	prompt, _ := queueContext["direct_task_prompt"].(string)
	for _, expected := range []string{"Deliverables: CSV report", "Preserve every original row", "Access needed (requested, not granted): finance.read", "ACTUAL CAPABILITIES (Host verified): none declared"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("compiled packet lacks %q: %s", expected, prompt)
		}
	}
	if model.calls != 1 {
		t.Fatalf("structured dispatch needed another model call: %d", model.calls)
	}
}

func TestEmployeeDefinitionArrayArgumentsRejectWrongTypes(t *testing.T) {
	for _, value := range []any{nil, "report", true, []any{"report", 7}} {
		if _, err := optionalStringArray(map[string]any{"deliverables": value}, "deliverables"); err == nil {
			t.Fatalf("accepted non-string-array: %#v", value)
		}
	}
	if values, err := optionalStringArray(map[string]any{}, "deliverables"); err != nil || len(values) != 0 {
		t.Fatal("optional omission forced a contract", values, err)
	}
}
