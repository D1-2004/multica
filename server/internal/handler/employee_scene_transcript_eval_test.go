package handler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

// The group transcript has no Diamond gate (user decision 2026-10-03): it is on
// for every employee-mode group wake, so the eval binding is unconditional.
func init() {
	memoryEvalEnable[memoryEvalGroupTranscript] = func(*testing.T, memoryEvalTarget) bool { return true }
	memoryEvalInstallTranscript = installEvalTranscript
}

// evalTranscriptLoader is a fake DWS range reader keyed by conversation id.
type evalTranscriptLoader struct {
	mu    sync.Mutex
	convs map[string]*evalTranscriptConv
}

type evalTranscriptConv struct {
	lines []memoryEvalGroupLine
	delay time.Duration
	calls int
}

func (l *evalTranscriptLoader) LoadRange(ctx context.Context, req inboundcoord.RangeRequest) (inboundcoord.RangePage, error) {
	l.mu.Lock()
	conv := l.convs[req.ConversationID]
	if conv != nil {
		conv.calls++
	}
	l.mu.Unlock()
	if conv == nil {
		return inboundcoord.RangePage{}, errors.New("conversation has no fake provider history")
	}
	if conv.delay > 0 {
		select {
		case <-time.After(conv.delay):
		case <-ctx.Done():
			return inboundcoord.RangePage{}, ctx.Err()
		}
	}
	page := inboundcoord.RangePage{RawCount: len(conv.lines)}
	for _, line := range conv.lines {
		if !line.SentAt.Before(req.Before) {
			continue
		}
		sendType := line.SenderType
		if sendType == "" {
			sendType = "user"
		}
		message := inboundcoord.RangeMessage{ID: line.MessageID, SentAt: line.SentAt.UTC(), Sender: line.SenderName, SenderUID: line.SenderUID, SendType: sendType, Content: line.Text}
		if line.QuotedMessageID != "" {
			message.Quoted = &inboundcoord.RangeQuote{ID: line.QuotedMessageID}
		}
		page.Messages = append(page.Messages, message)
	}
	return page, nil
}

func installEvalTranscript(t *testing.T, target memoryEvalTarget, conversationID string, lines []memoryEvalGroupLine, opts memoryEvalTranscriptOptions) func() int {
	t.Helper()
	loader, ok := target.Worker.SceneTranscript.(*evalTranscriptLoader)
	if !ok {
		loader = &evalTranscriptLoader{convs: map[string]*evalTranscriptConv{}}
		target.Worker.SceneTranscript = loader
	}
	conv := &evalTranscriptConv{lines: append([]memoryEvalGroupLine(nil), lines...), delay: opts.Delay}
	loader.mu.Lock()
	loader.convs[conversationID] = conv
	loader.mu.Unlock()
	return func() int {
		loader.mu.Lock()
		defer loader.mu.Unlock()
		return conv.calls
	}
}
