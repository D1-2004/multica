package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func createRuntimeStartClaimFixture(t *testing.T, protocol string) (runtimeID, taskID string, attempt db.AgentTaskRuntimeStartAttempt) {
	t.Helper()
	ctx := context.Background()
	runtimeID = createClaimReclaimRuntime(t, ctx, "Runtime start protocol fixture")
	capabilities := []string{"dws", "mcp"}
	if protocol == service.RuntimeStartProtocolHTTPJSONV1 {
		capabilities = append(capabilities, service.RuntimeStartCapabilityEventsV1)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET provider = 'hermes',
		    metadata = jsonb_build_object(
		        'kind', 'fc-e2b',
		        'template', 'runtime-start-test',
		        'capabilities', $2::text[]
		    )
		WHERE id = $1
	`, runtimeID, capabilities); err != nil {
		t.Fatalf("configure cloud runtime metadata: %v", err)
	}
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Runtime start protocol agent")
	taskID = seedQueuedIssueTask(t, ctx, agentID, runtimeID, issueID)
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		t.Fatalf("load queued task: %v", err)
	}
	attempt, err = testHandler.TaskService.BeginRuntimeStartAttempt(
		ctx,
		task,
		service.SandboxBackendAliyunFC,
		protocol,
	)
	if err != nil {
		t.Fatalf("begin Runtime start attempt: %v", err)
	}
	return runtimeID, taskID, attempt
}

func claimRuntimeStartFixture(t *testing.T, runtimeID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
		body,
		testWorkspaceID,
		"runtime-start-protocol-test",
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	return w
}

func TestClaimTaskByRuntimePreservesLegacyImageCompatibility(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	runtimeID, taskID, attempt := createRuntimeStartClaimFixture(t, service.RuntimeStartProtocolLegacyV1)
	w := claimRuntimeStartFixture(t, runtimeID, map[string]any{"target_task_id": taskID})
	if w.Code != http.StatusOK {
		t.Fatalf("legacy image claim status = %d: %s", w.Code, w.Body.String())
	}
	got, err := testHandler.Queries.GetAgentTaskRuntimeStartAttempt(context.Background(), db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: parseUUID(taskID), RuntimeID: parseUUID(runtimeID),
	})
	if err != nil {
		t.Fatalf("load legacy attempt: %v", err)
	}
	if got.Status != "claimed" {
		t.Fatalf("legacy attempt status = %q", got.Status)
	}
}

func TestClaimTaskByRuntimeRequiresExactNegotiatedStartupProtocol(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID, taskID, attempt := createRuntimeStartClaimFixture(t, service.RuntimeStartProtocolHTTPJSONV1)

	w := claimRuntimeStartFixture(t, runtimeID, map[string]any{"target_task_id": taskID})
	if w.Code != http.StatusConflict {
		t.Fatalf("missing protocol status = %d: %s", w.Code, w.Body.String())
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("load requeued task: %v", err)
	}
	if status != "queued" {
		t.Fatalf("protocol mismatch left task in %q", status)
	}

	w = claimRuntimeStartFixture(t, runtimeID, map[string]any{
		"target_task_id":           taskID,
		"runtime_start_attempt_id": util.UUIDToString(attempt.ID),
		"startup_status_protocol":  service.RuntimeStartProtocolHTTPJSONV1,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("exact protocol claim status = %d: %s", w.Code, w.Body.String())
	}
	got, err := testHandler.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: parseUUID(taskID), RuntimeID: parseUUID(runtimeID),
	})
	if err != nil {
		t.Fatalf("load exact-protocol attempt: %v", err)
	}
	if got.Status != "claimed" || !got.ClaimFinalizedAt.Valid {
		t.Fatalf("exact-protocol attempt = %+v", got)
	}
}

func TestClaimTaskByRuntimeRequiresPairedStartupProtocolFields(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID, taskID, _ := createRuntimeStartClaimFixture(t, service.RuntimeStartProtocolHTTPJSONV1)
	w := claimRuntimeStartFixture(t, runtimeID, map[string]any{
		"target_task_id":          taskID,
		"startup_status_protocol": service.RuntimeStartProtocolHTTPJSONV1,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unpaired protocol status = %d: %s", w.Code, w.Body.String())
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("load task after unpaired protocol: %v", err)
	}
	if status != "queued" {
		t.Fatalf("unpaired protocol changed task status to %q", status)
	}
}

func TestClaimTaskByRuntimeRejectsProtocolFieldsForLegacyAttempt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID, taskID, attempt := createRuntimeStartClaimFixture(t, service.RuntimeStartProtocolLegacyV1)
	w := claimRuntimeStartFixture(t, runtimeID, map[string]any{
		"target_task_id":           taskID,
		"runtime_start_attempt_id": util.UUIDToString(attempt.ID),
		"startup_status_protocol":  service.RuntimeStartProtocolHTTPJSONV1,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("legacy extra protocol status = %d: %s", w.Code, w.Body.String())
	}
	var taskStatus, attemptStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task after legacy protocol mismatch: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_runtime_start_attempt WHERE id = $1`, attempt.ID).Scan(&attemptStatus); err != nil {
		t.Fatalf("load attempt after legacy protocol mismatch: %v", err)
	}
	if taskStatus != "queued" || attemptStatus != "starting" {
		t.Fatalf("legacy protocol mismatch task=%q attempt=%q", taskStatus, attemptStatus)
	}
}

