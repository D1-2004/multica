package util

// DingTalkMentionMatchesUID compares trusted mention metadata with the receiving
// account. Some Router events carry the decimal UID only in openDingTalkId.
// Recognize that exact representation without treating opaque open IDs as UIDs.
// An explicit UID always takes precedence over the alternate field.
func DingTalkMentionMatchesUID(uid, openDingTalkID, receivingUID string) bool {
	if receivingUID == "" {
		return false
	}
	if uid != "" {
		return uid == receivingUID
	}
	if openDingTalkID != receivingUID {
		return false
	}
	for _, c := range receivingUID {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
