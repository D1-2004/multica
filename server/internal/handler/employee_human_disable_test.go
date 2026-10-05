package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service/a2ui"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	openai "github.com/openai/openai-go/v3"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func TestEmployeeHumanDisableOnlyClosesCardAndRejectsLateClick(t *testing.T) {
	f, _, q, probe := humanProjectionFixture(t)
	ctx := context.Background()
	database, _ := employeeEntryDB(f.h)
	response := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: "disable-current", Surface: "chat_text", RequesterRef: q.RequesterRef, Intent: "dismiss", Reason: "request_changed", RawText: "不选了，改写另外一个主题", EvidenceQuote: "不选了"}
	hook := func(ctx context.Context, tx pgx.Tx, q humanquestion.Question, r *humanquestion.Response) error {
		return f.h.stageEmployeeHumanCardProjectionTx(ctx, tx, q, r)
	}
	tx, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	foreign := response
	foreign.RequesterRef = "another-actor"
	if _, _, err = humanquestion.AcceptTx(ctx, tx, q.Scope, foreign, hook); !errors.Is(err, humanquestion.ErrForbidden) {
		t.Fatal("foreign actor accepted", err)
	}
	foreignScope := q.Scope
	foreignScope.SceneID = uuid.NewString()
	if _, _, err = humanquestion.AcceptTx(ctx, tx, foreignScope, response, hook); err == nil {
		t.Fatal("foreign scene accepted")
	}
	for range 2 {
		if _, _, err = humanquestion.AcceptTx(ctx, tx, q.Scope, response, hook); err != nil {
			t.Fatal(err)
		}
	}
	late := response
	late.ID = uuid.NewString()
	late.EventID = "late-click"
	late.Surface = "a2ui_action"
	late.Intent = "answer"
	late.Reason = ""
	late.Selected = []string{"process"}
	late.RawText = ""
	late.EvidenceQuote = ""
	if _, _, err = humanquestion.AcceptTx(ctx, tx, q.Scope, late, hook); !errors.Is(err, humanquestion.ErrStale) {
		t.Fatal("late click accepted", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := humanquestion.NewStore(database).Pending(ctx, q.Scope, q.RequesterRef)
	if err != nil || len(pending) != 0 {
		t.Fatal("disabled still pending", pending, err)
	}
	var responses, jobs, tasks, projections int
	err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_human_response WHERE question_id=$1::uuid),(SELECT count(*) FROM employee_scene_job WHERE agent_id=$2::uuid AND kind='human_response'),(SELECT count(*) FROM employee_task WHERE agent_id=$2::uuid),(SELECT count(*) FROM employee_human_card_projection WHERE question_id=$1::uuid)`, q.ID, f.agentID).Scan(&responses, &jobs, &tasks, &projections)
	if err != nil || responses != 1 || jobs != 0 || tasks != 0 || projections != 1 {
		t.Fatal("disable created work or duplicate projection", responses, jobs, tasks, projections, err)
	}
	humanProjectionReceipt(t, f, q)
	if updated, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 5); err != nil || updated != 1 {
		t.Fatal("disabled projection not delivered", updated, err)
	}
	if probe.calls != 1 || probe.sends != 0 || len(probe.messages) != 1 {
		t.Fatal("disable did not update only original card", probe)
	}
	for _, message := range probe.messages[0] {
		if strings.Contains(message, "runtime.clarification.submit") || strings.Contains(message, "ChoicePicker") {
			t.Fatal("disabled projection still has actions", message)
		}
	}

	result, err := employeeHumanCardResult(q, response)
	if err != nil || result.Outcome != "disabled" || len(result.Selected) != 0 {
		t.Fatal(result, err)
	}
}

func TestEmployeeHumanDisableHostCanContinueRound(t *testing.T) {
	f, dc, q := humanCardFixture(t)
	ctx := context.Background()
	f.command.Event.Data.Messages[0].OpenMsgID = "disable-text"
	f.command.Event.Data.Messages[0].Text = "不选了，告诉我你能做什么"
	res := employeeHTTP(t, f, dc, uuid.NewString())
	if res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT c.receipt_id::text FROM employee_event_consumption c JOIN employee_scene_job j ON j.id=c.job_id WHERE c.agent_id=$1::uuid AND j.state='pending' AND j.kind='message'`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model := &disableThenReplyModel{question: q.ID, source: receipt + "/disable-text"}
	f.h.EmployeeSceneWorker.model = model
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	var state string
	if err := testPool.QueryRow(ctx, `SELECT state FROM employee_human_question WHERE id=$1::uuid`, q.ID).Scan(&state); err != nil || state != "answered" || model.calls != 2 {
		t.Fatal(state, model.calls, err)
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='human_response'`, f.agentID).Scan(&n); err != nil || n != 0 {
		t.Fatal("unexpected human wake", n, err)
	}
}

// Compile-time schema coverage also guards accidental terminal designation.
func TestEmployeeHumanDisableToolIsNonTerminal(t *testing.T) {
	for _, tool := range employeeHumanTools() {
		if tool.Name == "disable_human_question" {
			if !tool.Effect || tool.Terminal != "" {
				t.Fatal(tool)
			}
			registry := employeeloop.NewToolRegistry()
			registry.Register(tool)
			ok, errs := registry.Validate(tool.Name, map[string]any{"source_ref": "current", "question_ref": "question", "evidence_quote": "text", "reason": "request_changed"})
			if !ok {
				t.Fatal(errs)
			}
			return
		}
	}
	t.Fatal("tool missing")
}

type disableThenReplyModel struct {
	question, source string
	calls            int
}

func (m *disableThenReplyModel) Chat(_ context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	name := "disable_human_question"
	args := map[string]any{"source_ref": m.source, "question_ref": m.question, "evidence_quote": "不选了", "reason": "request_changed"}
	if m.calls > 1 {
		name = "reply"
		args = map[string]any{"source_ref": m.source, "reply": "我可以帮你整理文档。"}
	}
	encoded, _ := json.Marshal(args)
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": name, "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}}}})
	var result openai.ChatCompletion
	err := json.Unmarshal(raw, &result)
	return &result, err
}

func TestEmployeeHumanDisableRacesNativeWithOneTerminal(t *testing.T) {
	f, _, q := humanCardFixture(t)
	ctx := context.Background()
	database, _ := employeeEntryDB(f.h)
	dismiss := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: "race-dismiss", Surface: "chat_text", RequesterRef: q.RequesterRef, Intent: "dismiss", Reason: "chat_continued", RawText: "不选了继续聊", EvidenceQuote: "不选了"}
	answer := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: "race-click", Surface: "a2ui_action", RequesterRef: q.RequesterRef, Intent: "answer", Selected: []string{"process"}}
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	for _, r := range []humanquestion.Response{dismiss, answer} {
		go func(r humanquestion.Response) {
			<-start
			tx, err := database.Begin(ctx)
			if err != nil {
				outcomes <- err
				return
			}
			defer tx.Rollback(ctx)
			_, _, err = humanquestion.AcceptTx(ctx, tx, q.Scope, r, func(ctx context.Context, tx pgx.Tx, q humanquestion.Question, r *humanquestion.Response) error {
				if r.Intent == "dismiss" {
					return f.h.stageEmployeeHumanCardProjectionTx(ctx, tx, q, r)
				}
				return f.h.admitEmployeeHumanResponseTx(ctx, tx, q, r)
			})
			if err == nil {
				err = tx.Commit(ctx)
			}
			outcomes <- err
		}(r)
	}
	close(start)
	wins, stale := 0, 0
	for range 2 {
		err := <-outcomes
		if err == nil {
			wins++
		} else if errors.Is(err, humanquestion.ErrStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatal("CAS failed", wins, stale)
	}
	var responses, projections, jobs int
	var intent string
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_human_response WHERE question_id=$1::uuid),(SELECT count(*) FROM employee_human_card_projection WHERE question_id=$1::uuid),(SELECT count(*) FROM employee_scene_job WHERE agent_id=$2::uuid AND kind='human_response'),(SELECT body->>'intent' FROM employee_human_response WHERE question_id=$1::uuid)`, q.ID, f.agentID).Scan(&responses, &projections, &jobs, &intent); err != nil {
		t.Fatal(err)
	}
	expected := 0
	if intent == "answer" {
		expected = 1
	}
	if responses != 1 || projections != 1 || jobs != expected {
		t.Fatal("race created duplicate or dismissed work", responses, projections, jobs, intent)
	}
}