func TestRuntimeStartFailureEventReturnsSafeErrorToChat(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Runtime start chat failure fixture")
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET provider = 'hermes',
		    metadata = '{
		      "kind":"fc-e2b",
		      "template":"runtime-start-test",
		      "capabilities":["dws","mcp","runtime_start_events_v1"]
		    }'::jsonb
		WHERE id = $1
	`, runtimeID); err != nil {
		t.Fatalf("configure cloud runtime metadata: %v", err)
	}
	agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Runtime start chat failure agent")
	var sessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, runtime_id)
		VALUES ($1, $2, $3, 'Runtime start failure', 'active', $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&sessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, context)
		VALUES ($1, $2, $3, 'queued', 0, '{}'::jsonb)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		t.Fatalf("load chat task: %v", err)
	}
	attempt, err := testHandler.TaskService.BeginRuntimeStartAttempt(
		ctx,
		task,
		service.SandboxBackendAliyunFC,
		service.RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin Runtime start attempt: %v", err)
	}
	secret := "OPENAI_API_KEY=" + "sk-" + strings.Repeat("x", 24)
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/tasks/"+taskID+"/runtime-start-events",
		map[string]any{
			"runtime_start_attempt_id": util.UUIDToString(attempt.ID),
			"startup_status_protocol":  service.RuntimeStartProtocolHTTPJSONV1,
			"event":                    "stage_failed",
			"stage":                    "daemon_start",
			"error_code":               "DAEMON-START-FAILED",
			"message":                  "daemon rejected credential " + secret,
		},
		testWorkspaceID,
		"runtime-start-protocol-test",
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	req = withURLParam(req, "taskId", taskID)
	testHandler.RecordRuntimeStartEvent(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("failure event status = %d: %s", w.Code, w.Body.String())
	}

	var taskStatus, taskError, failureReason string
	if err := testPool.QueryRow(ctx, `
		SELECT status, error, failure_reason
		FROM agent_task_queue
		WHERE id = $1
	`, taskID).Scan(&taskStatus, &taskError, &failureReason); err != nil {
		t.Fatalf("load failed task: %v", err)
	}
	if taskStatus != "failed" || failureReason != "runtime_start_failed" {
		t.Fatalf("failed task status=%q reason=%q", taskStatus, failureReason)
	}
	for _, value := range []string{taskError} {
		if strings.Contains(value, secret) || strings.Contains(value, "daemon rejected credential") {
			t.Fatalf("user-visible error exposes internal detail: %q", value)
		}
		for _, expected := range []string{"FCE2B-DAEMON-START-FAILED", "daemon_start", taskID} {
			if !strings.Contains(value, expected) {
				t.Fatalf("user-visible error %q missing %q", value, expected)
			}
		}
	}
	var role, chatContent, chatFailureReason string
	if err := testPool.QueryRow(ctx, `
		SELECT role, content, failure_reason
		FROM chat_message
		WHERE chat_session_id = $1 AND task_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID, taskID).Scan(&role, &chatContent, &chatFailureReason); err != nil {
		t.Fatalf("load chat failure message: %v", err)
	}
	if role != "assistant" || chatFailureReason != "runtime_start_failed" || chatContent != taskError {
		t.Fatalf("chat failure role=%q reason=%q content=%q task_error=%q", role, chatFailureReason, chatContent, taskError)
	}
	var internalDetail string
	if err := testPool.QueryRow(ctx, `
		SELECT error_detail
		FROM agent_task_runtime_start_attempt
		WHERE id = $1
	`, attempt.ID).Scan(&internalDetail); err != nil {
		t.Fatalf("load attempt detail: %v", err)
	}
	if strings.Contains(internalDetail, secret) || !strings.Contains(internalDetail, "[REDACTED") {
		t.Fatalf("attempt detail was not redacted: %q", internalDetail)
	}
}
