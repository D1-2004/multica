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
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
)

const testRouterTargetIdentity = "router-target:v1:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func createCoordinatorIssueTerminalFixture(t *testing.T, name string) (string, string, string) {
	t.Helper()
	agentID := createHandlerTestAgent(t, name, nil)
	var issueID, taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (
			workspace_id, title, status, priority, assignee_type, assignee_id,
			creator_type, creator_id, number, position
		)
		VALUES (
			$1, $2, 'todo', 'none', 'agent', $3, 'member', $4,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), 0
		)
		RETURNING id
	`, testWorkspaceID, name, agentID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority,
			initiator_user_id, originator_user_id, accountable_user_id, context
		)
		VALUES ($1, $2, $3, 'queued', 2, $4, $4, $4, '{}'::jsonb)
		RETURNING id
	`, agentID, handlerTestRuntimeID(t), issueID, testUserID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE root_task_id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1 OR request_id LIKE $2`, taskID, "%"+name+"%")
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return agentID, issueID, taskID
}

func assertCoordinatorIssueOwnsTerminal(
	t *testing.T,
	w *httptest.ResponseRecorder,
	issueID, taskID, dispatchTaskID string,
) {
	t.Helper()
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var updateType, resultMessage string
	if err := testPool.QueryRow(context.Background(), `
		SELECT update_type, result_message
		FROM task_execution_update_outbox
		WHERE root_task_id = $1 AND request_id = $2
	`, taskID, "multica-coord-issue:"+taskID).Scan(&updateType, &resultMessage); err != nil {
		t.Fatal(err)
	}
	if updateType != "delegated_to_issue" || resultMessage != "我去处理 WS-13" {
		t.Fatalf("update_type=%q result_message=%q", updateType, resultMessage)
	}
	var terminalCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM task_completion_outbox
		WHERE request_id = $1
	`, "multica-terminal:sync-completed:"+dispatchTaskID).Scan(&terminalCount); err != nil {
		t.Fatal(err)
	}
	if terminalCount != 0 {
		t.Fatalf("coordinator wrote %d premature terminal completions for issue %s", terminalCount, issueID)
	}
}

func TestCoordinatorIssueCommentKeepsRouterDispatchOpen(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler integration database is unavailable")
	}
	t.Run("chat materializer", func(t *testing.T) {
		agentID, issueID, taskID := createCoordinatorIssueTerminalFixture(t, "coord-issue-chat")
		const dispatchTaskID = "coord-issue-chat"
		w := httptest.NewRecorder()
		handled := testHandler.writeAgentChatCoordinatorOutcomeV2(
			w,
			context.Background(),
			DispatchCommand{CompletionCallback: &DispatchCompletionCallback{
				URL:       "/api/v1/dispatch-tasks/" + dispatchTaskID + "/execution-result",
				UpdateURL: "/api/v1/dispatch-tasks/" + dispatchTaskID + "/execution-update",
				Target:    testRouterTargetIdentity,
			}},
			agentDispatchContext{AgentID: util.MustParseUUID(agentID)},
			engine.Result{
				Outcome:         engine.OutcomeCoordinatorReply,
				IssueID:         util.MustParseUUID(issueID),
				TaskID:          util.MustParseUUID(taskID),
				IssueIdentifier: "WS-13",
				ReplyText:       "我去处理 WS-13",
			},
		)
		if !handled {
			t.Fatal("coordinator issue result was not handled")
		}
		var response AgentChatDispatchResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Continuation.Kind != "issue" || response.Continuation.IssueID != issueID || response.TaskID != taskID {
			t.Fatalf("response = %+v", response)
		}
		assertCoordinatorIssueOwnsTerminal(t, w, issueID, taskID, dispatchTaskID)
	})

	t.Run("issue materializer", func(t *testing.T) {
		agentID, issueID, taskID := createCoordinatorIssueTerminalFixture(t, "coord-issue-surface")
		const dispatchTaskID = "coord-issue-surface"
		w := httptest.NewRecorder()
		handled := writeDispatchCoordinatorTerminal(
			w,
			context.Background(),
			testHandler,
			DispatchCommand{CompletionCallback: &DispatchCompletionCallback{
				URL:       "/api/v1/dispatch-tasks/" + dispatchTaskID + "/execution-result",
				UpdateURL: "/api/v1/dispatch-tasks/" + dispatchTaskID + "/execution-update",
				Target:    testRouterTargetIdentity,
			}},
			agentDispatchContext{AgentID: util.MustParseUUID(agentID)},
			inboundcoord.Decision{
				Action:   inboundcoord.ActionReply,
				UserText: "我去处理 WS-13",
				IssueComment: &inboundcoord.IssueCommentEffect{
					IssueID:         issueID,
					IssueIdentifier: "WS-13",
					CommentID:       "cccccccc-cccc-cccc-cccc-cccccccccccc",
					TaskID:          taskID,
				},
			},
		)
		if !handled {
			t.Fatal("coordinator issue decision was not handled")
		}
		var response AgentDispatchResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Continuation.Kind != "issue" || response.Continuation.IssueID != issueID || response.TaskID != taskID {
			t.Fatalf("response = %+v", response)
		}
		assertCoordinatorIssueOwnsTerminal(t, w, issueID, taskID, dispatchTaskID)
	})
}

