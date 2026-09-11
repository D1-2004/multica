package agentmessagerouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubmitExecutionUpdateResultMessageProtocol(t *testing.T) {
	tests := []struct {
		name          string
		resultMessage string
		wantPresent   bool
	}{
		{name: "present", resultMessage: "任务已转入后台", wantPresent: true},
		{name: "empty omitted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var received map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Errorf("decode request: %v", err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"code":    "success",
					"data": map[string]string{
						"dispatchTaskId": "router-update-result-message",
						"requestId":      "multica-handoff:source-task",
						"updateType":     "delegated_to_issue",
					},
				})
			}))
			defer server.Close()

			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "service-secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.SubmitExecutionUpdate(
				context.Background(),
				"/api/v1/dispatch-tasks/router-update-result-message/execution-update",
				ExecutionUpdateRequest{
					RequestID:      "multica-handoff:source-task",
					AgentID:        "source-agent",
					ExternalTaskID: "source-task",
					UpdateType:     "delegated_to_issue",
					OccurredAt:     1787596800000,
					ResultMessage:  tt.resultMessage,
					Extension: ExecutionUpdateExtension{
						IssueID:         "issue-1",
						IssueIdentifier: "MUL-1",
						TargetTaskID:    "target-task",
						TargetAgentID:   "target-agent",
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			resultMessage, present := received["resultMessage"]
			if present != tt.wantPresent {
				t.Fatalf("resultMessage present = %v, want %v; body=%#v", present, tt.wantPresent, received)
			}
			if tt.wantPresent && resultMessage != tt.resultMessage {
				t.Fatalf("resultMessage = %#v, want %q", resultMessage, tt.resultMessage)
			}
		})
	}
}
