package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

type employeeReplyModelFunc func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)

func (f employeeReplyModelFunc) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	return f(ctx, p)
}

func employeeReplyCompletion(t *testing.T, calls ...map[string]any) *openai.ChatCompletion {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": calls}}}})
	if err != nil {
		t.Fatal(err)
	}
	var result openai.ChatCompletion
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}
func employeeReplyCall(t *testing.T, id, name string, args map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}
}

func TestEmployeeSceneReplyToolNeverCreatesTask(t *testing.T) {
	f, _, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Messages[0].Text = "从已有记忆回答口令，不要创建新任务。"
	response := employeeHTTP(t, f, dc, uuid.NewString())
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		return employeeReplyCompletion(t, employeeReplyCall(t, "reply-1", "reply", map[string]any{"source_ref": receipt + "/message-1", "reply": "MEM-blue"})), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worker=%v err=%v", worked, err)
	}
	var outcome []byte
	if err := testPool.QueryRow(context.Background(), `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(string(outcome), `"Reply": "MEM-blue"`) {
		t.Fatalf("calls=%d outcome=%s", calls, outcome)
	}
	assertEmployeeReplyNoTasks(t, f)
	// Recovery must reuse the recorded reply and never invent a background task.
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("recovery made %d model calls", calls)
	}
	assertEmployeeReplyNoTasks(t, f)
}

func TestEmployeeSceneMixedReplyAndDispatchHasNoEffects(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatch_first", true: "reply_first"}[replyFirst], func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			response := employeeHTTP(t, f, dc, uuid.NewString())
			if response.Code != http.StatusAccepted {
				t.Fatal(response.Body.String())
			}
			var receipt string
			if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				reply := employeeReplyCall(t, "reply", "reply", map[string]any{"source_ref": receipt + "/message-1", "reply": "MEM-blue"})
				dispatch := employeeReplyCall(t, "dispatch", "dispatch_task", map[string]any{"source_ref": receipt + "/message-1", "goal": "Analyze feedback", "prompt": "Analyze feedback and report the evidence", "reply": "我来分析这些反馈。"})
				if replyFirst {
					return employeeReplyCompletion(t, reply, dispatch), nil
				}
				return employeeReplyCompletion(t, dispatch, reply), nil
			})
			if _, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertEmployeeReplyNoTasks(t, f)
			if calls != employeeloop.MaxModelCalls {
				t.Fatalf("invalid batch consumed %d model calls, want bounded repair budget %d", calls, employeeloop.MaxModelCalls)
			}
		})
	}
}

func assertEmployeeReplyNoTasks(t *testing.T, f *dingTalkResponseFixture) {
	t.Helper()
	for _, table := range []string{"employee_task", "employee_task_run"} {
		var count int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND id<>$2`, f.agentID, f.taskID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new queue tasks=%d err=%v", count, err)
	}

}