func TestEmployeeHumanDeferPreservesWaitAndTextResumesSameTask(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	c, _, taskID, runID := humanWorkerV2Fixture(t)
	f := c.f
	ctx := context.Background()
	database, _ := employeeEntryDB(f.h)
	var originalID string
	var scope employeeentry.Scope
	if err := testPool.QueryRow(ctx, `SELECT id::text,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text FROM employee_human_question WHERE agent_id=$1::uuid ORDER BY created_at LIMIT 1`, f.agentID).Scan(&originalID, &scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.SceneID); err != nil {
		t.Fatal(err)
	}
	q, err := humanquestion.NewStore(database).Get(ctx, scope, originalID)
	if err != nil {
		t.Fatal(err)
	}
	q.ID = uuid.NewString()
	q.TaskID = taskID
	q.RunID = runID
	q.PublicID = ""
	q.ActionID = ""
	q.ResponseID = ""
	q.State = "open"
	if err = testPool.QueryRow(ctx, `SELECT goal_revision FROM employee_task WHERE id=$1::uuid`, taskID).Scan(&q.GoalRevision); err != nil {
		t.Fatal(err)
	}
	var external string
	if err = testPool.QueryRow(ctx, `SELECT external_scene_id FROM agent_scene WHERE id=$1::uuid`, scope.SceneID).Scan(&external); err != nil {
		t.Fatal(err)
	}
	f.h.A2UI = a2ui.New(f.h.Queries)
	tx, err := database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	in := dingtalkresponse.ActionInput{WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, DWSUID: f.command.ExternalIdentity.DWS.UID, DWSOrgID: scope.TenantOrgID, SceneID: scope.SceneID, ConversationID: external, SenderOpenDingTalkID: q.OperatorOpenID, IsGroup: true, Text: q.Summary}
	if _, err = f.h.stageEmployeeHumanQuestion(ctx, tx, &q, in); err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(tx).Get(ctx, employeeHumanTaskScope(scope), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = employeetask.WaitTaskTx(ctx, tx, task.Scope, task.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "disable_test", Key: q.ID + "/wait"}, Kind: employeetask.WaitHumanInput, RefID: q.ID, Mandatory: true, AuthorityRef: "human-question:" + q.ID, ExpectedVersion: task.Version}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid) ON CONFLICT(agent_id) DO NOTHING`, f.agentID, scope.WorkspaceID, in.DWSUID, scope.TenantOrgID, q.PrincipalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_human_card_projection WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM a2ui_interaction WHERE agent_id=$1::uuid`, f.agentID)
	})
	probe := &humanCardUpdateProbe{}
	f.h.DingTalkResponses = dingtalkresponse.NewService(testPool, probe, nil)
	humanProjectionReceipt(t, f, q)
	snapshot := func() string {
		var raw string
		err := testPool.QueryRow(ctx, `SELECT jsonb_build_object('task',to_jsonb(t),'runs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM employee_task_run r WHERE r.task_id=t.id),'waits',(SELECT jsonb_agg(to_jsonb(w) ORDER BY w.id) FROM employee_task_wait w WHERE w.task_id=t.id),'queues',(SELECT jsonb_agg(to_jsonb(aq) ORDER BY aq.id) FROM agent_task_queue aq JOIN employee_task_run r ON r.queue_task_id=aq.id WHERE r.task_id=t.id))::text FROM employee_task t WHERE t.id=$1::uuid`, taskID).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	var jobsBefore int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='human_response'`, f.agentID).Scan(&jobsBefore); err != nil {
		t.Fatal(err)
	}
	tx, err = database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	response := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: "bound-task-defer", Surface: "chat_text", RequesterRef: q.RequesterRef, Intent: "defer", Reason: "deferred", RawText: "先不管", EvidenceQuote: "先不管"}
	dismiss := response
	dismiss.Intent, dismiss.Reason = "dismiss", "not_needed"
	if _, _, err = humanquestion.AcceptTx(ctx, tx, scope, dismiss, f.h.stageEmployeeHumanCardProjectionTx); !errors.Is(err, humanquestion.ErrConflict) {
		t.Fatal("required wait could be orphaned by dismissal", err)
	}
	if _, _, err = humanquestion.AcceptTx(ctx, tx, scope, response, f.h.stageEmployeeHumanCardProjectionTx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if updated, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 5); err != nil || updated != 1 {
		t.Fatal(updated, err)
	}
	if before != snapshot() {
		t.Fatal("disable altered Task/Run/wait/queue")
	}
	pending, err := humanquestion.NewStore(database).Pending(ctx, scope, q.RequesterRef)
	if err != nil || len(pending) != 1 || pending[0].ID != q.ID || pending[0].State != "deferred" {
		t.Fatal(pending, err)
	}
	var jobsAfter int
	if err = testPool.QueryRow(ctx, `SELECT count(*) FROM employee_scene_job WHERE agent_id=$1::uuid AND kind='human_response'`, f.agentID).Scan(&jobsAfter); err != nil || jobsAfter != jobsBefore {
		t.Fatal("disable added typed job", jobsBefore, jobsAfter, err)
	}
	if probe.calls != 1 || probe.sends != 0 {
		t.Fatal("card not closed by original update", probe)
	}
	late := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: "late-deferred-click", Surface: "a2ui_action", RequesterRef: q.RequesterRef, Intent: "answer", Selected: []string{q.Choice.Options[0].ID}}
	tx, err = database.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, _, err = humanquestion.AcceptTx(ctx, tx, scope, late, f.h.admitEmployeeHumanResponseTx); !errors.Is(err, humanquestion.ErrStale) {
		t.Fatal("old deferred button resumed work", err)
	}
	answer := late
	answer.ID, answer.EventID, answer.Surface = uuid.NewString(), "fresh-text-answer", "chat_text"
	answer.RawText, answer.EvidenceQuote = "现在继续整理流程", "继续整理"
	if _, _, err = humanquestion.AcceptTx(ctx, tx, scope, answer, f.h.admitEmployeeHumanResponseTx); err != nil {
		t.Fatal("text answer failed to resume deferred question", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if before != snapshot() {
		t.Fatal("accepting text prematurely released the mandatory wait")
	}
	c.model.set(func(string) (string, map[string]any) {
		return wakeToolCall("resume-deferred", "continue_question_work", map[string]any{"prompt": "继续整理流程，不发送", "reply": "我来继续整理。"})
	})
	c.process()
	var nextTask, waitState string
	if err = testPool.QueryRow(ctx, `SELECT r.task_id::text,w.state FROM employee_task_run r JOIN agent_task_queue aq ON aq.id=r.queue_task_id JOIN employee_task_wait w ON w.task_id=r.task_id AND w.ref_id=$2 WHERE aq.context->>'employee_human_response_id'=$1`, answer.ID, q.ID).Scan(&nextTask, &waitState); err != nil || nextTask != taskID || waitState != "satisfied" {
		t.Fatal("response did not resume the exact original wait/Task", nextTask, waitState, err)
	}
	if updated, err := f.h.ReconcileEmployeeHumanCardProjections(ctx, 5); err != nil || updated != 0 || probe.calls != 1 {
		t.Fatal("text answer reopened the permanently closed card", updated, probe.calls, err)
	}
}
