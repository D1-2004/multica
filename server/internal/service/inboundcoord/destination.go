package inboundcoord

import (
	"strings"
)

func workWinsOverSpeakRepairReason() string {
	return "Use start_work or continue_work; speaking kinds cannot settle a work request. Put the human acknowledgement on that work action's reply."
}

func applyWorkWinsOverSpeakRepair(result *finishCheckResult, turn Turn, decision Decision) {
	if result == nil || result.Verdict != "allow" {
		return
	}
	if hasWorkDestination(decision) || !speakOnlyDestination(decision) {
		return
	}
	if !windowRequestsWork(turn) {
		return
	}
	result.Verdict = "revise"
	result.Reason = workWinsOverSpeakRepairReason()
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
