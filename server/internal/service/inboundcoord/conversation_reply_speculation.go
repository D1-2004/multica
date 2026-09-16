package inboundcoord

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
)

// The conversation render depends on the routed proposal only through the set
// of conversation acknowledgements it selects, and that set has one dominant
// shape: a single acknowledgement answering every source this Host owes an
// answer. Asking it before routing returns removes a whole model call from the
// critical path without weakening anything, because the answer is used only
// when the routed request hashes identically. A different routing outcome
// discards the speculation; it never edits a plan, reads work state, or
// reaches an effect path.
type conversationReplySpeculation struct {
	inputHash string
	done      <-chan conversationReplySpeculationResult
}

type conversationReplySpeculationResult struct {
	replies map[string]string
	err     error
}

// speculativeConversationDecision is the one hypothesis worth pre-rendering.
// It is a guess about the shape of the proposal, never a decision: Host still
// routes, reviews and persists the real plan.
func speculativeConversationDecision(turn Turn) Decision {
	var refs []string
	for i, utterance := range windowUtterances(turn) {
		if sourceResponseRequired(turn, utterance) {
			refs = append(refs, fmt.Sprintf("u%d", i+1))
		}
	}
	if len(refs) == 0 {
		return Decision{}
	}
	return Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: refs}}}
}

// startConversationReplySpeculation runs the hypothesis render concurrently
// with the first routing request. The Langfuse observation is opened here, on
// the caller's goroutine, and closed inside the worker: the trace's own
// metadata slice is not safe for concurrent writers, and an observation only
// ever touches its own span.
func (c *Coordinator) startConversationReplySpeculation(ctx context.Context, turn Turn) *conversationReplySpeculation {
	if turn.Loop == LoopTaskFinished {
		return nil
	}
	req, err := conversationReplyRequestFor(turn, speculativeConversationDecision(turn))
	if err != nil || req == nil {
		return nil
	}
	generation := c.startConversationReplyGeneration(ctx, req, "coordinator.conversation_reply.speculative", true)
	done := make(chan conversationReplySpeculationResult, 1)
	started := time.Now()
	go func() {
		replies, err := c.executeConversationReply(ctx, req, generation)
		done <- conversationReplySpeculationResult{replies: replies, err: err}
		slog.Info("inbound coordinator conversation reply speculated", append(coordinatorLogIndex(turn), "event", "inbound_coordinator_conversation_reply_speculation", "action_refs", req.refs, "input_hash", req.inputHash, "failed", err != nil, "elapsed_ms", time.Since(started).Milliseconds())...)
	}()
	return &conversationReplySpeculation{inputHash: req.inputHash, done: done}
}

// recordUnusedConversationReplySpeculation reports a speculation the turn never
// consumed. The cost is real whether or not the answer was used, so it is
// reported rather than dropped.
func recordUnusedConversationReplySpeculation(ctx context.Context, speculation *conversationReplySpeculation, consumed bool) {
	if speculation == nil || consumed {
		return
	}
	if lt := langfuse.TraceFromContext(ctx); lt != nil {
		lt.AddMetadata(map[string]any{"conversation_reply_speculation": "unused"})
	}
}
