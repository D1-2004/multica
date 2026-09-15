package inboundcoord

import (
	"fmt"
	"strings"
)

const receivingIdentityAuthority = "employee_uid is the trusted receiving account. A matching mention identifies this employee even when account names are stale. Persona/job names describe this same employee, not another recipient. response_required is a Host delivery obligation, not permission to execute work."

// sourceResponseRequired projects only trusted routing facts. It does not
// classify message intent or turn a window-level mention into every speaker's
// invitation. Missing metadata is distinct from an explicit empty mention list.
func sourceResponseRequired(turn Turn, utterance WindowUtterance) bool {
	if turn.Source != SourceDigitalEmployee || (turn.Loop != "" && turn.Loop != LoopInbound) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(turn.ChatType)) {
	case "single", "p2p", "direct":
		return true
	}
	if strings.TrimSpace(turn.DWSUID) == "" {
		return false
	}
	if mentionRelation(turn, utterance) == "includes_employee" {
		return true
	}
	return turn.Addressed && !turn.ProactiveConversation && utterance.Mentions == nil && len(windowUtterances(turn)) == 1
}

func requiredResponseRefs(turn Turn) []string {
	var refs []string
	for i, utterance := range windowUtterances(turn) {
		if sourceResponseRequired(turn, utterance) {
			refs = append(refs, fmt.Sprintf("u%d", i+1))
		}
	}
	return refs
}

// enforceRequiredResponses repairs an allowed ignore using the same trusted
// facts shown to both models. It requires a response, never a work destination.
func enforceRequiredResponses(result *finishCheckResult, turn Turn, decision Decision) {
	if result.Verdict != "allow" {
		return
	}
	required := requiredResponseRefs(turn)
	var ignored []string
	for _, ref := range required {
		for _, action := range decision.CoordinationActions {
			if action.Kind != "ignore" {
				continue
			}
			for _, sourceRef := range action.SourceRefs {
				if sourceRef == ref {
					ignored = append(ignored, ref)
					break
				}
			}
			if len(ignored) > 0 && ignored[len(ignored)-1] == ref {
				break
			}
		}
	}
	if len(ignored) == 0 {
		return
	}
	result.Verdict = "revise"
	result.Reason = "Required direct response ignored. Use acknowledge(ack_kind=conversation) for social feedback; dispatch actual work. Names/persona cannot negate receipt."
	result.MissingSourceRefs = ignored
	result.ConstraintQuote = ""
}
