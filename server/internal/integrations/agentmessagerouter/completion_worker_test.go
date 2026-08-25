package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func writeCompletionWorkerSuccess(w http.ResponseWriter, r *http.Request) {
	var request ExecutionResultRequest
	_ = json.NewDecoder(r.Body).Decode(&request)
	match := executionResultCallbackPattern.FindStringSubmatch(r.URL.Path)
	dispatchTaskID := ""
	if len(match) == 2 {
		dispatchTaskID = match[1]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"code":    "success",
		"data": map[string]string{
			"dispatchTaskId":    dispatchTaskID,
			"executionStatus":   request.ExecutionStatus,
			"executionReportId": "worker-report",
		},
	})
}

func taskCompletionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func enqueueWorkerTestCompletion(
	t *testing.T,
	queries *db.Queries,
	targetIdentity string,
	suffix string,
) db.TaskCompletionOutbox {
	t.Helper()
	now := time.Now().UnixNano()
	row, err := queries.EnqueueTaskCompletion(context.Background(), db.EnqueueTaskCompletionParams{
		RootTaskID:        pgtype.UUID{Bytes: [16]byte{byte(now), 1}, Valid: true},
		TerminalTaskID:    pgtype.UUID{Bytes: [16]byte{byte(now), 2}, Valid: true},
		CallbackUrl:       "/api/v1/dispatch-tasks/router-" + suffix + "/execution-result",
		TargetIdentity:    targetIdentity,
		RequestID:         fmt.Sprintf("worker-test:%s:%d", suffix, now),
		AgentID:           pgtype.UUID{Bytes: [16]byte{byte(now), 3}, Valid: true},
		ExecutionStatus:   "completed",
		ResultMessage:     "final reply",
		ExternalSessionID: pgtype.Text{String: "session-1", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func enqueueWorkerTestExecutionUpdate(
	t *testing.T,
	queries *db.Queries,
	targetIdentity string,
	suffix string,
) db.TaskExecutionUpdateOutbox {
	t.Helper()
	now := time.Now().UnixNano()
	row, err := queries.EnqueueTaskExecutionUpdate(context.Background(), db.EnqueueTaskExecutionUpdateParams{
		RootTaskID:          pgtype.UUID{Bytes: [16]byte{byte(now), 11}, Valid: true},
		TargetTaskID:        pgtype.UUID{Bytes: [16]byte{byte(now), 12}, Valid: true},
		IssueID:             pgtype.UUID{Bytes: [16]byte{byte(now), 13}, Valid: true},
		IssueIdentifier:     "MUL-123",
		CallbackUrl:         "/api/v1/dispatch-tasks/router-" + suffix + "/execution-update",
		TargetIdentity:      targetIdentity,
		RequestID:           fmt.Sprintf("worker-update-test:%s:%d", suffix, now),
		AgentID:             pgtype.UUID{Bytes: [16]byte{byte(now), 14}, Valid: true},
		TargetAgentID:       pgtype.UUID{Bytes: [16]byte{byte(now), 15}, Valid: true},
		UpdateType:          "delegated_to_issue",
		ResultMessageFrozen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestEnqueueTaskExecutionUpdateReplayPreservesFrozenPayload(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	update := enqueueWorkerTestExecutionUpdate(t, queries, "router-target:v1:sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "replay-frozen")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, update.ID)
	})
	if _, err := pool.Exec(context.Background(), `
		UPDATE task_execution_update_outbox SET result_message = $2 WHERE id = $1
	`, update.ID, "任务已转入后台"); err != nil {
		t.Fatal(err)
	}

	replayed, err := queries.EnqueueTaskExecutionUpdate(context.Background(), db.EnqueueTaskExecutionUpdateParams{
		RootTaskID:          update.RootTaskID,
		TargetTaskID:        update.TargetTaskID,
		IssueID:             update.IssueID,
		IssueIdentifier:     update.IssueIdentifier,
		CallbackUrl:         update.CallbackUrl,
		TargetIdentity:      update.TargetIdentity,
		RequestID:           update.RequestID,
		AgentID:             update.AgentID,
		TargetAgentID:       update.TargetAgentID,
		UpdateType:          update.UpdateType,
		ResultMessageFrozen: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.ResultMessageFrozen || replayed.ResultMessage.String != "任务已转入后台" {
		t.Fatalf("replayed result message = %#v frozen=%v", replayed.ResultMessage, replayed.ResultMessageFrozen)
	}
}

func TestCompletionWorkerDeliversExecutionUpdateBeforeTerminalWork(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/dispatch-tasks/router-update-success/execution-update" {
			t.Errorf("callback path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]string{
				"dispatchTaskId": "router-update-success",
				"requestId":      received["requestId"].(string),
				"updateType":     "delegated_to_issue",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	update := enqueueWorkerTestExecutionUpdate(t, queries, client.TargetIdentity(), "update-success")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, update.ID)
	})

	worker := NewCompletionWorker(queries, client, nil)
	worked, err := worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worked {
		t.Fatal("execution update was not processed")
	}
	if received["requestId"] != update.RequestID ||
		received["agentId"] != util.UUIDToString(update.AgentID) ||
		received["externalTaskId"] != util.UUIDToString(update.RootTaskID) ||
		received["updateType"] != "delegated_to_issue" {
		t.Fatalf("execution update request = %#v", received)
	}
	if _, exists := received["externalRunId"]; exists {
		t.Fatalf("execution update request leaked result-only externalRunId = %#v", received)
	}
	extension, ok := received["extension"].(map[string]any)
	if !ok ||
		extension["issueId"] != util.UUIDToString(update.IssueID) ||
		extension["issueIdentifier"] != "MUL-123" ||
		extension["targetTaskId"] != util.UUIDToString(update.TargetTaskID) ||
		extension["targetAgentId"] != util.UUIDToString(update.TargetAgentID) {
		t.Fatalf("execution update extension = %#v", received["extension"])
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM task_execution_update_outbox WHERE id = $1
	`, update.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("status = %q", status)
	}
}

func TestCompletionWorkerRetriesIdenticalFrozenExecutionUpdateResultMessage(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		received = append(received, body)
		if len(received) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]string{
				"dispatchTaskId": "router-stable-result",
				"requestId":      body["requestId"].(string),
				"updateType":     "delegated_to_issue",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	update := enqueueWorkerTestExecutionUpdate(t, queries, client.TargetIdentity(), "stable-result")
	if _, err := pool.Exec(context.Background(), `
		UPDATE task_execution_update_outbox
		SET result_message = $2, result_message_frozen = TRUE
		WHERE id = $1
	`, update.ID, "任务已转入后台"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, update.ID)
	})

	worker := NewCompletionWorker(queries, client, nil)
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("first delivery worked=%v error=%v", worked, processErr)
	}
	if _, err := pool.Exec(context.Background(), `
		UPDATE task_execution_update_outbox SET available_at = now() WHERE id = $1
	`, update.ID); err != nil {
		t.Fatal(err)
	}
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("retry delivery worked=%v error=%v", worked, processErr)
	}
	if len(received) != 2 {
		t.Fatalf("request count = %d, want 2", len(received))
	}
	first, err := json.Marshal(received[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(received[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("retry payload changed:\nfirst=%s\nsecond=%s", first, second)
	}
	if received[0]["resultMessage"] != "任务已转入后台" {
		t.Fatalf("resultMessage = %#v", received[0]["resultMessage"])
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM task_execution_update_outbox WHERE id = $1
	`, update.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("status = %q, want delivered", status)
	}
}

func TestCompletionWorkerConsumesLegacyExecutionUpdateWithoutResultMessage(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]string{
				"dispatchTaskId": "router-legacy-update",
				"requestId":      received["requestId"].(string),
				"updateType":     "delegated_to_issue",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	var updateID pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO task_execution_update_outbox (
			root_task_id, target_task_id, issue_id, issue_identifier,
			callback_url, target_identity, request_id, agent_id,
			target_agent_id, update_type
		) VALUES (
			gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 'MUL-LEGACY',
			'/api/v1/dispatch-tasks/router-legacy-update/execution-update',
			$1, $2, gen_random_uuid(), gen_random_uuid(), 'delegated_to_issue'
		)
		RETURNING id
	`, client.TargetIdentity(), fmt.Sprintf("worker-legacy-update:%d", time.Now().UnixNano())).Scan(&updateID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, updateID)
	})

	worker := NewCompletionWorker(queries, client, nil)
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("legacy delivery worked=%v error=%v", worked, processErr)
	}
	if _, present := received["resultMessage"]; present {
		t.Fatalf("legacy request included resultMessage: %#v", received)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM task_execution_update_outbox WHERE id = $1
	`, updateID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("legacy status = %q, want delivered", status)
	}
}

func TestCompletionWorkerDoesNotDeliverTerminalWhileExecutionUpdateRetries(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	updateRequests := 0
	terminalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if executionUpdateCallbackPattern.MatchString(r.URL.Path) {
			updateRequests++
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		terminalRequests++
		writeCompletionWorkerSuccess(w, r)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	update := enqueueWorkerTestExecutionUpdate(t, queries, client.TargetIdentity(), "ordered-retry")
	completion, err := queries.EnqueueTaskCompletion(context.Background(), db.EnqueueTaskCompletionParams{
		RootTaskID:      update.RootTaskID,
		TerminalTaskID:  update.TargetTaskID,
		CallbackUrl:     "/api/v1/dispatch-tasks/router-ordered-retry/execution-result",
		TargetIdentity:  client.TargetIdentity(),
		RequestID:       "worker-terminal-after-update:" + update.RequestID,
		AgentID:         update.AgentID,
		ExecutionStatus: "completed",
		ResultMessage:   "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, update.ID)
	})

	worker := NewCompletionWorker(queries, client, nil)
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("update worked=%v error=%v", worked, processErr)
	}
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || worked {
		t.Fatalf("terminal while update retries worked=%v error=%v", worked, processErr)
	}
	if updateRequests != 1 || terminalRequests != 0 {
		t.Fatalf("update requests=%d terminal requests=%d", updateRequests, terminalRequests)
	}
}

func TestWaitingExecutionUpdateHoldsTerminalCompletionForOldWorker(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	targetIdentity := "router-target:v1:sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Now().UnixNano()
	update, err := queries.EnqueueTaskExecutionUpdate(context.Background(), db.EnqueueTaskExecutionUpdateParams{
		RootTaskID:          pgtype.UUID{Bytes: [16]byte{byte(now), 21}, Valid: true},
		TargetTaskID:        pgtype.UUID{Bytes: [16]byte{byte(now), 22}, Valid: true},
		IssueID:             pgtype.UUID{Bytes: [16]byte{byte(now), 23}, Valid: true},
		IssueIdentifier:     "MUL-WAITING",
		CallbackUrl:         "/api/v1/dispatch-tasks/router-waiting/execution-update",
		TargetIdentity:      targetIdentity,
		RequestID:           fmt.Sprintf("worker-waiting-update:%d", now),
		AgentID:             pgtype.UUID{Bytes: [16]byte{byte(now), 24}, Valid: true},
		TargetAgentID:       pgtype.UUID{Bytes: [16]byte{byte(now), 25}, Valid: true},
		UpdateType:          "delegated_to_issue",
		ResultMessageFrozen: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := queries.EnqueueTaskCompletion(context.Background(), db.EnqueueTaskCompletionParams{
		RootTaskID:      update.RootTaskID,
		TerminalTaskID:  update.TargetTaskID,
		CallbackUrl:     "/api/v1/dispatch-tasks/router-waiting/execution-result",
		TargetIdentity:  targetIdentity,
		RequestID:       fmt.Sprintf("worker-waiting-completion:%d", now),
		AgentID:         update.AgentID,
		ExecutionStatus: "completed",
		ResultMessage:   "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, update.ID)
	})

	var held bool
	if err := pool.QueryRow(context.Background(), `
		SELECT available_at = 'infinity'::timestamptz
		FROM task_completion_outbox WHERE id = $1
	`, completion.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("terminal completion is visible before the handoff result is frozen")
	}
	if _, err := queries.ClaimTaskCompletion(context.Background(), targetIdentity); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old worker claimed held terminal completion: %v", err)
	}

	if _, err := pool.Exec(context.Background(), `
		UPDATE task_execution_update_outbox
		SET result_message = '任务已转入后台', result_message_frozen = TRUE, status = 'queued'
		WHERE id = $1
	`, update.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.ReleaseTaskCompletionsForExecutionUpdate(context.Background(), update.RootTaskID); err != nil {
		t.Fatal(err)
	}
	var available bool
	if err := pool.QueryRow(context.Background(), `
		SELECT available_at <= now()
		FROM task_completion_outbox WHERE id = $1
	`, completion.ID).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("terminal completion was not released with the frozen handoff")
	}
}

func TestCompletionWorkerRetriesMissingExecutionUpdateEndpointDuringRouterRolling(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	update := enqueueWorkerTestExecutionUpdate(t, queries, client.TargetIdentity(), "rolling-endpoint")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE id = $1`, update.ID)
	})

	worker := NewCompletionWorker(queries, client, nil)
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("update worked=%v error=%v", worked, processErr)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM task_execution_update_outbox WHERE id = $1
	`, update.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("rolling 404 moved execution update to %q, want queued", status)
	}
}

func TestCompletionWorkerDeliversAndAcknowledgesOutbox(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	requests := 0
	var received ExecutionResultRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]string{
				"dispatchTaskId":    "router-success",
				"executionStatus":   received.ExecutionStatus,
				"executionReportId": "worker-report",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completion := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "success")
	if _, err := pool.Exec(context.Background(), `
		UPDATE task_completion_outbox
		SET execution_summary = '{"task_id":"task-1","status":"completed"}'::jsonb
		WHERE id = $1
	`, completion.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
	})
	worker := NewCompletionWorker(queries, client, nil)
	worked, err := worker.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !worked || requests != 1 {
		t.Fatalf("worked=%v requests=%d", worked, requests)
	}
	if received.ExecutionSummary["task_id"] != "task-1" || received.ExecutionSummary["status"] != "completed" {
		t.Fatalf("executionSummary = %#v", received.ExecutionSummary)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM task_completion_outbox WHERE id = $1
	`, completion.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("status = %q", status)
	}
}

func TestCompletionWorkerNormalizesAndRedactsResultMessage(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	var resultMessage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ExecutionResultRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		resultMessage = request.ResultMessage
		match := executionResultCallbackPattern.FindStringSubmatch(r.URL.Path)
		dispatchTaskID := ""
		if len(match) == 2 {
			dispatchTaskID = match[1]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]string{
				"dispatchTaskId":    dispatchTaskID,
				"executionStatus":   request.ExecutionStatus,
				"executionReportId": "worker-report",
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completion := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "normalize-redact")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
	})
	const rawResult = `first\nAKIAIOSFODNN7EXAMPLE`
	if _, err := pool.Exec(context.Background(), `
		UPDATE task_completion_outbox SET result_message = $2 WHERE id = $1
	`, completion.ID, rawResult); err != nil {
		t.Fatal(err)
	}

	worker := NewCompletionWorker(queries, client, nil)
	if worked, processErr := worker.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("worked=%v error=%v", worked, processErr)
	}
	const want = "first\n[REDACTED AWS KEY]"
	if resultMessage != want {
		t.Fatalf("result_message = %q, want %q", resultMessage, want)
	}
}

func TestCompletionWorkerRetriesTransientResponse(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completion := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "retry")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
	})
	worker := NewCompletionWorker(queries, client, nil)
	if worked, err := worker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%v error=%v", worked, err)
	}
	var status string
	var attempts int
	if err := pool.QueryRow(context.Background(), `
		SELECT status, attempt_count FROM task_completion_outbox WHERE id = $1
	`, completion.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || attempts != 1 {
		t.Fatalf("status=%q attempts=%d", status, attempts)
	}
}

func TestCompletionWorkerDropsDelegatedCommentCallbackAfterFirstFailure(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completion := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "comment-drop")
	requestID := "multica-comment-terminal:" + util.UUIDToString(completion.RootTaskID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE task_completion_outbox SET request_id = $2 WHERE id = $1
	`, completion.ID, requestID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
	})

	worker := NewCompletionWorker(queries, client, nil)
	if worked, err := worker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%v error=%v", worked, err)
	}
	var status string
	var attempts int
	if err := pool.QueryRow(context.Background(), `
		SELECT status, attempt_count FROM task_completion_outbox WHERE id = $1
	`, completion.ID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || status != "dead_letter" || attempts != 1 {
		t.Fatalf("requests=%d status=%q attempts=%d", requests, status, attempts)
	}
}

func TestCompletionWorkerDeadLettersPermanentResponse(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completion := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "dead")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, completion.ID)
	})
	worker := NewCompletionWorker(queries, client, nil)
	if worked, err := worker.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%v error=%v", worked, err)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM task_completion_outbox WHERE id = $1
	`, completion.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead_letter" {
		t.Fatalf("status = %q", status)
	}
}

func TestCompletionWorkersOnlyClaimTheirRouterTarget(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	requestsA := 0
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsA++
		writeCompletionWorkerSuccess(w, r)
	}))
	defer serverA.Close()
	requestsB := 0
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsB++
		writeCompletionWorkerSuccess(w, r)
	}))
	defer serverB.Close()
	clientA, err := NewClient(ClientConfig{BaseURL: serverA.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := NewClient(ClientConfig{BaseURL: serverB.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completionB := enqueueWorkerTestCompletion(t, queries, clientB.TargetIdentity(), "target-b")
	completionA := enqueueWorkerTestCompletion(t, queries, clientA.TargetIdentity(), "target-a")
	t.Cleanup(func() {
		pool.Exec(context.Background(), `
			DELETE FROM task_completion_outbox WHERE id = ANY($1::uuid[])
		`, []pgtype.UUID{completionA.ID, completionB.ID})
	})

	workerA := NewCompletionWorker(queries, clientA, nil)
	if worked, processErr := workerA.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("worker A worked=%v error=%v", worked, processErr)
	}
	var statusA, statusB string
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT status FROM task_completion_outbox WHERE id = $1),
			(SELECT status FROM task_completion_outbox WHERE id = $2)
	`, completionA.ID, completionB.ID).Scan(&statusA, &statusB); err != nil {
		t.Fatal(err)
	}
	if requestsA != 1 || requestsB != 0 || statusA != "delivered" || statusB != "queued" {
		t.Fatalf("after worker A: requests=%d/%d status=%s/%s",
			requestsA, requestsB, statusA, statusB)
	}

	workerB := NewCompletionWorker(queries, clientB, nil)
	if worked, processErr := workerB.ProcessNext(context.Background()); processErr != nil || !worked {
		t.Fatalf("worker B worked=%v error=%v", worked, processErr)
	}
	if requestsB != 1 {
		t.Fatalf("worker B requests = %d", requestsB)
	}
}
