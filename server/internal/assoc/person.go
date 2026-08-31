package assoc

import (
	"strings"
	"unicode"
)

// CanonicalPersonKey picks a stable person_key: a decimal DingTalk uid when
// present, otherwise the first non-empty identifier. All other identifiers
// become aliases so recall can accept uid, staffId, or openDingTalkId.
func CanonicalPersonKey(ids ...string) (key string, aliases []string) {
	seen := map[string]struct{}{}
	var ordered []string
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	if len(ordered) == 0 {
		return "", nil
	}
	for _, id := range ordered {
		if isDecimalUID(id) {
			key = id
			break
		}
	}
	if key == "" {
		key = ordered[0]
	}
	for _, id := range ordered {
		if id != key {
			aliases = append(aliases, id)
		}
	}
	return key, aliases
}

func isDecimalUID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
