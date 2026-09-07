package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCoordinatorDispatchResultRequiresSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body, action string
		status             int
	}{
		{"created", `{"continuation":{"kind":"issue","issueId":"issue"},"issueIdentifier":"MUL-1","taskId":"run"}`, "issue_created", http.StatusCreated},
		{"commented", `{"continuation":{"kind":"issue","issueId":"issue"},"commentId":"comment","taskId":"run"}`, "issue_commented", http.StatusCreated},
		{"recovered", `{"continuation":{"kind":"issue","issueId":"issue"},"taskId":"run"}`, "issue_linked", http.StatusAccepted},
		{"failed", `{"continuation":{"kind":"issue","issueId":"issue"}}`, "", http.StatusInternalServerError},
		{"chat", `{"continuation":{"kind":"chat","chatSessionId":"chat"}}`, "", http.StatusAccepted},
		{"missing id", `{"continuation":{"kind":"issue"}}`, "", http.StatusCreated},
		{"malformed", `oops`, "", http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := newBufferedDispatchResponse()
			response.WriteHeader(tc.status)
			_, _ = response.Write([]byte(tc.body))
			decision := coordinatorDecisionWithDispatchResult(inboundcoord.Decision{Action: inboundcoord.ActionIssue}, response)
			if tc.action == "" {
				if len(decision.IssueResults) != 0 {
					t.Fatalf("unexpected success: %#v", decision.IssueResults)
				}
				return
			}
			if len(decision.IssueResults) != 1 || decision.IssueResults[0].Action != tc.action || decision.IssueResults[0].IssueID != "issue" || decision.IssueResults[0].TaskID != "run" {
				t.Fatalf("result = %#v", decision.IssueResults)
			}
		})
	}
}

func TestCoordinatorDispatchResultPreservesMultipleWindowItems(t *testing.T) {
	response := newBufferedDispatchResponse()
	writeJSON(response, http.StatusCreated, AgentDispatchResponse{
		Continuation: AgentDispatchContinuation{Kind: "issue", IssueID: "first"},
	})
	decision := coordinatorDecisionWithDispatchResult(inboundcoord.Decision{
		IssueResults: []protocol.ChatCoordinatorIssueResult{
			{Action: "issue_created", IssueID: "first"},
			{Action: "issue_created", IssueID: "second"},
		},
	}, response)
	if len(decision.IssueResults) != 2 || decision.IssueResults[1].IssueID != "second" {
		t.Fatalf("window results lost: %#v", decision.IssueResults)
	}
}

func TestCoordinatorFailureRetainsCommittedIssueAndOriginalSteps(t *testing.T) {
	observed := inboundcoord.Decision{
		Action: inboundcoord.ActionIssue, UserText: "Starting both tasks.",
		IssueResults: []protocol.ChatCoordinatorIssueResult{{Action: "issue_created", IssueID: "first", TaskID: "run"}},
		Steps:        []protocol.ChatCoordinatorStep{{Seq: 1, Type: "tool_use", Tool: "finish"}},
	}
	decision := failedCoordinatorDecision(DispatchCommand{}, "second item failed", &observed)
	if len(decision.IssueResults) != 1 || decision.IssueResults[0].IssueID != "first" {
		t.Fatalf("committed result lost: %#v", decision.IssueResults)
	}
	if len(decision.Steps) != 2 || decision.Steps[1].Type != "error" || decision.Steps[1].Content != "second item failed" {
		t.Fatalf("failure not recorded: %#v", decision.Steps)
	}
	if len(observed.Steps) != 1 || decision.UserText == observed.UserText {
		t.Fatal("failure must preserve the original verdict and replace its success acknowledgement")
	}
}
