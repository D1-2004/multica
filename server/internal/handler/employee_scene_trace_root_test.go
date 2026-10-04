package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

func TestEmployeeTraceRootReportsSavedFailureWithoutChangingCommitState(t *testing.T) {
	const failure = "context deadline exceeded token=FAILURE_SECRET"
	for _, tc := range []struct {
		name, failure, state, status string
		quiet, committed, replay     bool
		err                          error
	}{
		{name: "success", committed: true, state: "outbox_committed"},
		{name: "quiet", quiet: true, committed: true, state: "quiet_committed"},
		{name: "success_replay", committed: true, replay: true, state: "outbox_committed"},
		{name: "saved_failure", failure: failure, committed: true, state: "outbox_committed", status: "context deadline exceeded token=[redacted]"},
		{name: "failure_replay", failure: failure, committed: true, replay: true, state: "outbox_committed", status: "context deadline exceeded token=[redacted]"},
		{name: "original_error_wins", failure: failure, err: errors.New("commit failed api_key=ERROR_SECRET"), state: "incomplete", status: "commit failed api_key=[redacted]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, exporter := employeeTraceClient(t)
			trace := client.StartTrace(context.Background(), langfuse.TraceOptions{Name: "employee_loop", Type: langfuse.TypeAgent})
			saved := employeeSavedOutcome{Failure: tc.failure, Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: "recorded answer"}}}
			if tc.quiet {
				saved.Outcome.Kind = employeeloop.Quiet
			}
			if tc.replay {
				raw, err := json.Marshal(saved)
				if err != nil {
					t.Fatal(err)
				}
				saved = employeeSavedOutcome{}
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
			}
			employeeTraceFinish(trace, saved, tc.committed, tc.err)
			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("root completion emitted %d spans", len(spans))
			}
			root := spans[0]
			wantLevel := ""
			if tc.status != "" {
				wantLevel = "ERROR"
			}
			if level := employeeTraceAttr(root, "langfuse.observation.level"); level != wantLevel {
				t.Errorf("level=%q want %q", level, wantLevel)
			}
			if status := employeeTraceAttr(root, "langfuse.observation.status_message"); status != tc.status {
				t.Errorf("status=%q want %q", status, tc.status)
			}
			var output struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal([]byte(employeeTraceAttr(root, "langfuse.observation.output")), &output); err != nil {
				t.Fatal(err)
			}
			if output.State != tc.state {
				t.Fatalf("commit state=%q want %q", output.State, tc.state)
			}
			raw, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"FAILURE_SECRET", "ERROR_SECRET"} {
				if strings.Contains(string(raw), secret) {
					t.Fatalf("root leaked %s", secret)
				}
			}
		})
	}
}
