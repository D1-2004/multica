package inboundcoord

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
)

func workWinsOverSpeakRepairReason() string {
	return "Use start_work or continue_work; speaking kinds cannot settle a work request. Put the human acknowledgement on that work action's reply."
}

func applyWorkWinsOverSpeakRepair(result *finishCheckResult, turn Turn, decision Decision) {
	if result == nil || result.Verdict != "allow" {
		return
	}
	if hasWorkDestination(decision) || !windowRequestsWork(turn) {
		return
	}
	if speakOnlyDestination(decision) || declineWithoutStandingRestriction(decision, turn) {
		result.Verdict = "revise"
		result.Reason = workWinsOverSpeakRepairReason()
	}
}

func declineWithoutStandingRestriction(decision Decision, turn Turn) bool {
	hasDecline := false
	for _, action := range decision.CoordinationActions {
		switch action.Kind {
		case "start_work", "continue_work":
			return false
		case "decline":
			hasDecline = true
			if standingRestrictionQuote(action.ConstraintQuote, turn) {
				return false
			}
		}
	}
	return hasDecline
}

func standingRestrictionQuote(quote string, turn Turn) bool {
	q := strings.TrimSpace(quote)
	if q == "" || quoteIsCurrentWorkUtterance(q, turn) {
		return false
	}
	if strings.Contains(configuredPersona(turn), q) || strings.Contains(configuredReplyTone(turn), q) {
		return true
	}
	if contract, state := currentCoordinatorContract(turn); state == coordinatorcontract.StateLoaded && contract != nil {
		if strings.Contains(string(coordinatorcontract.Marshal(contract)), q) {
			return true
		}
	}
	for _, utterance := range windowUtterances(turn) {
		if strings.Contains(utterance.Text, q) && !workRequestUtterance(q) && strings.TrimSpace(utterance.Text) != q {
			return true
		}
	}
	return false
}

func windowRequestsWork(turn Turn) bool {
	for _, utterance := range windowUtterances(turn) {
		if workRequestUtterance(utterance.Text) {
			return true
		}
	}
	return false
}

func workRequestUtterance(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	for _, needle := range []string{"帮我", "请帮", "请你", "新建", "创建", "建一个", "建单", "查一下", "改成", "为什么不用"} {
		if strings.Contains(t, needle) {
			return true
		}
	}
	return false
}

func hasWorkDestination(decision Decision) bool {
	for _, action := range decision.CoordinationActions {
		if action.Kind == "start_work" || action.Kind == "continue_work" {
			return true
		}
	}
	return false
}

func speakOnlyDestination(decision Decision) bool {
	if len(decision.CoordinationActions) == 0 {
		return false
	}
	for _, action := range decision.CoordinationActions {
		switch action.Kind {
		case "acknowledge", "describe_capabilities", "report_memory", "ignore":
		case "clarify":
			hasAuthorization := false
			for _, field := range action.MissingFields {
				if field == "authorization" {
					hasAuthorization = true
					break
				}
			}
			if !hasAuthorization {
				return false
			}
		default:
			return false
		}
	}
	return true
}
