package inboundcoord

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var liveTokenRe = regexp.MustCompile(`(?i)(?:token=)([A-Za-z0-9][A-Za-z0-9._-]{2,})`)

// CurrentAdvancesIssue is true when this inbound continues that Issue.
// A new-ask with a different live token (another round's 高铁 vs this 订票)
// must open a new Issue instead of issue_comment_add.
func CurrentAdvancesIssue(message, issueTitle, issueDescription string) bool {
	current := liveTokens(message)
	if len(current) == 0 {
		return true
	}
	haystack := liveTokens(issueTitle + "\n" + issueDescription)
	if len(haystack) == 0 {
		return true
	}
	for _, got := range current {
		for _, have := range haystack {
			if tokensSameDeliverable(got, have) {
				return true
			}
		}
	}
	return false
}

func liveTokens(raw string) []string {
	matches := liveTokenRe.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, m := range matches {
		tok := strings.TrimSpace(m[1])
		if tok == "" {
			continue
		}
		if utf8.RuneCountInString(tok) < 3 {
			continue
		}
		if _, ok := seen[tok]; ok {
			continue
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}
	return out
}

func tokensSameDeliverable(current, issue string) bool {
	if current == "" || issue == "" {
		return false
	}
	if current == issue {
		return true
	}
	// W2 confirmation W2B shares the W2 prefix; W5 vs W3A does not.
	return strings.HasPrefix(current, issue) || strings.HasPrefix(issue, current)
}
