package wsfs

import "testing"

func TestLikeChildPatternEscapesWildcards(t *testing.T) {
	if got := likeChildPattern("notes"); got != `notes/%` {
		t.Fatalf("plain = %q", got)
	}
	if got := likeChildPattern("a_b"); got != `a\_b/%` {
		t.Fatalf("underscore = %q", got)
	}
	if got := likeChildPattern("100%"); got != `100\%/%` {
		t.Fatalf("percent = %q", got)
	}
	if got := likeChildPattern(`a\b`); got != `a\\b/%` {
		t.Fatalf("slash = %q", got)
	}
}
