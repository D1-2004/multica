package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeHumanResponsePreservesDistinctOriginalAndAnswer(t *testing.T) {
	b := employeeHumanBinding{Source: employeeSourceMessage{SourceRef: "original-receipt/original-message", Message: DispatchMessage{OpenMsgID: "original-message", Text: "帮我把草稿发给李明"}}, Question: humanquestion.Question{ID: "question", Summary: "有两位同名同事，尚未发送", Choice: humanquestion.Choice{Question: "选哪位？"}}, Response: humanquestion.Response{QuestionID: "question", Surface: "chat_text", RawText: "研发那位，先给我看简短版，不要发送", EvidenceQuote: "研发那位"}}
	raw, err := employeeHumanResponseWindow(b)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Source   employeeSourceMessage  `json:"original_source"`
		Response humanquestion.Response `json:"human_response"`
	}
	if err = json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source.Message.Text != b.Source.Message.Text || got.Source.Message.OpenMsgID != "original-message" || got.Response.RawText != b.Response.RawText || got.Response.Surface != "chat_text" {
		t.Fatal("response rewrote provider evidence or lost the human's added constraints", raw)
	}
	if got.Source.Message.Text == got.Response.RawText {
		t.Fatal("answer was forged as an original provider message")
	}
}

func TestEmployeeHumanResponseCannotChooseAnotherTaskOrResumeWaitingGoal(t *testing.T) {
	b := employeeHumanBinding{Question: humanquestion.Question{TaskID: "bound-task", RequesterRef: "requester", GoalRevision: 2}, Response: humanquestion.Response{Intent: "answer"}}
	base := employeetask.Task{ID: "bound-task", RequesterRef: "requester", GoalRevision: 2, State: employeetask.StateSucceeded}
	if err := employeeHumanWorkAllowed(b, &base); err != nil {
		t.Fatal("valid successful v1 task refused", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*employeeHumanBinding, *employeetask.Task)
	}{
		{"other_task", func(_ *employeeHumanBinding, task *employeetask.Task) { task.ID = "newest-task" }},
		{"other_requester", func(_ *employeeHumanBinding, task *employeetask.Task) { task.RequesterRef = "other" }},
		{"old_revision", func(_ *employeeHumanBinding, task *employeetask.Task) { task.GoalRevision++ }},
		{"running", func(_ *employeeHumanBinding, task *employeetask.Task) { task.ActiveRunID = "active" }},
		{"failed", func(_ *employeeHumanBinding, task *employeetask.Task) { task.State = employeetask.StateFailed }},
		{"v2_waiting", func(_ *employeeHumanBinding, task *employeetask.Task) {
			task.LifecycleVersion = employeetask.LifecycleV2
			task.State = employeetask.StateWaiting
		}},
		{"legacy_amend", func(binding *employeeHumanBinding, _ *employeetask.Task) { binding.Response.Intent = "amend" }},
		{"skip", func(binding *employeeHumanBinding, _ *employeetask.Task) { binding.Response.Intent = "skip" }},
		{"cancel", func(binding *employeeHumanBinding, _ *employeetask.Task) { binding.Response.Intent = "cancel" }},
		{"dismiss", func(binding *employeeHumanBinding, _ *employeetask.Task) { binding.Response.Intent = "dismiss" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding, task := b, base
			test.mutate(&binding, &task)
			if err := employeeHumanWorkAllowed(binding, &task); !errors.Is(err, employeeloop.ErrToolRefused) {
				t.Fatal("unsafe response was not refused", err)
			}
		})
	}
	for _, intent := range []string{"skip", "cancel", "dismiss"} {
		b.Response.Intent = intent
		if err := employeeHumanWorkAllowed(b, nil); !errors.Is(err, employeeloop.ErrToolRefused) {
			t.Fatal("foreground response must not start work", intent)
		}
	}
}

