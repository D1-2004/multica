package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
)

const testRouterTargetIdentity = "router-target:v1:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDispatchCommandValidateCompletionCallbackByPresence(t *testing.T) {
	valid := DispatchCommand{
		SchemaVersion: "2.0",
		AgentID:       "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{
			Domain: "channel",
			Type:   "message.created",
			Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: "cid"},
				Sender:       DispatchSender{OpenDingTalkID: "open-sender"},
				Messages:     []DispatchMessage{{OpenMsgID: "mid", Text: "hello"}},
			},
		},
		Surface:            DispatchSurface{Type: "chat"},
		Outbound:           DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		CompletionCallback: &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task-1/execution-result"},
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("digital employee callback rejected: %v", err)
	}
	robot := valid
	robot.Source.Type = "robot"
	robot.Outbound.Mode = "robot_sdk"
	if err := robot.validate(); err != nil {
		t.Fatalf("robot callback rejected: %v", err)
	}
	missing := valid
	missing.CompletionCallback = nil
	if err := missing.validate(); err != nil {
		t.Fatalf("callback-absent streaming or legacy dispatch rejected: %v", err)
	}

	tests := []struct {
		name     string
		source   string
		callback string
	}{
		{name: "absolute URL", source: "digital_employee", callback: "https://router.example/api/v1/dispatch-tasks/task/execution-result"},
		{name: "query", source: "digital_employee", callback: "/api/v1/dispatch-tasks/task/execution-result?token=secret"},
		{name: "wrong path", source: "digital_employee", callback: "/api/v1/tasks/task/complete"},
		{name: "robot wrong path", source: "robot", callback: "/api/v1/tasks/task/complete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := valid
			command.Source.Type = tt.source
			command.CompletionCallback = &DispatchCompletionCallback{URL: tt.callback}
			if err := command.validate(); err == nil {
				t.Fatal("expected callback validation error")
			}
		})
	}
}

func TestDispatchRuntimeContextPersistsCompletionCallback(t *testing.T) {
	command := DispatchCommand{
		SchemaVersion:      "2.0",
		CompletionCallback: &DispatchCompletionCallback{
			URL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
			Target: testRouterTargetIdentity,
		},
	}
	raw := dispatchRuntimeContext(command, "dispatch-window:1")

	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	callback, ok := stored["completion_callback"].(map[string]any)
	if !ok ||
		callback["url"] != "/api/v1/dispatch-tasks/router-task-1/execution-result" ||
		callback["target"] != testRouterTargetIdentity {
		t.Fatalf("stored callback = %#v", stored["completion_callback"])
	}
}

func TestDispatchRequestFingerprintExcludesOnlyTransientIdentityContext(t *testing.T) {
	command := DispatchCommand{
		SchemaVersion: "2.0",
		AgentID:       "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{
			Domain: "channel",
			Type:   "message.created",
			Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: "cid"},
				Messages:     []DispatchMessage{{OpenMsgID: "mid", Text: "hello"}},
			},
		},
		Surface:            DispatchSurface{Type: "chat"},
		Outbound:           DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ExternalIdentity:   AgentDispatchExternalIdentity{ContextToken: "token-one"},
		CompletionCallback: &DispatchCompletionCallback{
			URL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
			Target: testRouterTargetIdentity,
		},
		DispatchEndpointID: "endpoint-one",
	}
	first := dispatchRequestFingerprint(command, "idempotency-one")

	refreshedIdentity := command
	refreshedIdentity.ExternalIdentity.ContextToken = "token-two"
	if got := dispatchRequestFingerprint(refreshedIdentity, "idempotency-one"); got != first {
		t.Fatalf("context token changed stable fingerprint: got %q want %q", got, first)
	}

	changedEvent := command
	changedEvent.Event.Data.Messages = append([]DispatchMessage(nil), command.Event.Data.Messages...)
	changedEvent.Event.Data.Messages[0].Text = "changed"
	if got := dispatchRequestFingerprint(changedEvent, "idempotency-one"); got == first {
		t.Fatal("changed business event reused the original fingerprint")
	}

	changedCallback := command
	changedCallback.CompletionCallback = &DispatchCompletionCallback{
		URL:    "/api/v1/dispatch-tasks/router-task-2/execution-result",
		Target: testRouterTargetIdentity,
	}
	if got := dispatchRequestFingerprint(changedCallback, "idempotency-one"); got == first {
		t.Fatal("changed callback reused the original fingerprint")
	}
}

