package agentmessagerouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

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
		RootTaskID:      pgtype.UUID{Bytes: [16]byte{byte(now), 11}, Valid: true},
		TargetTaskID:    pgtype.UUID{Bytes: [16]byte{byte(now), 12}, Valid: true},
		IssueID:         pgtype.UUID{Bytes: [16]byte{byte(now), 13}, Valid: true},
		IssueIdentifier: "MUL-123",
		CallbackUrl:     "/api/v1/dispatch-tasks/router-" + suffix + "/execution-update",
		TargetIdentity:  targetIdentity,
		RequestID:       fmt.Sprintf("worker-update-test:%s:%d", suffix, now),
		AgentID:         pgtype.UUID{Bytes: [16]byte{byte(now), 14}, Valid: true},
		TargetAgentID:   pgtype.UUID{Bytes: [16]byte{byte(now), 15}, Valid: true},
		UpdateType:      "delegated_to_issue",
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeCompletionWorkerSuccess(w, r)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
	if err != nil {
		t.Fatal(err)
	}
	completion := enqueueWorkerTestCompletion(t, queries, client.TargetIdentity(), "success")
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
