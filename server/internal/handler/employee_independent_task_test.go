package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

// The model response is scripted: this checks the dispatch plumbing does not
// merge an explicitly new request into its similar candidate, not model intent.
func TestEmployeeIndependentTaskDispatchKeepsOldTask(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	var taskID string
	var version int64
	if err := testPool.QueryRow(ctx, `SELECT id::text,version FROM employee_task WHERE agent_id=$1`, f.agentID).Scan(&taskID, &version); err != nil {
		t.Fatal(err)
	}
	source := employeeSecondRequest(t, f, "independent-task", "开一个独立的新任务，实际运行 Python 求 1 到 10 的总和，保留之前的任务")
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(p.Messages)
		if !strings.Contains(string(raw), "independent new Task") {
			t.Error("wake did not freeze independent-task priority")
		}
		return employeeReplyCompletion(t, employeeReplyCall(t, "new-task", "dispatch_task", map[string]any{"source_ref": source, "goal": "独立运行 Python 求和", "prompt": "实际运行 Python 求 1 到 10 的总和，保留之前的任务", "reply": "我来新开任务执行，完成后发你。"})), nil
	})
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var tasks, oldRuns int
	var state string
	var nowVersion int64
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task WHERE agent_id=$1`, f.agentID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT state,version,(SELECT count(*) FROM employee_task_run WHERE task_id=t.id) FROM employee_task t WHERE id=$1`, taskID).Scan(&state, &nowVersion, &oldRuns); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 || state != "succeeded" || version != nowVersion || oldRuns != 1 || calls != 1 {
		t.Fatalf("tasks=%d old state=%s version %d->%d runs=%d calls=%d", tasks, state, version, nowVersion, oldRuns, calls)
	}
	var prompt string
	if err := testPool.QueryRow(ctx, `SELECT q.context->>'direct_task_prompt' FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE t.agent_id=$1 AND t.id<>$2`, f.agentID, taskID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "实际运行 Python") {
		t.Fatal("new execution instruction lost method")
	}
}
