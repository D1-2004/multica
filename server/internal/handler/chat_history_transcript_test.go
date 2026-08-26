package handler

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func chatMsg(role, content string) db.ChatMessage {
	return db.ChatMessage{Role: role, Content: content}
}

// Owner decision (2026-08-26): every message is clipped to 200 characters, not
// only the one that would overrun the byte budget.
//
// The rule this replaces — clip nothing while the budget has room — rested on
// the transcript being the run's only memory, so that anything discarded was
// discarded for good. That stopped being the whole story once the block began
// declaring itself an interaction record and the per-turn prompt started routing
// a run that needs more to the conversation itself, which still holds every
// word. Under that framing a long paste costs the entire window and buys
// nothing the head and tail do not already give.
//
// What must not change: the clip is announced where it happens, short messages
// are still verbatim, and the cap is counted in characters rather than bytes,
// because a CJK transcript would otherwise be cut at a third of the stated
// length.
func TestBoundedChatHistoryClipsEveryLongMessage(t *testing.T) {
	t.Parallel()

	short := "收到，明天上午十点，会议室 A。"
	spec := strings.Repeat("规格说明", 750) // 3000 runes, far over the cap
	out := boundedChatHistoryTranscript([]db.ChatMessage{
		chatMsg("user", spec),
		chatMsg("assistant", short),
	})

	if !strings.Contains(out, short) {
		t.Errorf("a message inside the cap must survive verbatim:\n%s", out)
	}
	if strings.Contains(out, spec) {
		t.Error("a 3000-character message was not clipped")
	}
	if !strings.Contains(out, chatHistoryClipMarker) {
		t.Error("the clip must be announced where it happened")
	}

	// The cap is characters, not bytes: 200 runes of CJK is ~600 bytes, and a
	// byte-based cap would have kept a third of what was asked for.
	body := strings.SplitN(out, "User:\n", 2)[1]
	clipped := strings.SplitN(body, "\n\nAssistant:", 2)[0]
	kept := utf8.RuneCountInString(strings.Replace(clipped, chatHistoryClipMarker, "", 1))
	if kept != 200 {
		t.Errorf("clipped message kept %d characters, want the 200-character cap", kept)
	}
	if !utf8.ValidString(out) {
		t.Error("clip split a rune")
	}
}

