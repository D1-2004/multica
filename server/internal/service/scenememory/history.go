package scenememory

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/redact"
)

var (
	secretKV       = regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)\s*[:=]\s*\S+`)
	secretURLParam = regexp.MustCompile(`(?i)([?&](?:token|access_token|signature|sig|secret|key)=)[^&\s]+`)
)

func redactSecrets(s string) string {
	s = secretKV.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretURLParam.ReplaceAllString(s, "${1}[REDACTED]")
	return redact.Text(s)
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
	events = sortHistoryEvents(events)
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

func includePendingWindow(delta, events []HistoryEvent, fromAt time.Time, fromEv string, cursorAt time.Time, cursorEv string) []HistoryEvent {
	// Events at or behind the source cursor were already merged. Re-adding
	// them recaps the 24/40 batch and stalls the cursor into an LLM loop.
	_ = events
	_ = fromAt
	_ = fromEv
	_ = cursorAt
	_ = cursorEv
	return delta
}

func forceIncludeEvidence(delta, events []HistoryEvent, evidence string) []HistoryEvent {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" || containsEvidence(delta, evidence) || !containsEvidence(events, evidence) {
		return delta
	}
	for _, event := range events {
		if event.EvidenceID == evidence {
			return sortHistoryEvents(append([]HistoryEvent{event}, delta...))
		}
	}
	return delta
}

func sortHistoryEvents(events []HistoryEvent) []HistoryEvent {
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].OccurredAt.Before(events[j].OccurredAt)
		}
		return events[i].EvidenceID < events[j].EvidenceID
	})
	return events
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
