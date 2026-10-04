package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func TestEmployeeCanonicalCompleteMaterializeAndQuoteContinue(t *testing.T) {
	for _, mode := range []string{"complete", "suggest", "duplicate", "unknown", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := employeeNoticeDatabase(t, "running", false, false, func(f *dingTalkResponseFixture, _ *employeeTestModel) {
				f.h.EmployeeSceneWorker.HumanQuestionsReady = func(context.Context) (bool, error) { return true, nil }
			})
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_human_response WHERE question_id IN (SELECT id FROM employee_human_question WHERE agent_id=$1)`, f.agentID)
				_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_human_question WHERE agent_id=$1`, f.agentID)
			})
			summary := "first\nsecond\t\"quoted\" C:\\new\\repo literal\\n"
			body, _ := json.Marshal(map[string]any{"version": "tag-round-result/v1", "summary": summary})
			output := string(body)
			switch mode {
			case "suggest":
				output = strings.TrimSuffix(output, "}") + `,"choice":{"intent":"suggest","kind":"single","question":"还需要什么？","options":[{"id":"week","label":"一周"},{"id":"table","label":"表格"}]}}`
			case "duplicate":
				output = strings.Replace(output, `"version":`, `"version":"other","version":`, 1)
			case "unknown":
				output = strings.TrimSuffix(output, "}") + `,"task_id":"forged"}`
			case "malformed":
				output = strings.TrimSuffix(output, "}")
			}
			payload, _ := json.Marshal(map[string]string{"output": output})
			if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(f.queueID), payload, "", "", false, ""); err != nil {
				t.Fatal(err)
			}
			var stored, taskID, goal string
			if err := testPool.QueryRow(ctx, `SELECT r.result,t.id::text,t.state FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE r.id=$1`, f.runID).Scan(&stored, &taskID, &goal); err != nil {
				t.Fatal(err)
			}
			if mode == "malformed" && json.Valid([]byte(stored)) {
				t.Fatal("malformed result was repaired", stored)
			}
			if mode != "malformed" && stored != output {
				t.Fatal("CompleteTask altered machine result before strict materialization", stored)
			}
			if goal != "ready" {
				t.Fatal("queue completion prematurely completed the explicit Goal", goal)
			}
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, f.runID); err != nil {
				t.Fatal(err)
			}
			var questions int
			if err := testPool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM employee_human_question WHERE task_id=t.id) FROM employee_task t WHERE id=$1`, taskID).Scan(&goal, &questions); err != nil {
				t.Fatal(err)
			}
			valid := mode == "complete" || mode == "suggest"
			if !valid {
				if goal != "ready" || questions != 0 {
					t.Fatal("malformed/unknown/duplicate result was accepted", goal, questions)
				}
				return
			}
			wantQuestions := 0
			if mode == "suggest" {
				wantQuestions = 1
			}
			if goal != "succeeded" || questions != wantQuestions {
				t.Fatal("valid result failed Goal completion", goal, questions)
			}
			// Anchor the next real-admission source to the verified accepted work.
			conversation := f.command.Event.Data.Conversation.OpenConversationID
			if tag, err := testPool.Exec(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=$3,provider_message_id='canonical-ack' WHERE agent_id=$1 AND input->>'scene_notice_id'=$2`, f.agentID, f.jobID, conversation); err != nil || tag.RowsAffected() != 1 {
				t.Fatal("quote anchor unavailable", tag.RowsAffected(), err)
			}
			var endpoint, namespace string
			if err := testPool.QueryRow(ctx, `SELECT endpoint_id,id::text FROM agent_dispatch_endpoint WHERE agent_id=$1 LIMIT 1`, f.agentID).Scan(&endpoint, &namespace); err != nil {
				t.Fatal(err)
			}
			dc := agentDispatchContext{EndpointID: endpoint, EndpointNamespaceID: parseUUID(namespace), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
			message := f.command.Event.Data.Messages[0]
			message.OpenMsgID, message.Text = "canonical-followup", "一周的，给我一个表格。"
			message.ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: "canonical-ack", Text: "已受理这项工作。"}
			provider := newFakeResourceDWS()
			provider.messages[message.OpenMsgID] = providerMessage(conversation, message.OpenMsgID, quoteRequesterOpenID, fmt.Sprintf(`{"openMessageId":%q}`, "canonical-ack"))
			provider.messages["canonical-ack"] = providerMessage(conversation, "canonical-ack", quoteEmployeeOpenID, "")
			worker := f.h.EmployeeSceneWorker
			worker.ResourceProvider = provider
			host, id, source := employeeMemoryHost(t, f.dingTalkResponseFixture, dc, []DispatchMessage{message})
			input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(input)
			if _, err := worker.store.SaveInput(ctx, host.job, raw); err != nil {
				t.Fatal(err)
			}
			if bindings := employeeQuotedBindings(t, host); len(bindings) != 1 || bindings[0].TaskID != taskID {
				t.Fatal("quote chose a different work item", bindings)
			}
			read := employeeQuoteCall("read_task", "canonical-read", source, "q1", nil)
			if _, err := host.Execute(ctx, id, read); err != nil {
				t.Fatal(err)
			}
			call := employeeQuoteCall("continue_task", "canonical-continue", source, "q1", map[string]any{"read_ref": "canonical-read", "instruction_quote": source.Message.Text, "prompt": "实际查询最近一周的提交并输出表格", "reply": "我来重新查询并整理表格。"})
			out, err := host.Execute(ctx, id, call)
			if err != nil || out.Receipt == "" || out.Terminal == nil || out.Terminal.Kind != employeeloop.Dispatched {
				t.Fatal("valid multi-line completion prevented continuation", out, err)
			}
			if replay, err := host.Execute(ctx, id, call); err != nil || replay.Receipt != out.Receipt {
				t.Fatal("continuation replay created a different effect", replay, err)
			}
			participationComplete(t, host, employeeloop.Outcome{Decision: *out.Terminal, Receipts: []employeeloop.Receipt{{NativeToolCallID: call.NativeToolCallID, ToolName: call.Name, ID: out.Receipt}}, ToolOutcomes: []employeeloop.ToolOutcome{{NativeToolCallID: call.NativeToolCallID, ToolName: call.Name, Result: out}}})
			var runs int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE task_id=$1`, taskID).Scan(&runs); err != nil || runs != 2 {
				t.Fatal("continuation was not one new Run", runs, err)
			}
			var nextQueue, contract, prompt string
			var revision int
			if err := testPool.QueryRow(ctx, `SELECT r.queue_task_id::text,r.goal_revision,q.context->>'employee_round_result_contract',q.context->>'direct_task_prompt' FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.task_id=$1 AND r.id<>$2`, taskID, f.runID).Scan(&nextQueue, &revision, &contract, &prompt); err != nil {
				t.Fatal(err)
			}
			if revision != 2 || contract != "tag-round-result/v1" || !strings.Contains(prompt, "tag-round-result/v1") || !strings.Contains(prompt, source.Message.Text) {
				t.Fatal("next revision lost current source or its result contract", revision, contract, prompt)
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, nextQueue); err != nil {
				t.Fatal(err)
			}
			second, _ := json.Marshal(map[string]string{"version": "tag-round-result/v1", "summary": "一周表格\n|date|commit|\n|today|abc|"})
			secondPayload, _ := json.Marshal(map[string]string{"output": string(second)})
			if _, err := f.h.TaskService.CompleteTask(ctx, parseUUID(nextQueue), secondPayload, "", "", false, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, out.Receipt); err != nil {
				t.Fatal(err)
			}
			if err := testPool.QueryRow(ctx, `SELECT state,goal_revision FROM employee_task WHERE id=$1`, taskID).Scan(&goal, &revision); err != nil || goal != "succeeded" || revision != 2 {
				t.Fatal("second structured completion left Goal stuck", goal, revision, err)
			}
		})
	}
}