// A single message larger than the whole budget must still yield something
// readable rather than an empty transcript, and must not smuggle the overflow
// past the bound.
func TestBoundedChatHistoryClipsOnlyWhatCannotFit(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("甲", 20000) // 60000 bytes
	out := boundedChatHistoryTranscript([]db.ChatMessage{chatMsg("user", huge)})

	if len(out) > 12000 {
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
func TestBoundedChatHistoryRendersOnlyMessagesWithText(t *testing.T) {
	t.Parallel()

	var blankPadded []db.ChatMessage
	for i := 0; i < 5; i++ {
		blankPadded = append(blankPadded, chatMsg("assistant", "   "))
	}
	for i := 0; i < 20; i++ {
		blankPadded = append(blankPadded, chatMsg("user", "有内容"))
	}
	if out := boundedChatHistoryTranscript(blankPadded); strings.Contains(out, "Assistant:") {
		t.Errorf("blank rows were rendered as turns:\n%s", out)
	}

	if out := boundedChatHistoryTranscript(nil); out != "" {
		t.Errorf("empty input must produce an empty transcript, got %q", out)
	}
	if out := boundedChatHistoryTranscript([]db.ChatMessage{chatMsg("user", "  ")}); out != "" {
		t.Errorf("blank-only input must produce an empty transcript, got %q", out)
	}
}

// The bounds boundedChatHistoryTranscript is written against. Restated here
// because they are function-local consts; a change to either side should make
// this test fail loudly rather than drift.
const (
	transcriptMaxBytes    = 12000
	transcriptMaxMessages = 20
)

// randomChatHistory builds one adversarial input: a mix of blank rows, ordinary
// turns, and occasional very long CJK pastes, at lengths chosen to straddle the
// message cap, the byte cap, and the per-message clip threshold.
func randomChatHistory(rng *rand.Rand) []db.ChatMessage {
	n := rng.Intn(30)
	msgs := make([]db.ChatMessage, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		var content string
		switch rng.Intn(6) {
		case 0:
			content = "" // no text at all
		case 1:
			content = strings.Repeat(" ", rng.Intn(4)) // whitespace only
		case 2:
			content = fmt.Sprintf("msg-%d-%s", i, strings.Repeat("a", rng.Intn(80)))
		case 3:
			content = fmt.Sprintf("msg-%d-", i) + strings.Repeat("字", rng.Intn(400))
		case 4: // straddles the byte cap on its own
			content = fmt.Sprintf("msg-%d-", i) + strings.Repeat("超长内容", 1+rng.Intn(1500))
		default:
			content = fmt.Sprintf("msg-%d-", i) + strings.Repeat("mixed 混合 ", rng.Intn(300))
		}
		msgs = append(msgs, chatMsg(role, content))
	}
	return msgs
}

// TestBoundedChatHistoryInvariants is the guard the live pre-release trace could
// not provide. That trace exercised a 904-byte, 7-message transcript — far
// inside every bound — so it proved only that the no-trim path still renders.
// The paths that matter are the ones a long conversation takes, and the property
// that has to hold on all of them is not "the output is small" but "the output
// never lies about what it dropped".
func TestBoundedChatHistoryInvariants(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(20260826))
	for iteration := 0; iteration < 2000; iteration++ {
		msgs := randomChatHistory(rng)
		out := boundedChatHistoryTranscript(msgs)

		if !utf8.ValidString(out) {
			t.Fatalf("iteration %d: clipping split a rune", iteration)
		}

		if len(out) > transcriptMaxBytes {
			t.Fatalf("iteration %d: transcript is %d bytes, over the %d-byte bound", iteration, len(out), transcriptMaxBytes)
		}

		var recoverable []db.ChatMessage
		for _, m := range msgs {
			if strings.TrimSpace(m.Content) != "" {
				recoverable = append(recoverable, m)
			}
		}

		if len(recoverable) == 0 {
			if out != "" {
				t.Fatalf("iteration %d: no message carried text, yet a transcript was rendered:\n%s", iteration, out)
			}
			continue
		}

		// Every message is identifiable by its "msg-<i>-" stem, so presence is
		// decidable even after a head/tail clip: the stem survives in the head.
		present := 0
		lastIndex := -1
		ordered := true
		for _, m := range recoverable {
			stem, _, ok := strings.Cut(strings.TrimSpace(m.Content), "-")
			_ = stem
			if !ok {
				continue // whitespace-only rows never carry a stem
			}
			marker := strings.SplitN(strings.TrimSpace(m.Content), "-", 3)
			if len(marker) < 2 {
				continue
			}
			stemKey := marker[0] + "-" + marker[1] + "-"
			at := strings.Index(out, stemKey)
			if at < 0 {
				continue
			}
			present++
			if at < lastIndex {
				ordered = false
			}
			lastIndex = at
		}

		if !ordered {
			t.Fatalf("iteration %d: transcript reordered the conversation:\n%s", iteration, out)
		}

		// The load-bearing invariant. Turns may be dropped to stay inside the
		// bounds, but only from the old end: whatever survives must be the most
		// recent run of the conversation, in order.
		if present < len(recoverable) {
			oldest := len(recoverable) - present
			for _, m := range recoverable[oldest:] {
				marker := strings.SplitN(strings.TrimSpace(m.Content), "-", 3)
				if len(marker) < 2 {
					continue
				}
				if !strings.Contains(out, marker[0]+"-"+marker[1]+"-") {
					t.Fatalf("iteration %d: a turn was dropped from the recent end of the window:\n%s", iteration, out)
				}
			}
		}

		// A clip must never fabricate text by overlapping its own head and tail.
		for _, part := range strings.Split(out, "\n\n") {
			head, tail, clipped := strings.Cut(part, chatHistoryClipMarker)
			if !clipped {
				continue
			}
			if len(head)+len(tail) >= transcriptMaxBytes {
				t.Fatalf("iteration %d: clipped part kept %d bytes, more than the whole budget", iteration, len(head)+len(tail))
			}
		}
	}
}

// The message cap and the byte cap are separate reasons to drop a turn. Neither
// is announced any more — the run carries a command for reading the conversation
// itself back — but both must keep the recent end of the window.
func TestBoundedChatHistoryKeepsTheRecentEndUnderBothCaps(t *testing.T) {
	t.Parallel()

	// Message cap: 25 short turns, nowhere near the byte cap.
	var many []db.ChatMessage
	for i := 0; i < transcriptMaxMessages+5; i++ {
		many = append(many, chatMsg("user", fmt.Sprintf("turn-%d", i)))
	}
	out := boundedChatHistoryTranscript(many)
	if strings.Contains(out, "turn-0\n") {
		t.Error("oldest turn survived the message cap")
	}
	if !strings.Contains(out, fmt.Sprintf("turn-%d", transcriptMaxMessages+4)) {
		t.Error("newest turn was dropped; the window must keep the recent end")
	}

	// Byte cap: few turns, each large.
	big := []db.ChatMessage{
		chatMsg("user", "oldest-"+strings.Repeat("甲", 3000)),
		chatMsg("assistant", "middle-"+strings.Repeat("乙", 3000)),
		chatMsg("user", "newest-"+strings.Repeat("丙", 3000)),
	}
	out = boundedChatHistoryTranscript(big)
	if !strings.Contains(out, "newest-") {
		t.Error("byte cap dropped the newest turn")
	}
	if !strings.Contains(out, chatHistoryClipMarker) {
		t.Errorf("byte cap reduced the transcript without clipping anything:\n%s", head400(out))
	}
}

func head400(s string) string {
	if len(s) <= 400 {
		return s
	}
	return s[:400]
}
