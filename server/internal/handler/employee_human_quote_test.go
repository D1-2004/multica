package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/humanquestion"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/openai/openai-go/v3"
)

func humanQuotedHost(t *testing.T, channel, fault string) (*dingTalkResponseFixture, *employeeSceneHost, employeeSourceMessage, humanquestion.Question, employeeSavedInput) {
	t.Helper()
	f, dc, q := humanCardFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1::uuid,$2::uuid,$3,$4,$5::uuid)`, f.agentID, q.Scope.WorkspaceID, f.command.ExternalIdentity.DWS.UID, q.Scope.TenantOrgID, q.PrincipalID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1::uuid`, f.agentID)
	})
	conversation := f.command.Event.Data.Conversation.OpenConversationID
	if channel == "response" {
		if _, err := testPool.Exec(ctx, `UPDATE response_action SET provider_message_id='quoted-human-card',provider_conversation_id=$2,state='delivered' WHERE id=$1`, q.ActionID, conversation); err != nil {
			t.Fatal(err)
		}
	}
	if channel == "interaction" {
		if _, err := testPool.Exec(ctx, `UPDATE a2ui_interaction SET message_id='quoted-human-card' WHERE id=$1::uuid`, q.ID); err != nil {
			t.Fatal(err)
		}
	}
	m := f.command.Event.Data.Messages[0]
	m.SenderOpenDingTalkID = q.OperatorOpenID
	m.OpenMsgID = "quoted-human-source"
	m.Text = "先不管"
	m.ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: "quoted-human-card", Text: "[互动卡片]"}
	if fault == "ordinary" {
		m.ReferencedMessage.Text = "原始请求"
	}
	provider := newFakeResourceDWS()
	outerSender := m.SenderOpenDingTalkID
	quotedID := "quoted-human-card"
	sender := "employee-card-sender"
	switch fault {
	case "outer_sender":
		outerSender = "other-person"
	case "quote_id":
		quotedID = "other-card"
	case "quoted_sender":
		sender = outerSender
	}
	provider.messages[m.OpenMsgID] = providerMessage(conversation, m.OpenMsgID, outerSender, fmt.Sprintf(`{"openMessageId":%q}`, quotedID))
	provider.messages["quoted-human-card"] = providerMessage(conversation, "quoted-human-card", sender, "")
	f.h.EmployeeSceneWorker.ResourceProvider = provider
	host, _, source := employeeMemoryHost(t, f, dc, []DispatchMessage{m})
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input)
	if _, err = host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	return f, host, source, q, input
}

func TestEmployeeHumanQuoteFrozenIdentityAndProviderAuthority(t *testing.T) {
	for _, tc := range []struct{ channel, fault, want string }{
		{"response", "", "exact"}, {"interaction", "", "exact"}, {"none", "", "unresolved"},
		{"response", "outer_sender", "unresolved"}, {"response", "quote_id", "unresolved"}, {"response", "quoted_sender", "unresolved"},
		{"none", "ordinary", "not_question"},
	} {
		t.Run(tc.channel+"/"+tc.fault, func(t *testing.T) {
			_, host, source, q, saved := humanQuotedHost(t, tc.channel, tc.fault)
			if len(saved.HumanQuotes) != 1 || saved.HumanQuotes[0].Outcome != tc.want {
				t.Fatalf("bindings=%+v", saved.HumanQuotes)
			}
			if tc.want == "exact" && (saved.HumanQuotes[0].QuestionRef != q.ID || !strings.Contains(saved.Input.TaskBrief, `"quoted_human_questions"`)) {
				t.Fatal("exact binding missing from model context")
			}
			tx, err := testPool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			err = host.requireHumanQuote(context.Background(), tx, source, q.ID)
			if (err == nil) != (tc.want == "exact" || tc.want == "not_question") {
				t.Fatal("guard outcome", err)
			}
			if tc.want == "exact" && host.requireHumanQuote(context.Background(), tx, source, uuid.NewString()) == nil {
				t.Fatal("quote A authorized question B")
			}
		})
	}
}

