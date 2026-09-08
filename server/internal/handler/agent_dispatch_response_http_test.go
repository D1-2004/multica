package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// Keep this payload independent of DispatchCommand serialization: Router is a
// separate service, and its wire DTO must survive both decode and conversion.
func responseHTTPPayload(agentID, taskID, policy string) []byte {
	policyField := ""
	if policy != "" {
		policyField = `,"responsePolicy":` + policy
	}
	return []byte(`{"schemaVersion":"2.0","agentId":"` + agentID + `",
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{"domain":"channel","type":"message.created","data":{
			"conversation":{"openConversationId":"cid-` + taskID + `","type":"group"},
			"sender":{"displayName":"Requester","openDingTalkId":"requester-open-id"},
			"messages":[{"openMsgId":"message-1","text":"Please reply with the reviewed schedule"}]}},
		"surface":{"type":"issue"},"outbound":{"mode":"dws","replyTo":"latest_message"},
		"externalIdentity":{"dws":{"uid":"123","orgId":"456"}},
		"completionCallback":{"url":"/api/v1/dispatch-tasks/` + taskID + `/execution-result",
			"updateUrl":"/api/v1/dispatch-tasks/` + taskID + `/execution-update",
			"responseUrl":"/api/v1/dispatch-tasks/` + taskID + `/response-receipt"}` + policyField + `}`)
}