func TestBindDispatchCompletionTargetRequiresConfiguredRouter(t *testing.T) {
	command := DispatchCommand{
		CompletionCallback: &DispatchCompletionCallback{
			URL: "/api/v1/dispatch-tasks/router-task-1/execution-result",
		},
	}
	bound, err := bindDispatchCompletionTarget(command, testRouterTargetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if bound.CompletionCallback == nil || bound.CompletionCallback.Target != testRouterTargetIdentity {
		t.Fatalf("bound callback = %#v", bound.CompletionCallback)
	}
	if command.CompletionCallback.Target != "" {
		t.Fatal("binding mutated the decoded wire request")
	}
	for _, target := range []string{"", "router-target:v1:sha256:not-a-digest"} {
		if _, err := bindDispatchCompletionTarget(command, target); err == nil {
			t.Fatalf("completion callback accepted invalid Router target %q", target)
		}
	}
}

func TestAgentDispatchAcceptanceSerializesConcurrentClaims(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-dispatch-acceptance-claims", nil)
	endpointID, _ := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(
		context.Background(),
		endpointID,
	)
	if err != nil {
		t.Fatal(err)
	}
	const idempotencyKey = "acceptance-concurrent-claim"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
	})
	command := DispatchCommand{
		SchemaVersion: "2.0",
		AgentID:       agentID,
		Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Event: DispatchEvent{
			Domain: "channel",
			Type:   "message.created",
			Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: "cid"},
				Messages:     []DispatchMessage{{OpenMsgID: "mid", Text: "hello"}},
			},
		},
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		CompletionCallback: &DispatchCompletionCallback{
			URL:    "/api/v1/dispatch-tasks/acceptance-concurrent/execution-result",
			Target: testRouterTargetIdentity,
		},
		DispatchEndpointID: endpointID,
	}
	dispatchContext := agentDispatchContext{
		EndpointNamespaceID: endpoint.ID,
		WorkspaceID:         endpoint.WorkspaceID,
		AgentID:             endpoint.AgentID,
		UserID:              endpoint.ActorUserID,
	}
	first, replay, err := testHandler.claimAgentDispatchAcceptance(
		context.Background(),
		command,
		dispatchContext,
		idempotencyKey,
	)
	if err != nil || replay {
		t.Fatalf("first claim: replay=%v err=%v", replay, err)
	}
	if !first.LeaseToken.Valid {
		t.Fatal("first claim has no lease")
	}
	if _, _, err := testHandler.claimAgentDispatchAcceptance(
		context.Background(),
		command,
		dispatchContext,
		idempotencyKey,
	); !errors.Is(err, errAgentDispatchAcceptancePending) {
		t.Fatalf("concurrent claim error = %v, want pending", err)
	}

	changed := command
	changed.Event.Data.Messages = append([]DispatchMessage(nil), command.Event.Data.Messages...)
	changed.Event.Data.Messages[0].Text = "changed"
	if _, _, err := testHandler.claimAgentDispatchAcceptance(
		context.Background(),
		changed,
		dispatchContext,
		idempotencyKey,
	); !errors.Is(err, errAgentDispatchAcceptanceConflict) {
		t.Fatalf("changed request claim error = %v, want conflict", err)
	}

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_dispatch_acceptance
		SET lease_expires_at = now() - interval '1 second'
		WHERE endpoint_id = $1 AND idempotency_key = $2
	`, endpoint.ID, idempotencyKey); err != nil {
		t.Fatal(err)
	}
	takenOver, replay, err := testHandler.claimAgentDispatchAcceptance(
		context.Background(),
		command,
		dispatchContext,
		idempotencyKey,
	)
	if err != nil || replay {
		t.Fatalf("expired claim takeover: replay=%v err=%v", replay, err)
	}
	if !takenOver.LeaseToken.Valid || takenOver.LeaseToken == first.LeaseToken {
		t.Fatalf("expired claim lease was not replaced: first=%v takeover=%v",
			first.LeaseToken, takenOver.LeaseToken)
	}
}

func TestHandleAgentDispatchV2DistinguishesActivePendingFromConflict(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-dispatch-active-pending-http", nil)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(
		context.Background(),
		endpointID,
	)
	if err != nil {
		t.Fatal(err)
	}
	const idempotencyKey = "acceptance-active-pending-http"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
	})
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"completionCallback":{"url":"/api/v1/dispatch-tasks/active-pending-http/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-active-pending-http","type":"single"},
				"sender":{"displayName":"张三"},
				"messages":[{"openMsgId":"msg-active-pending-http","occurredAt":1784512800000,"text":"active pending"}]
			}
		},
		"surface":{"type":"issue"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, agentID)
	var wire AgentDispatchV2Request
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatal(err)
	}
	command, err := bindDispatchCompletionTarget(
		wire.DispatchCommand(),
		testRouterTargetIdentity,
	)
	if err != nil {
		t.Fatal(err)
	}
	command.DispatchEndpointID = uuidToString(endpoint.ID)
	dispatchContext := agentDispatchContext{
		EndpointNamespaceID: endpoint.ID,
		WorkspaceID:         endpoint.WorkspaceID,
		AgentID:             endpoint.AgentID,
		UserID:              endpoint.ActorUserID,
	}
	if _, replay, err := testHandler.claimAgentDispatchAcceptance(
		context.Background(),
		command,
		dispatchContext,
		idempotencyKey,
	); err != nil || replay {
		t.Fatalf("seed active pending acceptance: replay=%v err=%v", replay, err)
	}
	dispatch := func(requestBody string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/webhooks/agent-dispatch",
			strings.NewReader(requestBody),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+deliverySecret)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req = withURLParams(req, "endpointId", endpointID)
		w := httptest.NewRecorder()
		testHandler.HandleAgentDispatch(w, req)
		return w
	}

	activePending := dispatch(body)
	if activePending.Code != http.StatusConflict ||
		activePending.Body.String() != "{\"error\":\"dispatch acceptance is still pending\"}\n" {
		t.Fatalf("active pending response: status=%d body=%s",
			activePending.Code, activePending.Body.String())
	}

	changedBody := strings.Replace(body, "active pending", "changed request", 1)
	conflict := dispatch(changedBody)
	if conflict.Code != http.StatusConflict ||
		conflict.Body.String() != "{\"error\":\"idempotency key conflicts with another dispatch\"}\n" {
		t.Fatalf("fingerprint conflict response: status=%d body=%s",
			conflict.Code, conflict.Body.String())
	}
}

func TestHandleAgentDispatchV2ChatActivePendingReturnsBusyConflict(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-chat-active-pending-http", nil)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(
		context.Background(),
		endpointID,
	)
	if err != nil {
		t.Fatal(err)
	}
	const idempotencyKey = "chat-active-pending-http"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
	})
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"completionCallback":{"url":"/api/v1/dispatch-tasks/chat-active-pending-http/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-chat-active-pending-http","type":"single"},
				"sender":{"displayName":"张三"},
				"messages":[{"openMsgId":"msg-chat-active-pending-http","occurredAt":1784512800000,"text":"chat active pending"}]
			}
		},
		"surface":{"type":"chat"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, agentID)
	var wire AgentDispatchV2Request
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatal(err)
	}
	command, err := bindDispatchCompletionTarget(
		wire.DispatchCommand(),
		testRouterTargetIdentity,
	)
	if err != nil {
		t.Fatal(err)
	}
	command.DispatchEndpointID = uuidToString(endpoint.ID)
	dispatchContext := agentDispatchContext{
		EndpointNamespaceID: endpoint.ID,
		WorkspaceID:         endpoint.WorkspaceID,
		AgentID:             endpoint.AgentID,
		UserID:              endpoint.ActorUserID,
	}
	if _, replay, err := testHandler.claimAgentDispatchAcceptance(
		context.Background(),
		command,
		dispatchContext,
		idempotencyKey,
	); err != nil || replay {
		t.Fatalf("seed chat active pending acceptance: replay=%v err=%v", replay, err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/webhooks/agent-dispatch",
		strings.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req = withURLParams(req, "endpointId", endpointID)
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	if w.Code != http.StatusConflict ||
		w.Body.String() != "{\"error\":\"dispatch acceptance is still pending\"}\n" {
		t.Fatalf("chat active pending response: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestWriteAgentChatNeedsBindingACKV2PersistsTerminalCallback(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler integration database is unavailable")
	}
	callback := "/api/v1/dispatch-tasks/router-needs-binding-handler/execution-result"
	command := DispatchCommand{
		CompletionCallback: &DispatchCompletionCallback{URL: callback, Target: testRouterTargetIdentity},
	}
	dispatchContext := agentDispatchContext{
		AgentID: util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `
			DELETE FROM task_completion_outbox
			WHERE request_id = 'multica-terminal:sync:router-needs-binding-handler'
		`)
	})

	w := httptest.NewRecorder()
	handled := testHandler.writeAgentChatNeedsBindingACKV2(
		w,
		context.Background(),
		command,
		dispatchContext,
		engine.Result{Outcome: engine.OutcomeNeedsBinding},
	)
	if !handled || w.Code != http.StatusAccepted {
		t.Fatalf("handled=%v status=%d body=%s", handled, w.Code, w.Body.String())
	}
	var status, reason string
	if err := testPool.QueryRow(context.Background(), `
		SELECT execution_status, failure_reason
		FROM task_completion_outbox
		WHERE request_id = 'multica-terminal:sync:router-needs-binding-handler'
	`).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || reason != "needs_binding" {
		t.Fatalf("status=%q reason=%q", status, reason)
	}
}

func TestWriteAgentChatNoTaskOutcomeV2PersistsAcceptedTerminalCallback(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler integration database is unavailable")
	}
	for _, outcome := range []engine.Outcome{
		engine.OutcomeAgentOffline,
		engine.OutcomeAgentArchived,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			dispatchTaskID := "router-no-task-" + string(outcome)
			callback := "/api/v1/dispatch-tasks/" + dispatchTaskID + "/execution-result"
			command := DispatchCommand{
				CompletionCallback: &DispatchCompletionCallback{
					URL:    callback,
					Target: testRouterTargetIdentity,
				},
			}
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `
					DELETE FROM task_completion_outbox WHERE request_id = $1
				`, "multica-terminal:sync:"+dispatchTaskID)
			})

			w := httptest.NewRecorder()
			handled := testHandler.writeAgentChatNoTaskOutcomeV2(
				w,
				context.Background(),
				command,
				agentDispatchContext{
					AgentID: util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
				},
				engine.Result{Outcome: outcome},
			)
			if !handled || w.Code != http.StatusAccepted {
				t.Fatalf("handled=%v status=%d body=%s", handled, w.Code, w.Body.String())
			}
			var status, reason string
			if err := testPool.QueryRow(context.Background(), `
				SELECT execution_status, failure_reason
				FROM task_completion_outbox
				WHERE request_id = $1
			`, "multica-terminal:sync:"+dispatchTaskID).Scan(&status, &reason); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || reason != string(outcome) {
				t.Fatalf("status=%q reason=%q", status, reason)
			}
		})
	}
}

func TestWriteAgentChatNoTaskOutcomeV2RejectsUnacceptedDrop(t *testing.T) {
	command := DispatchCommand{
		CompletionCallback: &DispatchCompletionCallback{
			URL:    "/api/v1/dispatch-tasks/router-no-task-rejected/execution-result",
			Target: testRouterTargetIdentity,
		},
	}
	w := httptest.NewRecorder()
	handled := testHandler.writeAgentChatNoTaskOutcomeV2(
		w,
		context.Background(),
		command,
		agentDispatchContext{
			AgentID: util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		},
		engine.Result{
			Outcome:    engine.OutcomeDropped,
			DropReason: engine.DropReasonTaskContextRejected,
		},
	)
	if !handled || w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("handled=%v status=%d body=%s", handled, w.Code, w.Body.String())
	}
}
