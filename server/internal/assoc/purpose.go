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

// ValidateCoordinatorPurpose requires the bind card to name the task
// elements: delegator (委托人), event (事件), and goal (目的). Place is optional.
func ValidateCoordinatorPurpose(purpose string) error {
	if err := ValidatePurpose(purpose); err != nil {
		return err
	}
	if strings.Contains(purpose, "在钉钉会话中的消息") {
		return fmt.Errorf("purpose must not paste the inbound envelope")
	}
	if !strings.Contains(purpose, "委托") {
		return fmt.Errorf("purpose must name the delegator with 委托")
	}
	lower := strings.ToLower(purpose)
	if strings.Contains(lower, "dws") || strings.Contains(lower, "data-auth") || strings.Contains(purpose, "openConversationId") {
		return fmt.Errorf("purpose must not include tooling or auth")
	}
	return nil
}

// NormalizeConversationID strips quotes/space so stored cid values compare.
func NormalizeConversationID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return strings.TrimSpace(s)
}

// ComposeCoordinatorPurpose builds the stored purpose from LLM bind fields.
// Template: {委托人}委托：{事件与目的}. Place is appended only when known.
func ComposeCoordinatorPurpose(delegator, place, purpose string) (string, error) {
	purpose = strings.TrimSpace(purpose)
	delegator = strings.TrimSpace(delegator)
	place = strings.TrimSpace(place)
	composed := purpose
	if !strings.Contains(purpose, "委托") {
		if delegator == "" {
			return "", fmt.Errorf("delegator is required")
		}
		composed = delegator + "委托：" + purpose
		if suffix := formatPlace(place); suffix != "" {
			composed += "（" + suffix + "）"
		}
	}
	if err := ValidateCoordinatorPurpose(composed); err != nil {
		return "", err
	}
	return composed, nil
}

func formatPlace(place string) string {
	place = strings.TrimSpace(place)
	if place == "" || place == "地点未说明" {
		return ""
	}
	if strings.Contains(place, "地点") {
		return place
	}
	return "地点：" + place
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
