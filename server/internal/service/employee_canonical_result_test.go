package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func canonicalResultEnvelope(t *testing.T, output string) []byte {
	t.Helper()
	raw, err := json.Marshal(protocol.TaskCompletedPayload{Output: output})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestTaskExecutionCanonicalResultPreservesProtocolAndRefusals(t *testing.T) {
	output := ` {"version":"tag-round-result/v1","summary":"first\nsecond\t\"quote\" C:\\new\\repo literal\\n","choice":{"intent":"suggest"}} `
	got := taskExecutionCanonicalResult(canonicalResultEnvelope(t, output))
	if got != output || !json.Valid([]byte(got)) {
		t.Fatal("machine output was unescaped or reformatted", got)
	}
	for _, raw := range []string{
		`{"version":"other","version":"tag-round-result/v1","summary":"ok"}`,
		`{"version":"tag-round-result/v1","summary":"ok","task_id":"forged"}`,
	} {
		if got := taskExecutionCanonicalResult(canonicalResultEnvelope(t, raw)); got != raw {
			t.Fatal("unknown, duplicate or malformed control data was repaired", got)
		}
	}
	for _, raw := range []string{
		"{\"version\":\"tag-round-result/v1\",\"summary\":\"bad\ncontrol\"}",
		"{\"version\":\"tag-round-result/v1\",\"summary\":\"-----BEGIN PRIVATE KEY-----\nABC\n-----END PRIVATE KEY-----\"}",
	} {
		got := taskExecutionCanonicalResult(canonicalResultEnvelope(t, raw))
		if json.Valid([]byte(got)) || strings.Contains(got, "ABC") {
			t.Fatal("redaction repaired malformed JSON into an acceptable protocol", got)
		}
	}
	if got := taskExecutionCanonicalResult([]byte(`{"output":`)); got != "" {
		t.Fatal("broken transport envelope accepted", got)
	}
}

func TestTaskExecutionCanonicalResultRedactsValuesWithoutDamagingJSON(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 40)
	summary := "first\nAPI_KEY=fictional-test-value\n" + token + "\nlast \\\"quoted\\\""
	raw, _ := json.Marshal(map[string]any{"version": "tag-round-result/v1", "summary": summary})
	got := taskExecutionCanonicalResult(canonicalResultEnvelope(t, string(raw)))
	var result map[string]any
	if json.Unmarshal([]byte(got), &result) != nil || result["version"] != "tag-round-result/v1" || strings.Contains(got, token) || strings.Contains(got, "fictional-test-value") || !strings.Contains(result["summary"].(string), "\nlast") {
		t.Fatal("redaction leaked credentials or damaged protocol", got)
	}
}

func TestDirectCompleteTaskKeepsCanonicalContractAndLegacyText(t *testing.T) {
	for _, machine := range []bool{true, false} {
		name, output := "legacy_text", `first\nsecond`
		if machine {
			name, output = "machine_contract", `{"version":"tag-round-result/v1","summary":"first\nsecond\t\"quote\" C:\\new\\repo"}`
		}
		t.Run(name, func(t *testing.T) {
			f := directDatabase(t)
			if machine {
				f.request.Context = json.RawMessage(`{"employee_round_result_contract":"tag-round-result/v1"}`)
			}
			ctx := context.Background()
			accepted, err := f.service.EnqueueDirectTask(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, accepted.Task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.CompleteTask(ctx, accepted.Task.ID, canonicalResultEnvelope(t, output), "", "", false, ""); err != nil {
				t.Fatal(err)
			}
			var stored, state string
			if err := f.pool.QueryRow(ctx, `SELECT state,result FROM employee_task_run WHERE id=$1`, accepted.Run.ID).Scan(&state, &stored); err != nil {
				t.Fatal(err)
			}
			want := "first\nsecond"
			if machine {
				want = output
				if !json.Valid([]byte(stored)) {
					t.Fatal("CompleteTask corrupted machine JSON before Run storage", stored)
				}
			}
			if state != "succeeded" || stored != want {
				t.Fatal("canonical/legacy contract changed", state, stored)
			}
			if _, err := f.service.ReconcileEmployeeRuns(ctx, 10); err != nil {
				t.Fatal("reconciliation broke result replay", err)
			}
		})
	}
}
