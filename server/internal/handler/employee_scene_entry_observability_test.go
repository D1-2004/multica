package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEmployeeSceneCompletionLogsPersistedModelCountsWithoutContent(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Messages[0].Text = "PRIVATE_REQUEST_SENTINEL: analyze this feedback"
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions='PRIVATE_INSTRUCTIONS_SENTINEL' WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	var receipt, jobID string
	var before int
	if err := testPool.QueryRow(ctx, `SELECT c.receipt_id::text,j.id::text,j.model_attempts FROM employee_event_consumption c JOIN employee_scene_job j ON j.id=c.job_id WHERE c.agent_id=$1`, f.agentID).Scan(&receipt, &jobID, &before); err != nil || before != 0 {
		t.Fatalf("fresh job budget=%d %v", before, err)
	}
	model.dispatch = true
	model.sourceRef = receipt + "/message-1"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	foundAdmission, foundCompletion := false, false
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		switch event["event"] {
		case "employee_scene_entry_admitted":
			foundAdmission = event["job_id"] == jobID && event["receipt_id"] == receipt
			messages, _ := event["source_message_ids"].([]any)
			if len(messages) != 1 || messages[0] != "message-1" {
				t.Fatalf("source OpenMsgID correlation missing: %s", line)
			}
		case "employee_scene_job_completed":
			foundCompletion = true
			if event["job_id"] != jobID || event["model_attempts"] != float64(1) || event["model_response_count"] != float64(1) || event["model_failure_count"] != float64(0) || event["kind"] != "dispatched" {
				t.Fatalf("completion used stale claim count or lost identity: %s", line)
			}
			if len(event["run_ids"].([]any)) != 1 || len(event["action_ids"].([]any)) != 1 {
				t.Fatalf("missing committed effect/delivery mapping: %s", line)
			}
		}
	}
	if !foundAdmission || !foundCompletion {
		t.Fatalf("missing committed observation: admitted=%v completed=%v", foundAdmission, foundCompletion)
	}
	for _, secret := range []string{"PRIVATE_REQUEST_SENTINEL", "PRIVATE_INSTRUCTIONS_SENTINEL", "Analyze feedback and report the evidence"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("private content reached logs: %s", secret)
		}
	}
}
