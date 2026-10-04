package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	openai "github.com/openai/openai-go/v3"
)

type humanQuestionModel struct {
	name  string
	args  map[string]any
	calls int
}

func (m *humanQuestionModel) Chat(_ context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	args, _ := json.Marshal(m.args)
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "human-call", "type": "function", "function": map[string]any{"name": m.name, "arguments": string(args)}}}}}}})
	var result openai.ChatCompletion
	err := json.Unmarshal(raw, &result)
	return &result, err
}

func humanCardFixture(t *testing.T) (*dingTalkResponseFixture, agentDispatchContext, humanquestion.Question) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	f, _, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	ep, err := f.h.Queries.EnsureAgentDispatchEndpoint(context.Background(), db.EnsureAgentDispatchEndpointParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID), ActorUserID: parseUUID(testUserID), EndpointID: "human-" + uuid.NewString(), DispatchUrl: "https://test.invalid/dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	dc.EndpointID, dc.EndpointNamespaceID = ep.EndpointID, ep.ID
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID)
	})
	f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return true, nil }
	f.h.A2UI = a2ui.New(f.h.Queries)
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model := &humanQuestionModel{name: "a2ui_ask", args: map[string]any{"source_ref": receipt + "/message-1", "summary": "先确认你要的范围。", "choice": map[string]any{"intent": "clarify", "kind": "single", "question": "先整理哪类资料？", "options": []any{map[string]any{"id": "handbook", "label": "员工手册"}, map[string]any{"id": "process", "label": "办公流程"}}, "allow_custom": true}}}
	f.h.EmployeeSceneWorker.model = model
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM employee_human_question WHERE agent_id=$1`, f.agentID).Scan(&id); err != nil {
		var outcome []byte
		_ = testPool.QueryRow(context.Background(), `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&outcome)
		t.Fatal(err, string(outcome))
	}
	database, _ := employeeEntryDB(f.h)
	var scope employeeentry.Scope
	if err := testPool.QueryRow(context.Background(), `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text FROM employee_human_question WHERE id=$1::uuid`, id).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.SceneID); err != nil {
		t.Fatal(err)
	}
	q, err := humanquestion.NewStore(database).Get(context.Background(), scope, id)
	if err != nil {
		t.Fatal(err)
	}
	var tasks, actions int
	if err = testPool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1),(SELECT count(*) FROM response_action WHERE agent_id=$1 AND input ? 'a2ui_card')`, f.agentID).Scan(&tasks, &actions); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 || actions != 1 || q.State != "open" || model.calls != 1 {
		t.Fatal("question dispatched work or missed its durable card", tasks, actions, q.State, model.calls)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_human_response WHERE question_id IN(SELECT id FROM employee_human_question WHERE agent_id=$1)`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_human_question WHERE agent_id=$1`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM a2ui_interaction WHERE agent_id=$1`, f.agentID)
	})
	return f, dc, q
}

func nativeQuestionLine(q humanquestion.Question, event, operator, choice string) []byte {
	raw, _ := json.Marshal(map[string]any{"eventId": event, "eventKey": "user_card_action_triggered", "payload": map[string]any{"body": map[string]any{"operatorDTO": map[string]any{"openDingTalkId": operator}, "a2uiEvent": map[string]any{"action": map[string]any{"name": "runtime.clarification.submit", "context": map[string]any{"sourceTurnId": q.PublicID, "sourceProjectionVersion": a2ui.Version, "outcome": "answered", "answers": map[string]any{"q0": map[string]any{"selected": []string{choice}, "custom": ""}}}}}}}})
	return raw
}

