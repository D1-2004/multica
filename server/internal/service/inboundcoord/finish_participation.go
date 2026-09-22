package inboundcoord

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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
	if turn.Loop == LoopTaskFinished || !strings.EqualFold(turn.ChatType, "group") {
		return false
	}
	if turn.ProactiveConversation {
		return true
	}
	if turn.Source == SourceDigitalEmployee {
		for _, utterance := range windowUtterances(turn) {
			if len(utterance.Mentions) > 0 {
				return true
			}
		}
	}
	return false
}

func addParticipationCheckSchema(tool *openai.ChatCompletionToolUnionParam, refs []string) {
	p := tool.OfFunction.Function.Parameters
	p["required"] = append(p["required"].([]string), "participation_checks")
	p["properties"].(map[string]any)["participation_checks"] = map[string]any{
		"type": "array", "description": "Independently determine each utterance's intended respondent and required disposition from the original text, BEFORE accepting the candidate explanation. Cover each current source_ref exactly once. Group refs sharing the same grounded recipient evidence and disposition to keep bursts compact.",
		"items": map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"source_refs", "basis", "recipient_quote", "evidence_ref", "disposition"},
			"properties": map[string]any{
				"source_refs":     map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "enum": refs}},
				"basis":           map[string]any{"type": "string", "enum": []string{"direct", "open_call", "dialogue", "existing_work", "other", "unknown"}, "description": "direct: this employee is the intended respondent; open_call: a current role/group invitation includes it; dialogue/existing_work require a loaded read_ref; other: another respondent; unknown: no grounded respondent. False explicit-mention metadata does not negate a natural name address."},
				"recipient_quote": map[string]any{"type": "string", "maxLength": 160, "description": "For direct/open_call, quote the exact requested respondent/role, not a verb, beneficiary or configuration name. includes_employee proves direct and permits an empty quote. With other_only, formal @ labels and substrings inside them name other UIDs even when their display names match this employee; direct requires an independent quote outside those mention spans. Do not strip @ or brackets to reinterpret a formal mention as natural naming. For other, quote the other respondent if present; otherwise empty."},
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
				grounded = (check.Basis == "direct" && mentionRelation(turn, utterances[index]) == "includes_employee") || (strings.TrimSpace(check.RecipientQuote) != "" && strings.Contains(utterances[index].Text, check.RecipientQuote))
			}
			if check.Basis == "direct" && mentionRelation(turn, utterances[index]) == "other_only" && !recipientQuoteOutsideMentions(utterances[index].Text, check.RecipientQuote) {
				return fmt.Errorf("source %s has trusted other_only mentions: the mentioned UIDs do not match receiving UID %q. direct recipient_quote %q has no occurrence outside the formal @ spans. Correct participation_checks using these UID facts and independent recipient evidence, then reconsider the candidate and verdict. Do not reinterpret the same-name @ label or its substrings as this employee", sourceRef, turn.DWSUID, check.RecipientQuote)
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
				result.HistoryReadRequired = true
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

type formalMentionSpan struct{ start, end int }

// formalMentionSpans identifies syntax only, never names or business intent.
// Channel <@id> notation and bracketed labels have explicit closing bounds.
// Bare multiword labels are ambiguous without provider spans: keep them whole
// until a clear punctuation or line boundary rather than guessing a name end.
func formalMentionSpans(text string) []formalMentionSpan {
	var spans []formalMentionSpan
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], "<@") {
			if end := strings.IndexByte(text[i+2:], '>'); end > 0 {
				label := text[i+2 : i+2+end]
				if strings.IndexFunc(label, unicode.IsSpace) < 0 {
					end += i + 3
					spans = append(spans, formalMentionSpan{i, end})
					i = end
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r != '@' {
			i += size
			continue
		}
		end := i + size
		aliasDepth := 0
		for end < len(text) {
			r, n := utf8.DecodeRuneInString(text[end:])
			if r == '(' || r == '（' || r == '[' || r == '【' {
				aliasDepth++
			}
			if aliasDepth == 0 && formalMentionBoundary(r) {
				break
			}
			closedAlias := false
			if (r == ')' || r == '）' || r == ']' || r == '】') && aliasDepth > 0 {
				aliasDepth--
				closedAlias = aliasDepth == 0
			}
			end += n
			if closedAlias {
				break
			}
		}
		if end > i+size {
			spans = append(spans, formalMentionSpan{i, end})
		}
		i = end
	}
	return spans
}

func formalMentionBoundary(r rune) bool {
	if r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' {
		return true
	}
	switch r {
	case '@', '<', '>', ',', '，', '、', '。', '!', '！', '?', '？', ';', '；', ':', '：', '[', ']', '【', '】', '"', '“', '”', '\'', '‘', '’', '\u200b':
		return true
	}
	return false
}

// A quote can occur both inside an @ label and independently later in the
// utterance. Only a wholly disjoint occurrence supplies natural-name evidence.
func recipientQuoteOutsideMentions(text, quote string) bool {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return false
	}
	spans := formalMentionSpans(text)
	for offset := 0; offset <= len(text)-len(quote); {
		index := strings.Index(text[offset:], quote)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(quote)
		independent := true
		for _, span := range spans {
			if start < span.end && end > span.start {
				independent = false
				break
			}
		}
		if independent {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		offset = start + size
	}
	return false
}
