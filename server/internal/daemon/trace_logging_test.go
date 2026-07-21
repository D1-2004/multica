package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

type traceLoggingBackend struct{}

func (traceLoggingBackend) Execute(_ context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	messages := make(chan agent.Message, 3)
	messages <- agent.Message{Type: agent.MessageStatus, Status: "running", SessionID: "session-1"}
	messages <- agent.Message{Type: agent.MessageText, Content: "first answer"}
	messages <- agent.Message{Type: agent.MessageText, Content: "second answer"}
	close(messages)

	results := make(chan agent.Result, 1)
	results <- agent.Result{Status: "completed", SessionID: "session-1", Output: "done"}
	return &agent.Session{Messages: messages, Result: results}, nil
}

func decodeTraceLogEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func findTraceEvents(events []map[string]any, name string) []map[string]any {
	var matches []map[string]any
	for _, event := range events {
		if event["event"] == name {
			matches = append(matches, event)
		}
	}
	return matches
}

func TestTaskTraceFieldsRoundTrip(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"id":"task-1","trace_id":"trace-1","trace_started_at_unix_ms":1721462400123}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.TraceID != "trace-1" || task.TraceStartedAtUnixMS != 1721462400123 {
		t.Fatalf("trace fields were not preserved: %+v", task)
	}
}

func TestExecuteAndDrainEmitsTraceMilestonesForHermesAndOpenCode(t *testing.T) {
	for _, provider := range []string{"hermes", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil)).With(
				"task_id", "task-1",
				"trace_id", "trace-1",
				"trace_started_at_unix_ms", int64(1721462400123),
				"provider", provider,
			)
			d := newTestDaemon(t)
			var seq atomic.Int32
			result, _, err := d.executeAndDrain(context.Background(), traceLoggingBackend{}, "prompt", agent.ExecOptions{}, logger, "task-1", &seq)
			if err != nil {
				t.Fatalf("executeAndDrain: %v", err)
			}
			if result.Status != "completed" {
				t.Fatalf("status = %q", result.Status)
			}

			events := decodeTraceLogEvents(t, logs.Bytes())
			for _, name := range []string{
				"provider_execute_started",
				"provider_first_event",
				"provider_first_text",
				"provider_result_received",
			} {
				matches := findTraceEvents(events, name)
				if len(matches) != 1 {
					t.Fatalf("event %s count = %d, logs=%s", name, len(matches), logs.String())
				}
				if matches[0]["task_id"] != "task-1" || matches[0]["trace_id"] != "trace-1" || matches[0]["provider"] != provider {
					t.Fatalf("event %s lost correlation fields: %#v", name, matches[0])
				}
				if elapsed, ok := matches[0]["stage_elapsed_ms"].(float64); !ok || elapsed < 0 {
					t.Fatalf("event %s has invalid stage_elapsed_ms: %#v", name, matches[0]["stage_elapsed_ms"])
				}
			}
			firstText := findTraceEvents(events, "provider_first_text")[0]
			if firstText["content_bytes"] != float64(len("first answer")) {
				t.Fatalf("content_bytes = %#v", firstText["content_bytes"])
			}
		})
	}
}

func TestReportTaskResultLogsCompleteCallbackAcknowledgement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil)).With(
		"task_id", "task-1",
		"trace_id", "trace-1",
		"trace_started_at_unix_ms", int64(1721462400123),
	)
	d := &Daemon{client: NewClient(srv.URL), logger: logger}
	d.reportTaskResult(context.Background(), "task-1", TaskResult{Status: "completed"}, logger)

	events := findTraceEvents(decodeTraceLogEvents(t, logs.Bytes()), "complete_callback_acknowledged")
	if len(events) != 1 {
		t.Fatalf("complete callback acknowledgement count = %d, logs=%s", len(events), logs.String())
	}
	if events[0]["task_id"] != "task-1" || events[0]["trace_id"] != "trace-1" {
		t.Fatalf("complete callback lost correlation fields: %#v", events[0])
	}
	if elapsed, ok := events[0]["stage_elapsed_ms"].(float64); !ok || elapsed < 0 {
		t.Fatalf("invalid stage_elapsed_ms: %#v", events[0]["stage_elapsed_ms"])
	}
}
