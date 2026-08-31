package assoc

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ParseSince(raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, fmt.Errorf("%w: since is required", ErrInvalidQuery)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return now.Add(-d), nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || days < 0 {
			return time.Time{}, fmt.Errorf("%w: invalid since %q", ErrInvalidQuery, raw)
		}
		return now.Add(-time.Duration(days) * 24 * time.Hour), nil
	}
	return time.Time{}, fmt.Errorf("%w: invalid since %q (use RFC3339 or 24h/48h/7d)", ErrInvalidQuery, raw)
}
