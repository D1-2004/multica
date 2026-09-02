package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRestoreInboundCoordinatorCommandRestoresPrivateExecutionFields(t *testing.T) {
	t.Parallel()
	endpointID := parseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	targetIdentity := "router-target:v1:sha256:" + strings.Repeat("b", 64)
	original := DispatchCommand{
		DispatchEndpointID: "must-not-survive-json",
		CompletionCallback: &DispatchCompletionCallback{
			URL:    "/api/v1/dispatch-tasks/test/execution-result",
			Target: targetIdentity,
		},
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := restoreInboundCoordinatorCommand(raw, endpointID, targetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DispatchEndpointID != uuidToString(endpointID) {
		t.Fatalf("dispatch endpoint = %q", restored.DispatchEndpointID)
	}
	if restored.CompletionCallback == nil || restored.CompletionCallback.Target != targetIdentity {
		t.Fatalf("completion target = %#v", restored.CompletionCallback)
	}
}