func TestCoordinatorIssueCommentReturnsIssueBeforeTerminalCallback(t *testing.T) {
	t.Parallel()
	const (
		agentID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		issueID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		taskID  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)

	t.Run("chat materializer", func(t *testing.T) {
		w := httptest.NewRecorder()
		handled := (&Handler{}).writeAgentChatCoordinatorOutcomeV2(
			w,
			context.Background(),
			DispatchCommand{CompletionCallback: &DispatchCompletionCallback{
				URL: "/api/v1/dispatch-tasks/router-chat/execution-result",
			}},
			agentDispatchContext{AgentID: util.MustParseUUID(agentID)},
			engine.Result{
				Outcome:   engine.OutcomeCoordinatorReply,
				IssueID:   util.MustParseUUID(issueID),
				TaskID:    util.MustParseUUID(taskID),
				ReplyText: "处理中",
			},
		)
		if !handled || w.Code != http.StatusAccepted {
			t.Fatalf("handled=%v status=%d body=%s", handled, w.Code, w.Body.String())
		}
		var response AgentChatDispatchResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Continuation.Kind != "issue" || response.Continuation.IssueID != issueID || response.TaskID != taskID {
			t.Fatalf("response = %+v", response)
		}
	})

	t.Run("issue materializer", func(t *testing.T) {
		w := httptest.NewRecorder()
		handled := writeDispatchCoordinatorTerminal(
			w,
			context.Background(),
			&Handler{},
			DispatchCommand{CompletionCallback: &DispatchCompletionCallback{
				URL: "/api/v1/dispatch-tasks/router-issue/execution-result",
			}},
			agentDispatchContext{AgentID: util.MustParseUUID(agentID)},
			inboundcoord.Decision{
				Action: inboundcoord.ActionReply,
				IssueComment: &inboundcoord.IssueCommentEffect{
					IssueID: issueID,
					TaskID:  taskID,
				},
			},
		)
		if !handled || w.Code != http.StatusAccepted {
			t.Fatalf("handled=%v status=%d body=%s", handled, w.Code, w.Body.String())
		}
		var response AgentDispatchResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Continuation.Kind != "issue" || response.Continuation.IssueID != issueID || response.TaskID != taskID {
			t.Fatalf("response = %+v", response)
		}
	})
}

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
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		CompletionCallback: &DispatchCompletionCallback{
			URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
			UpdateURL:          "/api/v1/dispatch-tasks/router-task-1/execution-update",
			TelemetryURL:       "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			TelemetryToken:     "task-write-capability",
			TelemetryExpiresAt: 1786377600000,
		},
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
	mismatchedUpdate := valid
	mismatchedUpdate.CompletionCallback = &DispatchCompletionCallback{
		URL:       "/api/v1/dispatch-tasks/router-task-1/execution-result",
		UpdateURL: "/api/v1/dispatch-tasks/router-task-2/execution-update",
	}
	if err := mismatchedUpdate.validate(); err == nil {
		t.Fatal("completion and update callbacks accepted different dispatch task IDs")
	}
	mismatchedTelemetry := valid
	mismatchedTelemetry.CompletionCallback = &DispatchCompletionCallback{
		URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
		TelemetryURL:       "/api/v1/dispatch-tasks/router-task-2/llm-traces",
		TelemetryToken:     "task-write-capability",
		TelemetryExpiresAt: 1786377600000,
	}
	if err := mismatchedTelemetry.validate(); err == nil {
		t.Fatal("completion and telemetry callbacks accepted different dispatch task IDs")
	}
	incompleteTelemetry := valid
	incompleteTelemetry.CompletionCallback = &DispatchCompletionCallback{
		URL:          "/api/v1/dispatch-tasks/router-task-1/execution-result",
		TelemetryURL: "/api/v1/dispatch-tasks/router-task-1/llm-traces",
	}
	if err := incompleteTelemetry.validate(); err == nil {
		t.Fatal("incomplete telemetry capability accepted")
	}
	for _, telemetry := range []DispatchCompletionCallback{
		{
			URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TelemetryURL:       "https://router.example.test/api/v1/dispatch-tasks/router-task-1/llm-traces",
			TelemetryToken:     "task-write-capability",
			TelemetryExpiresAt: 1786377600000,
		},
		{
			URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TelemetryURL:       "/api/v1/dispatch-tasks/router-task-1/llm-traces?token=secret",
			TelemetryToken:     "task-write-capability",
			TelemetryExpiresAt: 1786377600000,
		},
		{
			URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TelemetryURL:       "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			TelemetryToken:     "token with spaces",
			TelemetryExpiresAt: 1786377600000,
		},
	} {
		invalid := valid
		invalid.CompletionCallback = &telemetry
		if err := invalid.validate(); err == nil {
			t.Fatalf("invalid telemetry capability accepted: %#v", telemetry)
		}
	}
}

