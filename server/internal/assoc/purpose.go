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

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
