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

func TestEmployeeReplyProtocolCannotReachOutbox(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal_stop_repair", true: "recovered_outcome"}[recovered], func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			f.command.CompletionCallback, f.command.ResponsePolicy = nil, nil
			if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
				t.Fatal(response.Body.String())
			}
			ctx := context.Background()
			var receipt, jobID string
			if err := testPool.QueryRow(ctx, `SELECT receipt_id::text,job_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt, &jobID); err != nil {
				t.Fatal(err)
			}
			leak := `<reply>先查任务。<find_tasks source_ref="` + receipt + `/message-1"></find_tasks></reply>`
			if recovered {
				// A tool added by the frozen snapshot must remain reserved even
				// when it is absent from the current static scene inventory.
				leak = "<snapshot_extra_tool>内部调用</snapshot_extra_tool>"
				raw, _ := json.Marshal(employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: leak}}})
				snapshot, _ := json.Marshal(employeeSavedInput{Config: employeeloop.Config{Tools: []employeeloop.Tool{{Name: "snapshot_extra_tool"}}}})
				if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET outcome=$2::jsonb,input_snapshot=$3::jsonb WHERE id=$1::uuid`, jobID, raw, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				if recovered {
					t.Fatal("recovery repeated a model request")
				}
				if calls == 1 {
					var out openai.ChatCompletion
					raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": leak}}}})
					if err := json.Unmarshal(raw, &out); err != nil {
						t.Fatal(err)
					}
					return &out, nil
				}
				return employeeReplyCompletion(t, employeeReplyCall(t, "reply-safe", "reply", map[string]any{"source_ref": receipt + "/message-1", "reply": "没有找到此前的创建任务。"})), nil
			})
			if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
				t.Fatal(worked, err)
			}
			var text string
			if err := testPool.QueryRow(ctx, `SELECT input->>'text' FROM response_action WHERE input->>'employee_message_job_id'=$1`, jobID).Scan(&text); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "<") || strings.Contains(text, receipt) || text == "" || (!recovered && calls != 2) {
				t.Fatal(text, calls)
			}
			assertEmployeeReplyNoTasks(t, f)
		})
	}
}
