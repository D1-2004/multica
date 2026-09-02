//go:build unix

package agent

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// opencodeVoidStepStream is a stream that does real work and then dies: the
// first step is billed 14585 input tokens, the second opens, emits only
// reasoning (no deliverable, no tokens) and closes with reason "unknown" and an
// all-zero token block. `opencode run` still exits 0. This is the #6522 shape,
// and unlike opencodeStreamTail it trips the terminal-signal guard — which is
// the point of the test below.
const opencodeVoidStepStream = `printf '%s\n' '{"type":"step_start","timestamp":1,"sessionID":"ses_void","part":{"type":"step-start"}}'
printf '%s\n' '{"type":"text","timestamp":2,"sessionID":"ses_void","part":{"type":"text","text":"gathering context"}}'
printf '%s\n' '{"type":"step_finish","timestamp":3,"sessionID":"ses_void","part":{"type":"step-finish","reason":"tool-calls","tokens":{"input":14585,"output":89,"cache":{"write":0,"read":0}}}}'
printf '%s\n' '{"type":"step_start","timestamp":4,"sessionID":"ses_void","part":{"type":"step-start"}}'
printf '%s\n' '{"type":"reasoning","timestamp":5,"sessionID":"ses_void","part":{"type":"reasoning","text":"thinking..."}}'
printf '%s\n' '{"type":"step_finish","timestamp":6,"sessionID":"ses_void","part":{"type":"step-finish","reason":"unknown","tokens":{"input":0,"output":0,"reasoning":0,"cache":{"write":0,"read":0}},"cost":0}}'
`

// TestOpencodeExecuteVoidStepFailureReportsRequestSize pins the diagnostics on
// the guard failure. "the provider produced nothing" on its own is
// unactionable: the original incident was only root-caused by diffing two
// sibling issues and noticing that the 19563-character prompt failed where the
// 4163-character one passed. The prompt size is measured by this very process
// and used to be thrown away, so the message must carry it — along with the
// last request the provider actually billed and how far the run got.
//
// The prefix must survive the append, because taskfailure.Classify keys the
// retryable provider_network bucket on it.
func TestOpencodeExecuteVoidStepFailureReportsRequestSize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Drain stdin to EOF first: OpenCode reads the whole prompt before doing
	// any work, and a fake that never reads it would strand the writer.
	script := "#!/bin/sh\ncat > /dev/null\n" + opencodeVoidStepStream

	fakePath := filepath.Join(dir, "opencode")
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("opencode", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	// The size from the reported incident, so the assertion reads as the real
	// failure would.
	prompt := strings.Repeat("m", 19563)

	session, err := backend.Execute(t.Context(), prompt, ExecOptions{
		Timeout: 30 * time.Second,
		Model:   "lanz/Lanz-Medium",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	result := <-session.Result

	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed; error=%q", result.Status, result.Error)
	}
	if !strings.HasPrefix(result.Error, "opencode stream ended") {
		t.Errorf("error = %q, want it to keep the classifier's stream-ended prefix", result.Error)
	}
	want := fmt.Sprintf(
		"[request: %d prompt bytes on stdin; last accepted step reported 14585 input tokens across 2 steps]",
		len(prompt),
	)
	if !strings.Contains(result.Error, want) {
		t.Errorf("error = %q, want it to contain %q", result.Error, want)
	}
}

// TestOpencodeExecuteVoidStepFailureOmitsTokensWhenNoneBilled covers the shape
// the incident actually produced first: the provider billed nothing at all, so
// there is no last-accepted request to name. The prompt size is still the whole
// point of the message, and a "0 input tokens" figure would invite being read
// as a measurement of the dropped request rather than as its absence.
func TestOpencodeExecuteVoidStepFailureOmitsTokensWhenNoneBilled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := "#!/bin/sh\ncat > /dev/null\n" +
		`printf '%s\n' '{"type":"step_start","timestamp":1,"sessionID":"ses_void0","part":{"type":"step-start"}}'` + "\n" +
		`printf '%s\n' '{"type":"step_finish","timestamp":2,"sessionID":"ses_void0","part":{"type":"step-finish","reason":"unknown","tokens":{"input":0,"output":0,"cache":{"write":0,"read":0}},"cost":0}}'` + "\n"

	fakePath := filepath.Join(dir, "opencode")
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("opencode", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("New(opencode): %v", err)
	}

	prompt := strings.Repeat("m", 4163)

	session, err := backend.Execute(t.Context(), prompt, ExecOptions{
		Timeout: 30 * time.Second,
		Model:   "lanz/Lanz-Medium",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	result := <-session.Result

	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed; error=%q", result.Status, result.Error)
	}
	want := fmt.Sprintf("[request: %d prompt bytes on stdin]", len(prompt))
	if !strings.Contains(result.Error, want) {
		t.Errorf("error = %q, want it to contain %q", result.Error, want)
	}
	if strings.Contains(result.Error, "input tokens") {
		t.Errorf("error = %q, want no token figure when the provider billed nothing", result.Error)
	}
}