// These tests use the real database worker and a scripted provider. They do not
// send DingTalk messages or execute a sandbox/ambient agent CLI.
func humanResponseWorkerFixture(t *testing.T, f *dingTalkResponseFixture, originalJobID, taskID, runID string, intents ...string) humanquestion.Response {
	t.Helper()
	f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return true, nil }
	ctx := context.Background()
	var job employeeentry.Job
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT id::text,kind,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,principal_id::text,items,state FROM employee_scene_job WHERE id=$1::uuid`, originalJobID).Scan(&job.ID, &job.Kind, &job.Scope.WorkspaceID, &job.Scope.AgentID, &job.Scope.TenantOrgID, &job.Scope.SceneID, &job.PrincipalID, &raw, &job.State); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &job.Items); err != nil {
		t.Fatal(err)
	}
	var env employeeDispatchEnvelope
	if err := json.Unmarshal(job.Items[0].Payload, &env); err != nil {
		t.Fatal(err)
	}
	source := employeeSourceMessages(job.Items[0], env)[0]
	q := humanquestion.Question{ID: uuid.NewString(), Scope: job.Scope, PrincipalID: job.PrincipalID, SourceJobID: job.ID, SourceReceiptID: source.ReceiptID, SourceRef: source.SourceRef, RequesterRef: source.RequesterRef, OperatorOpenID: source.Message.SenderOpenDingTalkID, Version: 1, Summary: "已整理资料，需要确认版本", Choice: humanquestion.Choice{Intent: "clarify", Kind: "single", Question: "需要哪个版本？", Options: []humanquestion.Option{{ID: "short", Label: "简短版"}, {ID: "full", Label: "完整版本"}}, AllowCustom: true}, TaskID: taskID, RunID: runID}
	if len(intents) > 0 {
		q.Choice.Intent = intents[0]
	}
	if taskID != "" {
		if err := testPool.QueryRow(ctx, `SELECT goal_revision FROM employee_task WHERE id=$1::uuid`, taskID).Scan(&q.GoalRevision); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_human_card_projection WHERE agent_id=$1::uuid`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_human_response WHERE question_id IN (SELECT id FROM employee_human_question WHERE agent_id=$1::uuid)`, f.agentID)
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_human_question WHERE agent_id=$1::uuid`, f.agentID)
	})
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = humanquestion.StageTx(ctx, tx, q); err != nil {
		t.Fatal(err)
	}
	if taskID != "" && q.Choice.Intent == "clarify" {
		task, e := employeetask.NewStore(tx).Get(ctx, employeeHumanTaskScope(q.Scope), taskID)
		if e != nil {
			t.Fatal(e)
		}
		if task.Lifecycle() == employeetask.LifecycleV2 {
			_, _, e = employeetask.WaitTaskTx(ctx, tx, task.Scope, task.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "human_worker_test", Key: q.ID + "/wait"}, Kind: employeetask.WaitHumanInput, RefID: q.ID, Mandatory: true, AuthorityRef: "human-question:" + q.ID, ExpectedVersion: task.Version})
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	r := humanquestion.Response{ID: uuid.NewString(), QuestionID: q.ID, EventID: uuid.NewString(), Surface: "chat_text", RequesterRef: q.RequesterRef, Intent: "answer", RawText: "给我简短版，先不要发送给别人", EvidenceQuote: "简短版", Selected: []string{"short"}}
	if len(intents) > 1 {
		r.Intent = intents[1]
	}
	_, r, err = humanquestion.AcceptTx(ctx, tx, q.Scope, r, f.h.admitEmployeeHumanResponseTx)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return r
}

