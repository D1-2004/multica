package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeMemoryV2ToolsGatedByMemoryMarker(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	host, _, _ := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "gate", Text: "记住 color=BLUE", SenderOpenDingTalkID: "requester-open-id"}})
	worker := host.worker
	v1 := employeeSceneToolsForMemory(false)
	for name, ready := range map[string]func(context.Context) (bool, error){
		"unwired":   nil,
		"not_ready": func(context.Context) (bool, error) { return false, nil },
		"error":     func(context.Context) (bool, error) { return true, errors.New("fence unavailable") },
		"ready":     func(context.Context) (bool, error) { return true, nil },
	} {
		worker.MemoryToolsReady = ready
		input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(input)
		var frozen employeeSavedInput
		_ = json.Unmarshal(raw, &frozen)
		if got := employeeMemoryToolsV2Frozen(frozen); got != (name == "ready") {
			t.Fatalf("%s: v2=%v", name, got)
		}
		if len(input.Config.Tools) != len(v1) {
			t.Fatalf("%s: tool count changed", name)
		}
		for i, tool := range input.Config.Tools {
			if tool.Name != v1[i].Name {
				t.Fatalf("%s: tool order changed at %d", name, i)
			}
			before, _ := json.Marshal(v1[i])
			after, _ := json.Marshal(tool)
			if name != "ready" && string(before) != string(after) {
				t.Fatalf("%s: v1 schema changed for %s", name, tool.Name)
			}
			if !isEmployeeMemoryTool(tool.Name) && string(before) != string(after) {
				t.Fatalf("%s: non-memory tool %s changed", name, tool.Name)
			}
		}
	}
	// v1 schema stays byte-identical to the pre-v2 enum.
	raw, _ := json.Marshal(employeeMemoryTools()[0].Schema["properties"].(map[string]any)["type"])
	if string(raw) != `{"enum":["pattern","pitfall","preference","architecture","tool","operational"],"type":"string"}` {
		t.Fatalf("v1 type enum drifted: %s", raw)
	}
	// A job frozen with v1 keeps v1 after every replica supports v2, and its
	// replay requests no new generation.
	worker.MemoryToolsReady = func(context.Context) (bool, error) { return false, nil }
	input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	if _, err = worker.store.SaveInput(ctx, host.job, encoded); err != nil {
		t.Fatal(err)
	}
	source := employeeSourceMessages(host.job.Items[0], host.envelopes[0])[0]
	calls := 0
	model := employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		tools, _ := json.Marshal(p.Tools)
		if strings.Contains(string(tools), `"audience"`) {
			t.Fatal("frozen v1 job offered v2 tools")
		}
		if calls == 1 {
			return employeeReplyCompletion(t, employeeReplyCall(t, "v1-capture", "memory_capture", map[string]any{"source_ref": source.SourceRef, "key": "color", "type": "preference", "quote": "color=BLUE"})), nil
		}
		return employeeMemoryAnswer(t), nil
	})
	worker.MemoryToolsReady = func(context.Context) (bool, error) { return true, nil }
	if _, err = employeeloop.New(input.Config, &employeeJournalModel{store: worker.store, job: host.job, delegate: model}, host).Run(ctx, input.Input); err != nil {
		t.Fatal(err)
	}
	if err = worker.store.Retry(ctx, host.job, "restart"); err != nil {
		t.Fatal(err)
	}
	_, _ = testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID)
	worker.model = model
	if _, err = worker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var failure string
	if err = testPool.QueryRow(ctx, `SELECT COALESCE(outcome->>'failure','') FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&failure); err != nil || failure != "" || calls != 2 {
		t.Fatalf("v1 job replay: failure=%q calls=%d %v", failure, calls, err)
	}
	if rec := employeeMemoryRecord(t, f, "color"); rec.Insight != "color=BLUE" {
		t.Fatalf("v1 capture: %+v", rec)
	}
}

func TestEmployeeMemoryV2RefusalLetsModelReply(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "single")
	f.h.EmployeeSceneWorker.MemoryToolsReady = func(context.Context) (bool, error) { return true, nil }
	calls := employeeMemoryTurn(t, f, dc, "记一下：周报周五交", func(n int, source string, p openai.ChatCompletionNewParams) *openai.ChatCompletion {
		if n == 1 {
			tools, _ := json.Marshal(p.Tools)
			if !strings.Contains(string(tools), `"audience"`) {
				t.Fatal("ready worker froze v1 tools")
			}
			return employeeReplyCompletion(t, employeeReplyCall(t, "bad-quote", "memory_capture", map[string]any{"source_ref": source, "audience": "scene", "type": "fact", "subject": "周报", "quote": "周报周四交"}))
		}
		raw, _ := json.Marshal(p.Messages)
		if !strings.Contains(string(raw), "exact excerpt") {
			t.Fatalf("model did not see the refusal: %s", raw)
		}
		return employeeMemoryAnswer(t)
	})
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	var failure, reply string
	if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(outcome->>'failure',''),COALESCE(outcome#>>'{outcome,Reply}','') FROM employee_scene_job WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, f.agentID).Scan(&failure, &reply); err != nil || failure != "" || reply == "" {
		t.Fatalf("refusal ended the wake: failure=%q reply=%q %v", failure, reply, err)
	}
	if n := memoryRowCount(t, f); n != 0 {
		t.Fatalf("refused capture wrote %d rows", n)
	}
}

// Memory eval bindings (employee_memory_eval_test.go). Shared scene capture has
// no runtime switch: it is on for every employee-mode agent once the tools are
// v2, and tools v2 follow the replica marker, which these bindings report as
// supported by every live replica.
func init() {
	memoryEvalEnable[memoryEvalSceneCapture] = func(*testing.T, memoryEvalTarget) bool { return true }
	memoryEvalEnable[memoryEvalToolsV2] = func(_ *testing.T, target memoryEvalTarget) bool {
		target.Worker.MemoryToolsReady = func(context.Context) (bool, error) { return true, nil }
		return true
	}
}
