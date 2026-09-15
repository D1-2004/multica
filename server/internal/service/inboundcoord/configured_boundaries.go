package inboundcoord

import (
	"strings"
	"unicode/utf8"
)

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
	if quoteIsCurrentWorkUtterance(quote, turn) {
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

// quoteIsCurrentWorkUtterance reports that the decline quote is the current
// work ask (or nearly all of it), not a standing restriction. Scene memory
// and history are already excluded from suppliedConstraintQuote sources.
func quoteIsCurrentWorkUtterance(quote string, turn Turn) bool {
	q := strings.TrimSpace(quote)
	if q == "" {
		return false
	}
	qr := utf8.RuneCountInString(q)
	for _, utterance := range windowUtterances(turn) {
		t := strings.TrimSpace(utterance.Text)
		if t == "" || !workRequestUtterance(t) {
			continue
		}
		if q == t {
			return true
		}
		if strings.Contains(t, q) {
			tr := utf8.RuneCountInString(t)
			if tr > 0 && qr*4 >= tr*3 {
				return true
			}
		}
	}
	return false
}