func TestEmployeeHumanQuoteNoHotAuthorityAndRecheck(t *testing.T) {
	for _, fault := range []string{"legacy", "changed_message", "other_requester", "other_scene"} {
		t.Run(fault, func(t *testing.T) {
			_, host, source, q, saved := humanQuotedHost(t, "response", "")
			ctx := context.Background()
			switch fault {
			case "legacy":
				saved.HumanQuotes = nil
				raw, _ := json.Marshal(saved)
				if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET input_snapshot=$2 WHERE id=$1::uuid`, host.job.ID, raw); err != nil {
					t.Fatal(err)
				}
			case "changed_message":
				if _, err := testPool.Exec(ctx, `UPDATE response_action SET provider_message_id='another' WHERE id=$1`, q.ActionID); err != nil {
					t.Fatal(err)
				}
			case "other_requester":
				if _, err := testPool.Exec(ctx, `UPDATE employee_human_question SET requester_ref='different' WHERE id=$1::uuid`, q.ID); err != nil {
					t.Fatal(err)
				}
			case "other_scene":
				if _, err := testPool.Exec(ctx, `UPDATE response_action SET provider_conversation_id='other-scene' WHERE id=$1`, q.ActionID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if host.requireHumanQuote(ctx, tx, source, q.ID) == nil {
				t.Fatal("stale/newly derived authority accepted")
			}
		})
	}
}

func TestEmployeeHumanQuoteDisableUsesFrozenExactCard(t *testing.T) {
	_, host, source, q, _ := humanQuotedHost(t, "response", "")
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	result, err := host.disableHumanQuestion(ctx, tx, source, employeeloop.ToolCall{Arguments: map[string]any{"source_ref": source.SourceRef, "question_ref": q.ID, "evidence_quote": "先不管", "reason": "not_needed"}})
	if err != nil || result.Terminal != nil {
		t.Fatal(result, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var projections, responses, wakes int
	if err = testPool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM employee_human_card_projection WHERE question_id=$1::uuid),(SELECT count(*) FROM employee_human_response WHERE question_id=$1::uuid),(SELECT count(*) FROM employee_scene_job WHERE agent_id=$2::uuid AND kind='human_response') FROM employee_human_question WHERE id=$1::uuid`, q.ID, q.Scope.AgentID).Scan(&state, &projections, &responses, &wakes); err != nil || state != "answered" || projections != 1 || responses != 1 || wakes != 0 {
		t.Fatal(state, projections, responses, wakes, err)
	}
}

// The scripted model validates the actual prompt projection and drives two
// separate calls through the production Loop and Host; it is not a real LLM.
type humanQuoteLoopModel struct {
	t                *testing.T
	question, source string
	calls            int
}

func (m *humanQuoteLoopModel) Chat(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	m.calls++
	if m.calls == 1 {
		raw, _ := json.Marshal(p)
		if !strings.Contains(string(raw), m.question) || !strings.Contains(string(raw), "quoted_human_questions") {
			m.t.Fatal("model did not receive exact quoted question")
		}
	}
	name := "disable_human_question"
	args := map[string]any{"source_ref": m.source, "question_ref": m.question, "evidence_quote": "先不管", "reason": "not_needed"}
	if m.calls > 1 {
		name = "reply"
		args = map[string]any{"source_ref": m.source, "reply": "好，先放着。"}
	}
	rawArgs, _ := json.Marshal(args)
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": name, "type": "function", "function": map[string]any{"name": name, "arguments": string(rawArgs)}}}}}}})
	var out openai.ChatCompletion
	err := json.Unmarshal(raw, &out)
	return &out, err
}
func TestEmployeeHumanQuoteScriptedLoopDisableBeforeReply(t *testing.T) {
	_, host, source, q, saved := humanQuotedHost(t, "response", "")
	model := &humanQuoteLoopModel{t: t, question: q.ID, source: source.SourceRef}
	out, err := employeeloop.New(saved.Config, model, host).Run(context.Background(), saved.Input)
	if err != nil || out.Kind != employeeloop.Reply || model.calls != 2 {
		t.Fatal(out, model.calls, err)
	}
	var n int
	if err = testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_human_card_projection WHERE question_id=$1::uuid`, q.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("no original-card close intent", n, err)
	}
}

func TestEmployeeHumanQuoteRejectsDuplicateMessageIdentity(t *testing.T) {
	_, host, source, q, _ := humanQuotedHost(t, "response", "")
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	duplicate := q
	duplicate.ID = uuid.NewString()
	duplicate.PublicID = "ask:" + duplicate.ID
	// Two durable questions claiming one send fact must not pick the first.
	if _, err = humanquestion.StageTx(ctx, tx, duplicate); err != nil {
		t.Fatal(err)
	}
	ids, err := employeeHumanQuoteCandidates(ctx, tx, q.Scope, q.RequesterRef, host.envelopes[0].Command.Event.Data.Conversation.OpenConversationID, "quoted-human-card")
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	if host.requireHumanQuote(ctx, tx, source, q.ID) == nil {
		t.Fatal("ambiguity selected a question")
	}
}
