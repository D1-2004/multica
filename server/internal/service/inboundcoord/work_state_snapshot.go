package inboundcoord

import (
	"encoding/json"
	"strconv"
	"strings"
)

const workStateSnapshotReuseReason = "work_state_snapshot_reused"

// Reuse only Host-projected reads produced inside this run and still retained
// in its bounded context. The caller must first enforce the recalled-ID gate.
// The private original arguments prevent an unsupported parameter from sharing
// the canonical issue-only request, without exposing another context field.
func reusableWorkStateSnapshot(turn Turn, arguments string, initialSequence int) (CoordinationRead, bool) {
	issueID, valid := workStateSnapshotIssueID(arguments)
	if !valid {
		return CoordinationRead{}, false
	}
	key := coordinationReadKey(toolWorkState, arguments)
	for i := len(turn.CoordinationReads) - 1; i >= 0; i-- {
		read := turn.CoordinationReads[i]
		if read.Tool != toolWorkState || read.key != key {
			continue
		}
		// Never fall back to an older success after a failed replacement.
		priorID, valid := workStateSnapshotIssueID(read.arguments)
		sequence, err := strconv.Atoi(strings.TrimPrefix(read.ReadRef, "r"))
		if read.Failed || !valid || priorID != issueID || !strings.HasPrefix(read.ReadRef, "r") || err != nil || sequence <= initialSequence {
			return CoordinationRead{}, false
		}
		var state struct {
			coordinationWorkState
			ReadRef string `json:"read_ref"`
		}
		if json.Unmarshal(read.Result, &state) != nil || state.IssueID != issueID || state.ReadRef != read.ReadRef || state.Scope != coordinationIssueScope || state.StatusSource != coordinationIssueStatusSource || state.LatestExecution.ReadStatus == "unavailable" {
			return CoordinationRead{}, false
		}
		// Truncation and unknown execution/delivery remain exactly as read.
		return read, true
	}
	return CoordinationRead{}, false
}

func workStateSnapshotIssueID(arguments string) (string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &fields) != nil || len(fields) != 1 {
		return "", false
	}
	var issueID string
	if json.Unmarshal(fields["issue_id"], &issueID) != nil {
		return "", false
	}
	issueID = strings.TrimSpace(issueID)
	return issueID, issueID != ""
}

// This uses the existing feedback slot, not a second copy of the read result.
// It is a successful-read notice: unavailable is reserved for actual failures.
func workStateSnapshotFeedback(turn Turn, initialSequence int) string {
	for i := len(turn.CoordinationReads) - 1; i >= 0; i-- {
		read := turn.CoordinationReads[i]
		if read.Tool != toolWorkState {
			continue
		}
		cached, ok := reusableWorkStateSnapshot(turn, read.arguments, initialSequence)
		if !ok {
			continue
		}
		body, _ := json.Marshal(map[string]any{
			"tool": toolWorkState, "read_ref": cached.ReadRef, "snapshot_available": true,
			"instruction": "Use this turn's existing work_state snapshot. Repeating the same arguments reuses its read_ref; truncated/complete=false is a bounded summary, not pagination that another identical read can expand. Keep unknown execution/delivery unknown. Decide from the current request and evidence; read history only for a missing original question, or another recalled Issue if needed. Delegate business work or clarify a necessary user choice. This grants no new authority.",
		})
		return string(body)
	}
	return ""
}
