package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	openai "github.com/openai/openai-go/v3"
)

type employeeSteerModel struct {
	calls     int
	sourceRef string
}

func (m *employeeSteerModel) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	args, _ := json.Marshal(map[string]any{"source_ref": m.sourceRef, "correction": "不对，只统计已签约客户", "reply": "收到，已按新要求调整正在进行的任务。"})
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-steer", "type": "function", "function": map[string]any{"name": "steer_task", "arguments": string(args)}}}}}}})
	var result openai.ChatCompletion
	err := json.Unmarshal(raw, &result)
	return &result, err
}

// A follow-up message from the same requester steers the running Direct task
// through EmployeeLoop: one model call, no new EmployeeTask, the old run is
// exit-fenced and the successor answers the correction message.
func TestEmployeeLoopSteerTaskToolInterruptsRequesterTask(t *testing.T) {
	f := employeeNoticeDatabase(t, "running", false, false)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET status='online' WHERE id=(SELECT runtime_id FROM agent_task_queue WHERE id=$1::uuid)`, f.queueID); err != nil {
		t.Fatal(err)
	}
	var endpointID string
	var namespaceID pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id FROM agent_dispatch_endpoint WHERE agent_id=$1::uuid`, f.agentID).Scan(&endpointID, &namespaceID); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: endpointID, EndpointNamespaceID: namespaceID, UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	model := &employeeSteerModel{}
	f.h.EmployeeSceneWorker = NewEmployeeSceneWorker(f.h, model)
	f.h.EmployeeSceneWorker.ReplicaReady = func(context.Context) error { return nil }
	f.command.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "message-2", Text: "不对，只统计已签约客户"}}
	if w := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var jobID string
	var items []employeeentry.Item
	if err := testPool.QueryRow(ctx, `SELECT id::text,items FROM employee_scene_job WHERE agent_id=$1::uuid AND id<>$2::uuid`, f.agentID, f.jobID).Scan(&jobID, &items); err != nil {
		t.Fatal(err)
	}
	model.sourceRef = items[0].ReceiptID + "/message-2"
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls=%d", model.calls)
	}
	old, err := f.h.Queries.GetAgentTask(ctx, parseUUID(f.queueID))
	if err != nil || old.Status != "cancelled" {
		t.Fatalf("original run: %+v %v", old, err)
	}
	var tasks, runs int
	var successorContext []byte
	if err = testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid),(SELECT count(*) FROM employee_task_run WHERE agent_id=$1::uuid),(SELECT q.context FROM agent_task_queue q WHERE q.context->>'steer_predecessor_task_id'=$2)`, f.agentID, f.queueID).Scan(&tasks, &runs, &successorContext); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || runs != 2 {
		t.Fatalf("steer created new work: tasks=%d runs=%d", tasks, runs)
	}
	var private map[string]any
	if err = json.Unmarshal(successorContext, &private); err != nil {
		t.Fatal(err)
	}
	if private["employee_job_id"] != jobID || private["employee_source_ref"] != model.sourceRef || !strings.Contains(private["direct_task_prompt"].(string), "不对，只统计已签约客户") {
		t.Fatalf("successor does not answer the correction: %s", successorContext)
	}
	var outcome []byte
	if err = testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1::uuid`, jobID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(outcome), "收到，已按新要求调整") {
		t.Fatalf("acknowledgement not recorded: %s", outcome)
	}
}