func assertHumanResponseRun(t *testing.T, f *dingTalkResponseFixture, r humanquestion.Response, taskID string, expectedTasks, expectedRuns int) {
	t.Helper()
	ctx := context.Background()
	var tasks, runs int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid)`, f.agentID).Scan(&tasks, &runs); err != nil {
		t.Fatal(err)
	}
	if tasks != expectedTasks || runs != expectedRuns {
		var outcome []byte
		_ = testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1::uuid`, r.JobID).Scan(&outcome)
		t.Fatal("unexpected business Task/Run count", tasks, runs, string(outcome))
	}
	var gotTask, gotJob, gotResponse, prompt string
	if err := testPool.QueryRow(ctx, `SELECT r.task_id::text,q.context->>'employee_job_id',q.context->>'employee_human_response_id',q.context->>'direct_task_prompt' FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.agent_id=$1::uuid AND q.context->>'employee_human_response_id'=$2`, f.agentID, r.ID).Scan(&gotTask, &gotJob, &gotResponse, &prompt); err != nil {
		t.Fatal(err)
	}
	if (taskID != "" && gotTask != taskID) || gotJob != r.JobID || gotResponse != r.ID || !strings.Contains(prompt, r.RawText) {
		t.Fatal("typed answer execution lost its bound Task, effect job, or raw constraints", gotTask, gotJob, gotResponse, prompt)
	}
	var kind string
	var count int
	if err := testPool.QueryRow(ctx, `SELECT kind,message_count FROM employee_scene_job WHERE id=$1::uuid`, r.JobID).Scan(&kind, &count); err != nil {
		t.Fatal(err)
	}
	if kind != employeeentry.KindHumanResponse || count != 0 {
		t.Fatal("callback manufactured an IM job", kind, count)
	}
	var life int
	var contract string
	if err := testPool.QueryRow(ctx, `SELECT t.lifecycle_version,COALESCE(q.context->>'employee_round_result_contract','') FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id JOIN employee_task t ON t.id=r.task_id WHERE q.context->>'employee_human_response_id'=$1`, r.ID).Scan(&life, &contract); err != nil {
		t.Fatal(err)
	}
	if life == int(employeetask.LifecycleV2) && (contract != humanquestion.Version || !strings.Contains(prompt, humanquestion.PromptContract)) {
		t.Fatal("v2 run lost round-result contract", contract)
	}
	if life != int(employeetask.LifecycleV2) && contract != "" {
		t.Fatal("legacy single-run goal was falsely advertised as round-result capable", contract)
	}
}

