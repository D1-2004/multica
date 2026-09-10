package inboundcoord

import (
	"fmt"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

type finishParticipationCheck struct {
	SourceRefs     []string `json:"source_refs"`
	Basis          string   `json:"basis"`
	RecipientQuote string   `json:"recipient_quote"`
	EvidenceRef    string   `json:"evidence_ref"`
	Disposition    string   `json:"disposition"`
}

func requiresParticipationCheck(turn Turn) bool {
	return turn.Loop != LoopTaskFinished && turn.ProactiveConversation && strings.EqualFold(turn.ChatType, "group")
}

func addParticipationCheckSchema(tool *openai.ChatCompletionToolUnionParam, refs []string) {
	p := tool.OfFunction.Function.Parameters
	p["required"] = append(p["required"].([]string), "participation_checks")
	p["properties"].(map[string]any)["participation_checks"] = map[string]any{
		"type": "array", "description": "Independently determine each utterance's intended respondent and required disposition from the original text, BEFORE accepting the candidate explanation. Cover each current source_ref exactly once. Group refs sharing the same grounded recipient evidence and disposition to keep bursts compact.",
		"items": map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"source_refs", "basis", "recipient_quote", "evidence_ref", "disposition"},
			"properties": map[string]any{
				"source_refs":     map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": refs}},
				"basis":           map[string]any{"type": "string", "enum": []string{"direct", "open_call", "dialogue", "existing_work", "other", "unknown"}, "description": "direct: this employee is the intended respondent; open_call: a current role/group invitation includes it; dialogue/existing_work require a loaded read_ref; other: another respondent; unknown: no grounded respondent. False explicit-mention metadata does not negate a natural name address."},
				"recipient_quote": map[string]any{"type": "string", "maxLength": 160, "description": "For direct/open_call, quote only the exact words in this source identifying the requested respondent/role/group, not the request verb, the beneficiary or a name copied from configuration. A presence query without a respondent expression supplies no such quote. For other, quote an explicit other respondent if present; otherwise empty."},
				"evidence_ref":    map[string]any{"type": "string", "description": "For dialogue/existing_work, copy a loaded history/work read_ref that grounds the relationship. Otherwise empty."},
				"disposition":     map[string]any{"type": "string", "enum": []string{"ignore", "coordinate", "work"}, "description": "Required response to the ORIGINAL utterance, not a copy of candidate.kind. A greeting to the employee needs coordinate; other/unknown needs ignore. A status/presence reminder of accepted work needs coordinate, not another execution. Authorized substantive work needs work."},
			}},
	}
}

// The model supplies semantic judgments. Host checks their explicit consistency
// and quote/read provenance, never a vocabulary of names or message intents.
func validateFinishParticipationChecks(result *finishCheckResult, turn Turn, decision Decision) error {
	if !requiresParticipationCheck(turn) {
		return nil
	}
	utterances := windowUtterances(turn)
	if len(result.ParticipationChecks) == 0 {
		return fmt.Errorf("participation_checks must independently assess every current source_ref")
	}
	seen := map[string]bool{}
	for _, check := range result.ParticipationChecks {
		if len(check.SourceRefs) == 0 {
			return fmt.Errorf("participation check requires source_refs")
		}
		for _, sourceRef := range check.SourceRefs {
			index := -1
			for i := range utterances {
				if sourceRef == fmt.Sprintf("u%d", i+1) {
					index = i
					break
				}
			}
			if index < 0 || seen[sourceRef] || !oneOf(check.Basis, "direct", "open_call", "dialogue", "existing_work", "other", "unknown") || !oneOf(check.Disposition, "ignore", "coordinate", "work") {
				return fmt.Errorf("participation_checks has an unknown/duplicate source or invalid judgment")
			}
			seen[sourceRef] = true
			grounded := true
			if check.Basis == "direct" || check.Basis == "open_call" {
				grounded = strings.TrimSpace(check.RecipientQuote) != "" && strings.Contains(utterances[index].Text, check.RecipientQuote)
			}
			if check.Basis == "dialogue" || check.Basis == "existing_work" {
				grounded = false
				for _, read := range turn.CoordinationReads {
					if read.ReadRef == check.EvidenceRef && !read.Failed {
						if check.Basis == "dialogue" && read.Tool == toolContextRead && read.Kind == "history" && turn.HistoryStatus == "loaded" {
							grounded = true
						}
						if check.Basis == "existing_work" && (read.Tool == toolAssocRecall || read.Tool == toolWorkState) {
							grounded = true
						}
					}
				}
			}
			actual := "ignore"
			for _, action := range decision.CoordinationActions {
				if !slices.Contains(action.SourceRefs, sourceRef) {
					continue
				}
				if action.Kind == "start_work" || action.Kind == "continue_work" {
					actual = "work"
				} else if action.Kind != "ignore" && actual != "work" {
					actual = "coordinate"
				}
			}
			// Absence of a loaded dialogue is not evidence of no dialogue.
			// An explicit other-recipient quote can settle this without a read.
			explicitOther := check.Basis == "other" && strings.TrimSpace(check.RecipientQuote) != "" && strings.Contains(utterances[index].Text, check.RecipientQuote)
			if result.Verdict == "allow" && actual == "ignore" && oneOf(check.Basis, "other", "unknown") && turn.HistoryStatus == "not_loaded" && strings.TrimSpace(turn.ConversationID) != "" && !explicitOther {
				result.Verdict = "revise"
				result.Reason = "Source " + sourceRef + " has no grounded respondent and history is not_loaded. Read context_read(kind=history) once before concluding no ongoing dialogue; compare authors, original timestamps, replies and intervening messages. A failed read is unknown, not evidence of absence."
			}
			if result.Verdict == "allow" && (!grounded || actual != check.Disposition || (oneOf(check.Basis, "other", "unknown") && actual != "ignore")) {
				result.Verdict = "revise"
				result.Reason = "Source " + sourceRef + " participation judgment (" + check.Basis + ", " + check.Disposition + ") does not ground candidate " + actual + "; repair this source using original recipient evidence, preserving other requests."
			}
		}
	}
	if len(seen) != len(utterances) {
		return fmt.Errorf("participation_checks omitted current sources")
	}
	return nil
}
