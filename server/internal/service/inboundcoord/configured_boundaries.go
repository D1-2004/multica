package inboundcoord

import "strings"

// Use the same bounded configuration in routing, review and quote validation.
// Presence proves provenance only; the independent review still determines
// applicability and cannot expand job policy or the current user's authority.
func configuredPersona(turn Turn) string {
	return clipRunes(strings.TrimSpace(turn.Persona), personaBudget)
}

func configuredReplyTone(turn Turn) string {
	return clipRunes(strings.TrimSpace(turn.ReplyTone), toneBudget)
}

func suppliedConstraintQuote(quote string, turn Turn) bool {
	if strings.TrimSpace(quote) == "" {
		return false
	}
	for _, source := range []string{coordinationConstraintText(turn), configuredPersona(turn), configuredReplyTone(turn)} {
		if strings.Contains(source, quote) {
			return true
		}
	}
	for _, utterance := range windowUtterances(turn) {
		if strings.Contains(utterance.Text, quote) {
			return true
		}
	}
	return false
}
