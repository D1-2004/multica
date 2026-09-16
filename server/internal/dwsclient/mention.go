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
