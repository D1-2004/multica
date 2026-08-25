package handler

import (
	"strings"
	"testing"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func chatMsg(role, content string) db.ChatMessage {
	return db.ChatMessage{Role: role, Content: content}
}

// The transcript is the run's authoritative memory on a cloud chat
// (makeChatHistoryAuthoritative drops the provider session in its favour), so
// the rule this pins is not "bound the size" but "do not spend fidelity you did
// not have to". A transcript well inside the budget must arrive verbatim.
func TestBoundedChatHistoryKeepsEverythingThatFits(t *testing.T) {
	t.Parallel()

	spec := strings.Repeat("规格说明", 750) // 3000 runes, ~9000 bytes, still under 12000
	out := boundedChatHistoryTranscript([]db.ChatMessage{
		chatMsg("user", spec),
		chatMsg("assistant", "收到"),
	})

	if !strings.Contains(out, spec) {
		t.Fatalf("a message that fits the budget was clipped; transcript = %d bytes", len(out))
	}
	if strings.Contains(out, chatHistoryOmittedMarker) {
		t.Errorf("nothing was dropped, so the transcript must not claim a loss:\n%s", out)
	}
	if strings.Contains(out, chatHistoryClipMarker) {
		t.Error("no clip marker should appear when the whole transcript fits")
	}
}

// A single message larger than the whole budget must still yield something
// readable rather than an empty transcript, and must not smuggle the overflow
// past the bound.
func TestBoundedChatHistoryClipsOnlyWhatCannotFit(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("甲", 20000) // 60000 bytes
	out := boundedChatHistoryTranscript([]db.ChatMessage{chatMsg("user", huge)})

	if len(out) > 12000+len(chatHistoryOmittedMarker)+2 {
		t.Fatalf("clipped transcript overshoots the byte bound: %d bytes", len(out))
	}
	if !strings.Contains(out, chatHistoryClipMarker) {
		t.Error("an over-long message must announce its own truncation")
	}
	if !utf8.ValidString(out) {
		t.Error("clip cut a rune in half")
	}
	// Head AND tail: the closing of a long message usually carries the ask.
	body := strings.TrimPrefix(out, "User:\n")
	head, tail, ok := strings.Cut(body, chatHistoryClipMarker)
	if !ok || head == "" || tail == "" {
		t.Errorf("clip must keep both ends, got head=%d tail=%d", len(head), len(tail))
	}
}

// clipChatHistoryMessage is called with a byte budget the caller has already
// committed to. Returning something longer than that budget — or longer than
// the input it was asked to shorten — breaks the arithmetic at the call site.
func TestClipChatHistoryMessageNeverGrows(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		"",
		"短",
		strings.Repeat("a", 401),
		strings.Repeat("字", 200),
		strings.Repeat("mixed 混合 ", 300),
	} {
		for _, budget := range []int{0, 1, 17, 400, 401, 1000, 100000} {
			got := clipChatHistoryMessage(content, budget)
			if len(got) > len(content) {
				t.Errorf("budget=%d grew a %d-byte message to %d bytes", budget, len(content), len(got))
			}
			if len(got) > budget && len(content) > budget {
				t.Errorf("budget=%d exceeded: got %d bytes", budget, len(got))
			}
			if !utf8.ValidString(got) {
				t.Errorf("budget=%d produced invalid UTF-8", budget)
			}
		}
	}
}

// The omitted marker is a claim, and the per-turn chat prompt tells the run to
// treat a declared gap as context it must go and fetch. Raising it for rows that
// carried no text sends the run after a conversation it already has in full.
func TestBoundedChatHistoryMarkerTracksRealLoss(t *testing.T) {
	t.Parallel()

	var blankPadded []db.ChatMessage
	for i := 0; i < 5; i++ {
		blankPadded = append(blankPadded, chatMsg("assistant", "   "))
	}
	for i := 0; i < 20; i++ {
		blankPadded = append(blankPadded, chatMsg("user", "有内容"))
	}
	if out := boundedChatHistoryTranscript(blankPadded); strings.Contains(out, chatHistoryOmittedMarker) {
		t.Errorf("only blank rows fell outside the window; nothing was lost:\n%s", out)
	}

	var overflowing []db.ChatMessage
	for i := 0; i < 25; i++ {
		overflowing = append(overflowing, chatMsg("user", "第 N 条真实内容"))
	}
	if out := boundedChatHistoryTranscript(overflowing); !strings.Contains(out, chatHistoryOmittedMarker) {
		t.Errorf("five messages with text were dropped and the transcript did not say so:\n%s", out)
	}

	if out := boundedChatHistoryTranscript(nil); out != "" {
		t.Errorf("empty input must produce an empty transcript, got %q", out)
	}
	if out := boundedChatHistoryTranscript([]db.ChatMessage{chatMsg("user", "  ")}); out != "" {
		t.Errorf("blank-only input must produce an empty transcript, got %q", out)
	}
}
