package scenememory

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	secretBearer   = regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._\-+=/]{8,}`)
	secretPassword = regexp.MustCompile(`(?i)(password\s*[:=]\s*)\S+`)
	secretURLParam = regexp.MustCompile(`(?i)([?&](?:token|access_token|signature|sig|secret|key)=)[^&\s]+`)
)

func redactSecrets(s string) string {
	s = secretBearer.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretPassword.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretURLParam.ReplaceAllString(s, "${1}[REDACTED]")
	return s
}

type HistoryEvent struct {
	EvidenceID string
	OccurredAt time.Time
	Speaker    string
	Content    string
	Self       bool
}

func filterUntil(events []HistoryEvent, cutoffAt time.Time, cutoffEvidence string) []HistoryEvent {
	if cutoffAt.IsZero() {
		return events
	}
	out := make([]HistoryEvent, 0, len(events))
	for _, event := range events {
		if event.OccurredAt.After(cutoffAt) {
			continue
		}
		if event.OccurredAt.Equal(cutoffAt) && event.EvidenceID > cutoffEvidence {
			continue
		}
		out = append(out, event)
	}
	return out
}

func afterCursor(events []HistoryEvent, cursorAt time.Time, cursorEvidence string) []HistoryEvent {
	sort.Slice(events, func(i, j int) bool {
		if !events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].OccurredAt.Before(events[j].OccurredAt)
		}
		return events[i].EvidenceID < events[j].EvidenceID
	})
	out := make([]HistoryEvent, 0, len(events))
	for _, event := range events {
		if cursorAt.IsZero() {
			out = append(out, event)
			continue
		}
		if event.OccurredAt.After(cursorAt) || (event.OccurredAt.Equal(cursorAt) && event.EvidenceID > cursorEvidence) {
			out = append(out, event)
		}
	}
	return out
}

func containsEvidence(events []HistoryEvent, evidence string) bool {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return false
	}
	for _, event := range events {
		if event.EvidenceID == evidence {
			return true
		}
	}
	return false
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}
