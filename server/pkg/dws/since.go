package dws

import (
	"strconv"
	"strings"
	"time"
)

// ParseSince reads a history bound: a duration back from now ("30m", "2h",
// "7d"), a Beijing-time date or time ("2026-09-29", "2026-09-29 10:00",
// "2026-09-29 10:00:00"), or RFC 3339 with or without seconds
// ("2026-09-29T10:00+08:00"). Empty means no bound. The Python client
// accepts the same forms.
func ParseSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if n := len(s); n > 1 {
		if v, err := strconv.Atoi(s[:n-1]); err == nil && v >= 0 {
			switch s[n-1] {
			case 'm':
				return now.Add(-time.Duration(v) * time.Minute), nil
			case 'h':
				return now.Add(-time.Duration(v) * time.Hour), nil
			case 'd':
				return now.AddDate(0, 0, -v), nil
			}
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, shanghai); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, invalid("since must look like 2h, 7d, 2026-09-29 10:00 or RFC 3339")
}
