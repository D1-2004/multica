package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

// A DingTalk quote reply carrying a new request was refused as "continuation"
// and answered 「这次没能完成受理，请稍后再试。」 (2026-10-03, Qwen-DWS group,
// 「创建新的例行任务，每隔30分钟讲个笑话」 quoting the routine list).
func TestEmployeeSceneDispatchAcceptsQuoteReply(t *testing.T) {
	f, model, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Messages[0].Text = "创建新的例行任务，每隔30分钟讲个笑话"
	f.command.Event.Data.Messages[0].ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: "bot-routine-list", Text: "本群目前有两条例行任务", SenderUID: "employee-uid"}
	model.dispatch = true
	if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != 202 {
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
	var raw, queueContext []byte
	var requester string
	if err := testPool.QueryRow(context.Background(), `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedOutcome
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Outcome.Kind != employeeloop.Dispatched || saved.Failure != "" || strings.Contains(saved.Outcome.Reply, "没能完成受理") {
		t.Fatalf("quote reply refused: %s", raw)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT t.requester_ref,q.context FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE t.agent_id=$1`, f.agentID).Scan(&requester, &queueContext); err != nil {
		t.Fatal(err)
	}
	if requester != "dingtalk:456:open_id:requester-open-id" {
		t.Fatalf("quoted author became the requester: %q", requester)
	}
	var compiled struct {
		Prompt string `json:"direct_task_prompt"`
	}
	if err := json.Unmarshal(queueContext, &compiled); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.Prompt, "本群目前有两条例行任务") || !strings.Contains(compiled.Prompt, "每隔30分钟讲个笑话") {
		t.Fatalf("executor lost the request or its quoted material: %q", compiled.Prompt)
	}
}

func TestEmployeeHistoryConfigLinksLeaveNoLinkShape(t *testing.T) {
	for _, text := range []string{
		"配置页面在这里。\n\n[本群能力配置](dingtalk://dingtalkclient/page/link?url=https%3A%2F%2Fpre.example%2Fdingtalk%2Fconfigure%3Flink%3Dabc_123&pc_slide=true)（30 分钟内有效）",
		"配置页面在这里。\n\n[本群能力配置]([configuration link])（30 分钟内有效）",
	} {
		got := employeeHistoryConfigLinks(text)
		if strings.Contains(got, "](") || strings.Contains(got, "configure") || strings.Contains(got, "[configuration link]") || !strings.Contains(got, employeeOmittedConfigLink) || !strings.HasPrefix(got, "配置页面在这里。") {
			t.Fatalf("history keeps a copyable link: %q", got)
		}
	}
	if got := employeeHistoryConfigLinks("普通回复（说明）"); got != "普通回复（说明）" {
		t.Fatalf("plain text changed: %q", got)
	}
}

// The model answered a quoted 「配置页面告诉我」 by copying its earlier answer,
// including the redacted link, so the group got a link to "[configuration link]".
func TestEmployeeSceneCopiedConfigLinkIsReplacedOnce(t *testing.T) {
	f, _, dc := employeeFixture(t)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Conversation.Type = "group"
	f.h.cfg.AppURL = "https://app.multica.example"
	if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != 202 {
		t.Fatal(r.Body.String())
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM context_config_link WHERE agent_id=$1`, f.agentID)
	})
	copied := "配置页面在这里，30 分钟内有效。\n\n[本群能力配置]([configuration link])（30 分钟内有效）"
	calls := 0
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": copied}}}})
		var result openai.ChatCompletion
		return &result, json.Unmarshal(raw, &result)
	})
	verify := func() string {
		t.Helper()
		var raw []byte
		var links int
		if err := testPool.QueryRow(context.Background(), `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_config_link WHERE agent_id=$1`, f.agentID).Scan(&links); err != nil {
			t.Fatal(err)
		}
		var saved employeeSavedOutcome
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		reply := saved.Outcome.Reply
		if links != 1 || strings.Contains(reply, "[configuration link]") || strings.Count(reply, "dingtalkclient/page/link") != 1 || !strings.HasPrefix(reply, "配置页面在这里") {
			t.Fatalf("links=%d reply=%q", links, reply)
		}
		return reply
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	first := verify()
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := verify(); got != first || calls != 1 {
		t.Fatalf("replay minted again or reran the model: calls=%d", calls)
	}
}
