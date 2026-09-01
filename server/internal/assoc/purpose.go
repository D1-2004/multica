package assoc

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var vaguePurposes = map[string]struct{}{
	"帮我约一下":  {},
	"帮我看看":   {},
	"看一下":    {},
	"处理一下":   {},
	"帮我处理":   {},
	"帮我看看这个": {},
}

func ValidatePurpose(purpose string) error {
	trimmed := strings.TrimSpace(purpose)
	if utf8.RuneCountInString(trimmed) < MinPurposeRunes {
		return fmt.Errorf("purpose must be at least %d characters and name the deliverable", MinPurposeRunes)
	}
	compact := stripSpace(trimmed)
	if _, ok := vaguePurposes[compact]; ok {
		return fmt.Errorf("purpose is too vague; write the subject, deliverable, and scope")
	}
	if _, ok := vaguePurposes[trimmed]; ok {
		return fmt.Errorf("purpose is too vague; write the subject, deliverable, and scope")
	}
	return nil
}

// ResolvePurpose picks the first candidate that satisfies ValidatePurpose.
// Short coordinator look_into titles fall through to the original user message.
func ResolvePurpose(candidates ...string) (string, error) {
	seen := map[string]struct{}{}
	var nonempty []string
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		nonempty = append(nonempty, trimmed)
		if err := ValidatePurpose(trimmed); err == nil {
			return trimmed, nil
		}
	}
	if len(nonempty) > 1 {
		joined := strings.Join(nonempty, " ")
		if err := ValidatePurpose(joined); err == nil {
			return joined, nil
		}
	}
	if len(nonempty) > 0 {
		padded := "跟进：" + nonempty[0]
		if err := ValidatePurpose(padded); err == nil {
			return padded, nil
		}
	}
	return "", fmt.Errorf("%w: issue title is not a precise purpose", ErrInvalidTask)
}

// ClipBody bounds stored/recalled event text for LLM rerank.
func ClipBody(s string, n int) string {
	trimmed := strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(trimmed) <= n {
		return trimmed
	}
	return string([]rune(trimmed)[:n])
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
