//go:build unix

package agent

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pi 0.84.4's JSON print mode returns exit code zero for a completed
// assistant message with stopReason=error. Only text mode turns that into
// a process failure. These fixtures exercise the JSON protocol without a CLI
// installation or any model credentials.
func piTerminalFixture(t *testing.T, events []string) (Result, []Message) {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "pi")
	writeTestExecutable(t, fake, []byte(piEventStreamScript(events)))
	backend, err := New("pi", Config{ExecutablePath: fake, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(t.Context(), "Calculate the requested result", ExecOptions{
		Timeout: 5 * time.Second, ResumeSessionID: filepath.Join(dir, "session.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var messages []Message
	for message := range session.Messages {
		messages = append(messages, message)
	}
	return <-session.Result, messages
}

func TestPiExecuteReportsAssistantFailureWithZeroExit(t *testing.T) {
	// Real Employee Direct failure: the model proxy returned HTTP 401 with
	// "invalid daemon token", but the former adapter reported completed/empty.
	failed := `{"role":"assistant","content":[],"model":"qwen3.8-max","stopReason":"error","errorMessage":"invalid daemon token","usage":{"input":0,"output":0}}`
	for _, event := range []string{
		`{"type":"message_end","message":` + failed + `}`,
		`{"type":"turn_end","message":` + failed + `}`,
		`{"type":"agent_end","messages":[` + failed + `]}`,
	} {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err != nil {
			t.Fatal(err)
		}
		t.Run(envelope.Type, func(t *testing.T) {
			result, messages := piTerminalFixture(t, []string{`{"type":"agent_start"}`, `{"type":"turn_start"}`, event})
			if result.Status != "failed" || !strings.Contains(result.Error, "invalid daemon token") {
				t.Fatalf("provider failure became success: %+v", result)
			}
			found := false
			for _, message := range messages {
				found = found || (message.Type == MessageError && strings.Contains(message.Content, "invalid daemon token"))
			}
			if !found {
				t.Fatalf("provider failure missing from task transcript: %+v", messages)
			}
		})
	}
}

func TestPiExecuteUsesFinalAssistantStateAfterRetry(t *testing.T) {
	failed := `{"role":"assistant","content":[],"stopReason":"error","errorMessage":"temporary overload"}`
	completed := `{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"stop"}`
	result, messages := piTerminalFixture(t, []string{
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":` + failed + `}`,
		`{"type":"agent_end","messages":[` + failed + `],"willRetry":true}`,
		`{"type":"auto_retry_start","attempt":1}`,
		`{"type":"turn_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"done"}}`,
		`{"type":"message_end","message":` + completed + `}`,
		`{"type":"auto_retry_end","success":true,"attempt":1}`,
		`{"type":"turn_end","message":` + completed + `}`,
		`{"type":"agent_end","messages":[` + failed + `,` + completed + `]}`,
	})
	if result.Status != "completed" || result.Output != "done" || result.Error != "" {
		t.Fatalf("successful recovery retained earlier failure: %+v", result)
	}
	for _, message := range messages {
		if message.Type == MessageError {
			t.Fatalf("recovered error was reported as final: %+v", message)
		}
	}
}

func TestPiExecutePreservesQuietSuccessAndAssistantAbort(t *testing.T) {
	for _, tc := range []struct{ reason, status string }{{"stop", "completed"}, {"aborted", "aborted"}, {"error", "failed"}} {
		t.Run(tc.reason, func(t *testing.T) {
			result, _ := piTerminalFixture(t, []string{
				`{"type":"message_end","message":{"role":"toolResult","stopReason":"error","errorMessage":"recoverable tool failure"}}`,
				`{"type":"turn_end","message":{"role":"assistant","content":[],"stopReason":"` + tc.reason + `"}}`,
			})
			if result.Status != tc.status || result.Output != "" {
				t.Fatalf("unexpected empty terminal result: %+v", result)
			}
			if (result.Error == "") != (tc.status == "completed") {
				t.Fatalf("missing failure reason or quiet result became failure: %+v", result)
			}
		})
	}
}