func TestDispatchRuntimeContextPersistsCompletionCallback(t *testing.T) {
	command := DispatchCommand{
		SchemaVersion: "2.0",
		CompletionCallback: &DispatchCompletionCallback{
			URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
			UpdateURL:          "/api/v1/dispatch-tasks/router-task-1/execution-update",
			TelemetryURL:       "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			TelemetryToken:     "task-write-capability",
			TelemetryExpiresAt: 1786377600000,
			Target:             testRouterTargetIdentity,
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
		callback["update_url"] != "/api/v1/dispatch-tasks/router-task-1/execution-update" ||
		callback["telemetry_url"] != "/api/v1/dispatch-tasks/router-task-1/llm-traces" ||
		callback["telemetry_token"] != "task-write-capability" ||
		callback["telemetry_expires_at"] != float64(1786377600000) ||
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
		Surface:  DispatchSurface{Type: "chat"},
		Outbound: DispatchOutbound{Mode: "dws", ReplyTo: "latest_message"},
		ExternalIdentity: AgentDispatchExternalIdentity{
			ContextToken: "token-one",
			ExpiresAt:    4102444800000,
		},
		CompletionCallback: &DispatchCompletionCallback{
			URL:                "/api/v1/dispatch-tasks/router-task-1/execution-result",
			UpdateURL:          "/api/v1/dispatch-tasks/router-task-1/execution-update",
			TelemetryURL:       "/api/v1/dispatch-tasks/router-task-1/llm-traces",
			TelemetryToken:     "token-one",
			TelemetryExpiresAt: 1786377600000,
			Target:             testRouterTargetIdentity,
		},
		DispatchEndpointID: "endpoint-one",
	}
	first := dispatchRequestFingerprint(command, "idempotency-one")

	refreshedIdentity := command
	refreshedIdentity.ExternalIdentity.ContextToken = "token-two"
	if got := dispatchRequestFingerprint(refreshedIdentity, "idempotency-one"); got != first {
		t.Fatalf("context token changed stable fingerprint: got %q want %q", got, first)
	}
	refreshedTelemetry := command
	refreshedTelemetry.CompletionCallback = &DispatchCompletionCallback{
		URL:                command.CompletionCallback.URL,
		UpdateURL:          command.CompletionCallback.UpdateURL,
		TelemetryURL:       command.CompletionCallback.TelemetryURL,
		TelemetryToken:     "token-two",
		TelemetryExpiresAt: 1786464000000,
		Target:             command.CompletionCallback.Target,
	}
	if got := dispatchRequestFingerprint(refreshedTelemetry, "idempotency-one"); got != first {
		t.Fatalf("refreshed telemetry capability changed stable fingerprint: got %q want %q", got, first)
	}
	changedTelemetryURL := refreshedTelemetry
	changedTelemetryURL.CompletionCallback = &DispatchCompletionCallback{
		URL:                command.CompletionCallback.URL,
		UpdateURL:          command.CompletionCallback.UpdateURL,
		TelemetryURL:       "/api/v1/dispatch-tasks/router-task-2/llm-traces",
		TelemetryToken:     "token-two",
		TelemetryExpiresAt: 1786464000000,
		Target:             command.CompletionCallback.Target,
	}
	if got := dispatchRequestFingerprint(changedTelemetryURL, "idempotency-one"); got == first {
		t.Fatal("changed telemetry URL reused the original fingerprint")
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
	changedUpdateCallback := command
	changedUpdateCallback.CompletionCallback = &DispatchCompletionCallback{
		URL:       command.CompletionCallback.URL,
		UpdateURL: "/api/v1/dispatch-tasks/router-task-2/execution-update",
		Target:    testRouterTargetIdentity,
	}
	if got := dispatchRequestFingerprint(changedUpdateCallback, "idempotency-one"); got == first {
		t.Fatal("changed update callback reused the original fingerprint")
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
