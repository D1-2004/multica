package inboundcoord

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDecisionTraceKeepsCommittedIssueResults(t *testing.T) {
	results := []protocol.ChatCoordinatorIssueResult{
		{Action: "issue_created", IssueID: "first", IssueIdentifier: "MUL-1", TaskID: "run-1"},
		{Action: "issue_created", IssueID: "second", IssueIdentifier: "MUL-2", TaskID: "run-2"},
	}
	decision := Decision{Action: ActionIssue, IssueResults: results}
	var persisted protocol.ChatCoordinatorTrace
	if err := json.Unmarshal(decision.TraceJSON(), &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.IssueResults, results) {
		t.Fatalf("persisted results = %#v", persisted.IssueResults)
	}
	if len((Decision{Action: ActionIssue, IssueID: "model-supplied"}).Trace().IssueResults) != 0 {
		t.Fatal("model intent must not be treated as a committed result")
	}
}

func TestDecisionTraceIncludesTerminalCommentEffect(t *testing.T) {
	decision := Decision{
		Action: ActionReply,
		IssueComment: &IssueCommentEffect{
			IssueID: "existing", IssueIdentifier: "MUL-7", CommentID: "comment", TaskID: "run",
		},
	}
	results := decision.Trace().IssueResults
	if len(results) != 1 || results[0].Action != "issue_commented" || results[0].CommentID != "comment" || results[0].TaskID != "run" {
		t.Fatalf("terminal comment result = %#v", results)
	}
}
