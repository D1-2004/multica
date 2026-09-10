package inboundcoord

import (
	"encoding/json"
	"strings"
)

// Project only existing trusted reads next to a continuation proposal. This is
// evidence presentation, not a semantic match or an additional retrieval.
type finishExistingWork struct {
	ReadStatus   string `json:"read_status"`
	ReadRef      string `json:"read_ref,omitempty"`
	IssueID      string `json:"issue_id"`
	OriginalGoal string `json:"original_goal,omitempty"`
	Truncated    bool   `json:"truncated"`
}

func existingWorkForFinish(turn Turn, issueID string) *finishExistingWork {
	for i := len(turn.CoordinationReads) - 1; i >= 0; i-- {
		read := turn.CoordinationReads[i]
		if read.Failed {
			continue
		}
		goal := ""
		truncated := false
		switch read.Tool {
		case toolWorkState:
			var view coordinationWorkState
			if json.Unmarshal(read.Result, &view) == nil && view.IssueID == issueID {
				goal, truncated = view.OriginalGoal, view.Truncated
			}
		case toolAssocRecall:
			var view coordinatorRecallView
			if json.Unmarshal(read.Result, &view) == nil && (view.Status == "loaded" || view.Status == "empty") {
				for _, item := range view.Items {
					if item.IssueID == issueID {
						goal, truncated = item.Purpose, item.Truncated
						break
					}
				}
			}
		}
		if strings.TrimSpace(goal) != "" {
			return &finishExistingWork{ReadStatus: "loaded", ReadRef: read.ReadRef, IssueID: issueID, OriginalGoal: goal, Truncated: truncated}
		}
	}
	return &finishExistingWork{ReadStatus: "unavailable", IssueID: issueID}
}
