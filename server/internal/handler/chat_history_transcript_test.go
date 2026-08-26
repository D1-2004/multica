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

		// The omission marker is allowed to push the result past the byte cap:
		// it is a statement about the transcript, not part of it. Nothing else is.
		if limit := transcriptMaxBytes + len(chatHistoryOmittedMarker) + 2; len(out) > limit {
			t.Fatalf("iteration %d: transcript is %d bytes, over the %d-byte bound", iteration, len(out), limit)
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

		announced := strings.HasPrefix(out, chatHistoryOmittedMarker)

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

		// The load-bearing invariant. Anything missing must be announced, and
		// nothing may be announced that is not missing.
		missing := present < len(recoverable)
		if missing && !announced {
			t.Fatalf("iteration %d: %d of %d messages with text are absent and the transcript does not say so:\n%s",
				iteration, len(recoverable)-present, len(recoverable), out)
		}
		if !missing && announced {
			t.Fatalf("iteration %d: every message with text is present, yet the transcript claims a loss:\n%s",
				iteration, out)
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

// The message cap and the byte cap are separate reasons to drop a turn, and both
// have to reach the same conclusion about announcing it.
func TestBoundedChatHistoryAnnouncesBothKindsOfLoss(t *testing.T) {
	t.Parallel()

	// Message cap: 25 short turns, nowhere near the byte cap.
	var many []db.ChatMessage
	for i := 0; i < transcriptMaxMessages+5; i++ {
		many = append(many, chatMsg("user", fmt.Sprintf("turn-%d", i)))
	}
	out := boundedChatHistoryTranscript(many)
	if !strings.HasPrefix(out, chatHistoryOmittedMarker) {
		t.Errorf("message cap dropped 5 turns without announcing it:\n%s", out)
	}
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
	if !strings.HasPrefix(out, chatHistoryOmittedMarker) && !strings.Contains(out, chatHistoryClipMarker) {
		t.Errorf("byte cap reduced the transcript without any marker:\n%s", head400(out))
	}
}

func head400(s string) string {
	if len(s) <= 400 {
		return s
	}
	return s[:400]
}
