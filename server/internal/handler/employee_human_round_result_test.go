package handler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

func TestEmployeeHumanRoundResultWaitProjectionAndStaleSendFence(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	f := employeeNoticeDatabase(t, "succeeded", false, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
		f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return true, nil }
	})
	raw := `{"version":"tag-round-result/v1","summary":"两份资料已查到，尚未交付。","choice":{"intent":"clarify","kind":"multiple","question":"需要哪些资料？","options":[{"id":"handbook","label":"员工手册"},{"id":"process","label":"办公流程"}],"allow_custom":true,"min":1,"max":2}}`
	setNoticeExecutionOutput(t, f, raw)
	if created, err := f.h.enqueueEmployeeRunNotice(context.Background(), testWorkspaceID, f.runID); err != nil || !created {
		t.Fatal(created, err)
	}
	var qid, taskState, waitState, body string
	if err := testPool.QueryRow(context.Background(), `SELECT q.id::text,t.state,w.state,n.body FROM employee_human_question q JOIN employee_task t ON t.id=q.task_id JOIN employee_task_wait w ON w.task_id=t.id AND w.ref_id=q.id::text JOIN employee_run_notice n ON n.run_id=q.run_id WHERE q.run_id=$1::uuid`, f.runID).Scan(&qid, &taskState, &waitState, &body); err != nil {
		t.Fatal(err)
	}
	if taskState != "waiting" || waitState != "open" || body != "两份资料已查到，尚未交付。" {
		t.Fatal("run completion incorrectly finished the goal or leaked control JSON", taskState, waitState, body)
	}
	var input []byte
	if err := testPool.QueryRow(context.Background(), `SELECT input FROM response_action WHERE input->'a2ui_card'->>'question_id'=$1`, qid).Scan(&input); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if json.Unmarshal(input, &in) != nil || in.A2UICard == nil || len(in.A2UICard.Messages) != 2 {
		t.Fatal(string(input))
	}
	in.ActionID = ""
	if err := testPool.QueryRow(context.Background(), `SELECT action_id FROM employee_human_question WHERE id=$1::uuid`, qid).Scan(&in.ActionID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.BeforeEmployeeHumanQuestionSend(context.Background(), in); err != nil {
		t.Fatal("valid card refused", err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=(SELECT task_id FROM employee_human_question WHERE id=$1::uuid)`, qid); err != nil {
		t.Fatal(err)
	}
	err := f.h.BeforeEmployeeHumanQuestionSend(context.Background(), in)
	var suppressed *dingtalkresponse.SuppressSendError
	if !errors.As(err, &suppressed) {
		t.Fatal("stale pending card was not suppressed", err)
	}
}

func TestEmployeeHumanRoundResultCompletedSuggestionAndMalformedBoundaries(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	for _, tc := range []struct {
		name, output, state string
		questions           int
	}{
		{"complete", `{"version":"tag-round-result/v1","summary":"资料已整理。"}`, "succeeded", 0},
		{"suggest", `{"version":"tag-round-result/v1","summary":"资料已整理。","choice":{"intent":"suggest","kind":"single","question":"还需要哪一步？","options":[{"id":"checklist","label":"制作清单"},{"id":"summary","label":"制作摘要"}]}}`, "succeeded", 1},
		{"malformed", `{"version":"tag-round-result/v1","summary":"完成","task_id":"other"}`, "ready", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
				f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return true, nil }
			})
			setNoticeExecutionOutput(t, f, tc.output)
			if created, err := f.h.enqueueEmployeeRunNotice(context.Background(), testWorkspaceID, f.runID); err != nil || !created {
				t.Fatal(created, err)
			}
			var state string
			var questions int
			if err := testPool.QueryRow(context.Background(), `SELECT t.state,(SELECT count(*) FROM employee_human_question q WHERE q.task_id=t.id) FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE r.id=$1::uuid`, f.runID).Scan(&state, &questions); err != nil {
				t.Fatal(err)
			}
			if state != tc.state || questions != tc.questions {
				t.Fatal("unverified result claimed completion or generated choices", state, questions)
			}
		})
	}
}