func TestEmployeeHumanResponseForegroundCreatesOneTaskWithRealEffectJob(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires an explicit isolated DATABASE_URL")
	}
	c := newCollectionHarness(t)
	c.f.command.Event.Data.Messages[0].Text = "帮我整理办公流程资料"
	if response := employeeHTTP(t, c.f, c.dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	c.process()
	var job string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM employee_scene_job WHERE agent_id=$1::uuid`, c.f.agentID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	r := humanResponseWorkerFixture(t, c.f, job, "", "")
	c.model.set(func(request string) (string, map[string]any) {
		if !strings.Contains(request, r.RawText) {
			t.Error("model lost full human input")
		}
		return wakeToolCall("question-work", "continue_question_work", map[string]any{"prompt": "整理简短版办公流程，先不要发给别人", "reply": "我来整理简短版。"})
	})
	c.process()
	assertHumanResponseRun(t, c.f, r, "", 1, 1)
	c.process()
	assertHumanResponseRun(t, c.f, r, "", 1, 1)
}

func TestEmployeeHumanResponseExistingTaskGetsFreshRun(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires an explicit isolated DATABASE_URL")
	}
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	var taskID string
	if err := testPool.QueryRow(context.Background(), `SELECT task_id::text FROM employee_task_run WHERE id=$1::uuid`, f.runID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	r := humanResponseWorkerFixture(t, f.dingTalkResponseFixture, f.jobID, taskID, f.runID)
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		return employeeReplyCompletion(t, employeeReplyCall(t, "question-continue", "continue_question_work", map[string]any{"prompt": "基于已有资料整理简短版，不要发给别人", "reply": "我来继续整理。"})), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatal(worked, err)
	}
	assertHumanResponseRun(t, f.dingTalkResponseFixture, r, taskID, 1, 2)
}

func TestEmployeeHumanResponseV2ClarificationReleasesOnlyItsWait(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires an explicit isolated DATABASE_URL")
	}
	c, originalJob, taskID, runID := humanWorkerV2Fixture(t)
	f := c.f
	ctx := context.Background()
	r := humanResponseWorkerFixture(t, f, originalJob, taskID, runID)
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		return employeeReplyCompletion(t, employeeReplyCall(t, "wait-continue", "continue_question_work", map[string]any{"prompt": "整理简短版，不要发给别人", "reply": "我来继续整理。"})), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	assertHumanResponseRun(t, f, r, taskID, 1, 2)
	var life int
	var waitState string
	if err := testPool.QueryRow(ctx, `SELECT t.lifecycle_version,w.state FROM employee_task t JOIN employee_task_wait w ON w.task_id=t.id WHERE t.id=$1::uuid AND w.kind='human_input' AND w.ref_id=$2`, taskID, r.QuestionID).Scan(&life, &waitState); err != nil {
		t.Fatal(err)
	}
	if life != int(employeetask.LifecycleV2) || waitState != string(employeetask.WaitSatisfied) {
		t.Fatal("clarification did not satisfy its exact v2 wait", life, waitState)
	}
}

// humanWorkerV2Fixture creates its goal through the real foreground response
// worker, then records one completed Pi-shaped run without finishing the goal.
func humanWorkerV2Fixture(t *testing.T) (*collectionHarness, string, string, string) {
	t.Helper()
	c := newCollectionHarness(t)
	ctx := context.Background()
	c.f.command.Event.Data.Messages[0].Text = "帮我整理办公流程资料"
	if response := employeeHTTP(t, c.f, c.dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	c.process()
	var original string
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM employee_scene_job WHERE agent_id=$1::uuid`, c.f.agentID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	first := humanResponseWorkerFixture(t, c.f, original, "", "")
	c.model.set(func(string) (string, map[string]any) {
		return wakeToolCall("initial-human-work", "continue_question_work", map[string]any{"prompt": "整理资料，先不要发送", "reply": "我来整理。"})
	})
	c.process()
	var task, run, queue string
	if err := testPool.QueryRow(ctx, `SELECT r.task_id::text,r.id::text,r.queue_task_id::text FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.agent_id=$1::uuid AND q.context->>'employee_human_response_id'=$2`, c.f.agentID, first.ID).Scan(&task, &run, &queue); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, queue); err != nil {
		t.Fatal(err)
	}
	if _, err := c.f.h.TaskService.CompleteTask(ctx, parseUUID(queue), []byte(`{"output":"{\"version\":\"tag-round-result/v1\",\"summary\":\"资料已整理\"}"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	return c, original, task, run
}

func TestEmployeeHumanResponseV2SuggestionCreatesLinkedGoal(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires an explicit isolated DATABASE_URL")
	}
	c, original, taskID, runID := humanWorkerV2Fixture(t)
	ctx := context.Background()
	var scope employeeentry.Scope
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scene_id::text FROM employee_task WHERE id=$1::uuid`, taskID).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.SceneID); err != nil {
		t.Fatal(err)
	}
	task, err := employeetask.NewStore(testPool).Get(ctx, employeeHumanTaskScope(scope), taskID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := employeetask.NewStore(testPool).ReadCurrent(ctx, task.Scope, task.RequesterRef, taskID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = employeetask.NewStore(testPool).CompleteGoal(ctx, snapshot.Task.Scope, taskID, employeetask.CompleteGoalParams{Source: employeetask.Source{Namespace: "human_worker_test", Key: runID + "/complete"}, GoalRevision: snapshot.Task.GoalRevision, InputSeq: snapshot.Task.LastEntrySeq, ExpectedVersion: snapshot.Task.Version, AuthorityRef: "employee_task_entry:" + taskID + "/1", EvidenceRef: "test-completed-run:" + runID})
	if err != nil {
		t.Fatal(err)
	}
	r := humanResponseWorkerFixture(t, c.f, original, taskID, runID, "suggest")
	c.model.set(func(string) (string, map[string]any) {
		return wakeToolCall("suggested-new-goal", "continue_question_work", map[string]any{"prompt": "根据已有结果整理简短版，不要发给别人", "reply": "我来整理这个版本。"})
	})
	c.process()
	assertHumanResponseRun(t, c.f, r, "", 2, 2)
	var links int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_link l JOIN employee_task_run r ON r.task_id=l.task_id JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE q.context->>'employee_human_response_id'=$1 AND l.related_task_id=$2::uuid AND l.relation='builds_on'`, r.ID, taskID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatal("optional step reopened the completed v2 goal instead of linking a new goal", links)
	}
}

func TestEmployeeHumanResponseV2AmendmentUpdatesGoalBeforeFreshRun(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires an explicit isolated DATABASE_URL")
	}
	c, original, taskID, runID := humanWorkerV2Fixture(t)
	ctx := context.Background()
	r := humanResponseWorkerFixture(t, c.f, original, taskID, runID, "clarify", "amend")
	c.model.set(func(string) (string, map[string]any) {
		return wakeToolCall("amended-question-work", "continue_question_work", map[string]any{"prompt": "按人工修改仅整理简短版，不要发给别人", "reply": "我按新的要求整理。"})
	})
	c.process()
	assertHumanResponseRun(t, c.f, r, taskID, 1, 2)
	var revision int
	var goal, source string
	if err := testPool.QueryRow(ctx, `SELECT t.goal_revision,t.definition->>'goal',e.source_key FROM employee_task t JOIN employee_task_entry e ON e.task_id=t.id AND e.kind='amendment' WHERE t.id=$1::uuid`, taskID).Scan(&revision, &goal, &source); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || !strings.Contains(goal, r.RawText) || !strings.HasSuffix(source, "/"+r.ID+"/amend") {
		t.Fatal("human change did not become a source-bound goal amendment", revision, goal, source)
	}
}

func TestEmployeeHumanResponseFinalRunReturnsVerifiedNotice(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	ctx := context.Background()
	c := newCollectionHarness(t)
	if response := employeeHTTP(t, c.f, c.dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	c.process()
	var originalJob string
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM employee_scene_job WHERE agent_id=$1::uuid`, c.f.agentID).Scan(&originalJob); err != nil {
		t.Fatal(err)
	}
	r := humanResponseWorkerFixture(t, c.f, originalJob, "", "")
	c.model.set(func(string) (string, map[string]any) {
		return wakeToolCall("final-human-work", "continue_question_work", map[string]any{"prompt": "整理简短版，不发给别人", "reply": "我来整理。"})
	})
	c.process()
	var runID, queueID string
	if err := testPool.QueryRow(ctx, `SELECT r.id::text,r.queue_task_id::text FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE q.context->>'employee_human_response_id'=$1`, r.ID).Scan(&runID, &queueID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, queueID); err != nil {
		t.Fatal(err)
	}
	output := `{"version":"tag-round-result/v1","summary":"简短版资料已整理。"}`
	result, _ := json.Marshal(map[string]string{"output": output})
	if _, err := c.f.h.TaskService.CompleteTask(ctx, parseUUID(queueID), result, "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if created, err := c.f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, runID); err != nil || !created {
		t.Fatal(created, err)
	}
	var state, reason, body, taskState string
	if err := testPool.QueryRow(ctx, `SELECT n.state,n.reason,n.body,t.state FROM employee_run_notice n JOIN employee_task t ON t.id=n.task_id WHERE n.run_id=$1::uuid`, runID).Scan(&state, &reason, &body, &taskState); err != nil {
		t.Fatal(err)
	}
	if state != "enqueued" || reason != "" || body != "简短版资料已整理。" || taskState != "succeeded" {
		t.Fatal("human response final result lost source/effect proof", state, reason, body, taskState)
	}
}