func TestAgentDispatchResponseHTTPRejectsMalformedPolicy(t *testing.T) {
	for _, tc := range []struct{ name, policy string }{
		{"unknown version", `{"version":2,"mode":"multica_coordinator","revision":9}`},
		{"unknown mode", `{"version":1,"mode":"future_mode","revision":9}`},
		{"missing revision", `{"version":1,"mode":"multica_coordinator"}`},
		{"negative revision", `{"version":1,"mode":"multica_coordinator","revision":-1}`},
		{"invalid flag type", `{"version":1,"mode":"multica_coordinator","revision":9,"showAiTag":"false"}`},
		{"invalid policy type", `"multica_coordinator"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := responseHTTPPayload("11111111-1111-4111-8111-111111111111", "malformed-wire", tc.policy)
			h := &Handler{TaskCompletionTargetIdentity: testRouterTargetIdentity}
			w := httptest.NewRecorder()
			h.handleAgentDispatchV2(w, httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewReader(raw)), raw, agentDispatchContext{})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("malformed policy was not rejected before any persistence: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

type responseHTTPNoExternalProvider struct{ calls int }

func (p *responseHTTPNoExternalProvider) Send(context.Context, dingtalkresponse.ActionInput, string) (dwsclient.SendResult, error) {
	p.calls++
	return dwsclient.SendResult{}, errors.New("test must not execute DWS")
}

func (p *responseHTTPNoExternalProvider) Query(context.Context, dingtalkresponse.ActionInput, string) (dwsclient.SendStatus, error) {
	p.calls++
	return dwsclient.SendStatus{}, errors.New("test must not query DWS")
}

func TestAgentDispatchResponseHTTPAcceptanceToReplyAction(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		managed      bool
	}{
		{"managed", `{"version":1,"mode":"multica_coordinator","revision":9,"showAiTag":true}`, true},
		{"legacy missing policy", "", false},
		{"legacy explicit", `{"version":1,"mode":"legacy","revision":9}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
			ctx := context.Background()
			t.Cleanup(func() {
				if _, err := testPool.Exec(ctx, `DELETE FROM agent_dispatch_acceptance WHERE agent_id=$1`, f.agentID); err != nil {
					t.Error(err)
				}
			})
			provider := &responseHTTPNoExternalProvider{}
			f.h.DingTalkResponses = dingtalkresponse.NewService(testPool, provider, nil)
			chat := &responseTestCompleter{}
			f.h.InboundCoordinator = &inboundcoord.Coordinator{LLM: llm.New(llm.Config{APIKey: "test", BaseURL: "http://127.0.0.1:1"}), Chat: chat}
			f.h.InboundCoordinatorWorker = NewInboundCoordinatorJobWorker(f.h)
			routerTaskID := "http-response-" + uuid.NewString()
			raw := responseHTTPPayload(f.agentID, routerTaskID, tc.policy)
			scope := agentDispatchContext{EndpointID: "response-test-endpoint", EndpointNamespaceID: parseUUID(uuid.NewString()), UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
			w := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/dispatch", bytes.NewReader(raw))
			request.Header.Set("Idempotency-Key", routerTaskID)
			f.h.handleAgentDispatchV2(w, request, raw, scope)
			if w.Code != http.StatusAccepted {
				t.Fatalf("HTTP acceptance failed: status=%d body=%s", w.Code, w.Body.String())
			}
			if provider.calls != 0 || chat.calls != 0 {
				t.Fatalf("202 path ran external work: DWS=%d LLM=%d", provider.calls, chat.calls)
			}
			var stored []byte
			if err := testPool.QueryRow(ctx, `SELECT command FROM inbound_coordinator_job WHERE agent_id=$1`, f.agentID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var command DispatchCommand
			if err := json.Unmarshal(stored, &command); err != nil {
				t.Fatal(err)
			}
			var err error
			command, err = bindDispatchCompletionTarget(command, testRouterTargetIdentity)
			if err != nil {
				t.Fatal(err)
			}
			if managedDingTalkResponse(command) != tc.managed {
				t.Fatalf("HTTP policy did not survive durable Coordinator acceptance: %+v", command.ResponsePolicy)
			}
			for _, callback := range []string{command.CompletionCallback.URL, command.CompletionCallback.UpdateURL} {
				route, err := f.h.DingTalkResponses.FindRoute(ctx, callback)
				if err != nil || (route != nil) != tc.managed {
					t.Fatalf("HTTP acceptance route=%+v managed=%v err=%v", route, tc.managed, err)
				}
			}
			// Simulate the Coordinator verdict, then use its real durable outbox
			// result as the response worker input instead of fabricating an action.
			const reply = "The reviewed schedule is ready for your decision."
			terminal := httptest.NewRecorder()
			if !writeDispatchCoordinatorTerminal(terminal, ctx, f.h, command, scope, inboundcoord.Decision{Action: inboundcoord.ActionReply, UserText: reply}) || terminal.Code != http.StatusAccepted {
				t.Fatalf("reply completion failed: status=%d body=%s", terminal.Code, terminal.Body.String())
			}
			var result agentmessagerouter.ExecutionResultRequest
			if err := testPool.QueryRow(ctx, `SELECT request_id,agent_id::text,execution_status,result_message FROM task_completion_outbox WHERE agent_id=$1 AND callback_url=$2`, f.agentID, command.CompletionCallback.URL).Scan(&result.RequestID, &result.AgentID, &result.ExecutionStatus, &result.ResultMessage); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				managed, err := f.h.PrepareExecutionResult(ctx, command.CompletionCallback.URL, result)
				if err != nil || managed != tc.managed {
					t.Fatalf("reply ownership managed=%v want=%v err=%v", managed, tc.managed, err)
				}
			}
			var count int
			if err := testPool.QueryRow(ctx, `SELECT count(*) FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if !tc.managed {
				if count != 0 || command.ResponsePolicy != nil && command.ResponsePolicy.Mode != "legacy" || result.ResultMessage != reply {
					t.Fatalf("legacy behavior changed: actions=%d policy=%+v result=%+v", count, command.ResponsePolicy, result)
				}
				return
			}
			if count != 1 {
				t.Fatalf("managed completion should create one send despite replay, got %d", count)
			}
			var kind, state string
			var input []byte
			if err := testPool.QueryRow(ctx, `SELECT kind,state,input FROM response_action WHERE agent_id=$1`, f.agentID).Scan(&kind, &state, &input); err != nil {
				t.Fatal(err)
			}
			var action dingtalkresponse.ActionInput
			if err := json.Unmarshal(input, &action); err != nil {
				t.Fatal(err)
			}
			if kind != "message.send" || state != "pending" || action.Text != reply || !action.ShowAITag || action.CallbackURL != command.CompletionCallback.ResponseURL || action.ConversationID != "cid-"+routerTaskID || action.DWSUID != "123" || action.DWSOrgID != "456" || action.SenderOpenDingTalkID != "requester-open-id" {
				t.Fatalf("wrong action after HTTP dispatch: kind=%s state=%s action=%+v", kind, state, action)
			}
			if provider.calls != 0 || chat.calls != 0 || strings.TrimSpace(result.ResultMessage) != reply {
				t.Fatal("persistence-only regression unexpectedly executed external work")
			}
		})
	}
}

func TestAgentDispatchResponseHTTPDecodePreservesPolicy(t *testing.T) {
	raw := responseHTTPPayload("11111111-1111-4111-8111-111111111111", "wire-test", `{"version":1,"mode":"multica_coordinator","revision":9,"showAiTag":true}`)
	var request AgentDispatchV2Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	command := request.DispatchCommand()
	if command.ResponsePolicy == nil {
		t.Fatal("Router responsePolicy was lost at the HTTP request boundary")
	}
	if p := command.ResponsePolicy; p.Version != 1 || p.Mode != "multica_coordinator" || p.Revision != 9 || !p.ShowAITag {
		t.Fatalf("response policy changed: %+v", p)
	}
	if err := command.validate(); err != nil {
		t.Fatalf("valid Router payload rejected: %v", err)
	}
	if !managedDingTalkResponse(command) {
		t.Fatal("decoded managed request fell back to legacy ownership")
	}
}