func TestEmployeeHumanCardRejectsForeignActorIllegalChoiceAndDeduplicates(t *testing.T) {
	f, _, q := humanCardFixture(t)
	ctx := context.Background()
	var uid string
	if err := testPool.QueryRow(ctx, `SELECT sender_uid FROM a2ui_interaction WHERE id=$1::uuid`, q.ID).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	actor := dwsclient.Identity{AgentID: f.agentID, UID: uid, OrgID: q.Scope.TenantOrgID}
	for _, line := range [][]byte{nativeQuestionLine(q, "wrong-person", "someone-else", "o1"), nativeQuestionLine(q, "illegal", q.OperatorOpenID, "o99")} {
		if err := f.h.HandleDWSNativeCardAction(ctx, actor, line, nil); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_human_response WHERE question_id=$1::uuid`, q.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("invalid click consumed answer", n, err)
	}
	line := nativeQuestionLine(q, "valid-once", q.OperatorOpenID, "o1")
	for range 3 {
		if err := f.h.HandleDWSNativeCardAction(ctx, actor, line, nil); err != nil {
			t.Fatal(err)
		}
	}
	var body []byte
	if err := testPool.QueryRow(ctx, `SELECT body FROM employee_human_response WHERE question_id=$1::uuid`, q.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var r humanquestion.Response
	if json.Unmarshal(body, &r) != nil || len(r.Selected) != 1 || r.Selected[0] != "process" || r.Surface != "a2ui_action" {
		t.Fatal(string(body))
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1 AND kind='human_response'`, f.agentID).Scan(&n); err != nil || n != 1 {
		t.Fatal("callback not uniquely queued", n, err)
	}
}

func TestEmployeeHumanTextIsOrdinaryMessageAndPreservesConstraints(t *testing.T) {
	f, dc, q := humanCardFixture(t)
	ctx := context.Background()
	f.command.Event.Data.Messages[0].OpenMsgID = "typed-answer"
	f.command.Event.Data.Messages[0].Text = "先整理办公流程，简短一点，先不要发给别人。"
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT c.receipt_id::text FROM employee_event_consumption c JOIN employee_scene_job j ON j.id=c.job_id WHERE c.agent_id=$1 AND j.state='pending' AND j.kind='message'`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model := &humanQuestionModel{name: "accept_human_response", args: map[string]any{"source_ref": receipt + "/typed-answer", "question_ref": q.ID, "intent": "answer", "selected": []string{"process"}, "answer_quote": "先整理办公流程"}}
	f.h.EmployeeSceneWorker.model = model
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT body FROM employee_human_response WHERE question_id=$1::uuid`, q.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var r humanquestion.Response
	if json.Unmarshal(raw, &r) != nil || r.RawText != f.command.Event.Data.Messages[0].Text || r.Surface != "chat_text" {
		t.Fatal("raw words or input surface lost", string(raw))
	}
	var input []byte
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=(SELECT job_id FROM employee_event_consumption WHERE receipt_id=$1::uuid)`, receipt).Scan(&input); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedInput
	if json.Unmarshal(input, &saved) != nil || saved.Input.TaskBrief == "" {
		t.Fatal("pending questions were not explicitly available", string(input))
	}
}

// First questions have no pending ledger entry yet. Their frozen input still
// needs the policy that keeps missing pre-execution answers out of auto plans.
func TestEmployeeHumanFirstQuestionFreezesPolicyWithoutPendingAndOnReplay(t *testing.T) {
	f, _, q := humanCardFixture(t)
	ctx := context.Background()
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=$1::uuid`, q.SourceJobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedInput
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved.Config.Persona.Instructions, "HUMAN INTERACTION:") || !strings.Contains(saved.Config.Persona.Instructions, "never a human-input wait") || strings.Contains(saved.Input.TaskBrief, `"pending_human_questions":`) {
		t.Fatal("first-question policy omitted or invented pending question data", saved.Config.Persona.Instructions, saved.Input.TaskBrief)
	}
	original := string(raw)
	f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return false, nil }
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE id=$1::uuid`, q.SourceJobID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT input_snapshot FROM employee_scene_job WHERE id=$1::uuid`, q.SourceJobID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatal("new policy hot-rewrote a frozen input on replay")
	}
}

func TestEmployeeHumanPolicyDoesNotUpgradeUnsupportedProducer(t *testing.T) {
	w := &EmployeeSceneWorker{HumanQuestionsReady: func(context.Context) (bool, error) { return false, nil }}
	in := employeeSavedInput{}
	if err := w.appendHumanQuestions(context.Background(), employeeentry.Job{}, nil, &in); err != nil {
		t.Fatal(err)
	}
	if in.Config.Persona.Instructions != "" {
		t.Fatal("human policy was injected without the producer gate")
	}
}
