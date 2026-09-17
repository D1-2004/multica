package dwsclient

import "strings"

// MentionToken is the DingTalk placeholder that renders as an @ of that member.
func MentionToken(openDingTalkID string) string {
	openDingTalkID = strings.TrimSpace(openDingTalkID)
	if openDingTalkID == "" {
		return ""
	}
	return "<@" + openDingTalkID + ">"
}

// StripLeadingAddressing removes both forms that open a message by addressing
// one person: the `<@openDingTalkId>` placeholder a plain send needs, and the
// `@display name` an executor types by hand. Only that person's own opening is
// removed, so an address to somebody else and a deliberate mention inside the
// sentence both stay.
func StripLeadingAddressing(content, openDingTalkID, displayName string) string {
	for {
		next := stripLeadingDisplayMention(StripLeadingMention(content, openDingTalkID), displayName)
		if next == content {
			return content
		}
		content = next
	}
}

func stripLeadingDisplayMention(content, displayName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return content
	}
	rest := strings.TrimLeft(content, " \t")
	if !strings.HasPrefix(rest, "@"+displayName) {
		return content
	}
	tail := strings.TrimPrefix(rest, "@"+displayName)
	// Require a boundary so `@冬翔翔` is not mistaken for `@冬翔`.
	if tail != "" && !strings.ContainsRune(" \t\r\n 　，,：:", []rune(tail)[0]) {
		return content
	}
	return strings.TrimLeft(tail, " \t 　")
}

// StripLeadingMention removes leading placeholders addressing openDingTalkID.
// DingTalk already prefixes a quote reply with an @ of the quoted sender, so a
// placeholder carried over from a plain send renders that person twice. Only
// the addressing prefix is removed; a deliberate mention inside the sentence
// stays.
func StripLeadingMention(content, openDingTalkID string) string {
	mention := MentionToken(openDingTalkID)
	if mention == "" {
		return content
	}
	rest := strings.TrimLeft(content, " \t")
	stripped := false
	for strings.HasPrefix(rest, mention) {
		rest = strings.TrimLeft(strings.TrimPrefix(rest, mention), " \t")
		stripped = true
	}
	if !stripped {
		return content
	}
	return rest
}
