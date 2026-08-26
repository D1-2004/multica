package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTaskCompleteRequestIgnoresLegacyResultMessage(t *testing.T) {
	var request TaskCompleteRequest
	if err := json.Unmarshal([]byte(`{"output":"provider final output","result_message":"旧 DWS 工具回执"}`), &request); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Output != "provider final output" {
		t.Fatalf("output = %q", payload.Output)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["result_message"]; ok {
		t.Fatalf("legacy result_message survived typed payload: %#v", raw)
	}
}

func TestTaskFailRequestIgnoresLegacyResultMessage(t *testing.T) {
	var request TaskFailRequest
	if err := json.Unmarshal([]byte(`{"error":"runtime timed out","result_message":"旧 DWS 工具回执"}`), &request); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "runtime timed out" {
		t.Fatalf("error = %#v", payload["error"])
	}
	if _, ok := payload["result_message"]; ok {
		t.Fatalf("legacy result_message survived typed payload: %#v", payload)
	}
}
