package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
)

// historyPrefetchTimeout bounds how long the first model request waits for
// the DingTalk history read. The read normally returns within two seconds;
// a slower read leaves history not_loaded so the loop can still read it on
// demand instead of stalling the reply.
const historyPrefetchTimeout = 2500 * time.Millisecond

type historyPrefetchResult struct {
	history []HistoryLine
	err     error
	// timedOut is true when the bounded prefetch context expired before the
	// read returned. The DWS client wraps transport errors without the context
	// sentinel, so the context state, not the error type, decides this.
	timedOut bool
	elapsed  time.Duration
	// window is the collect-window read outcome: "" when that read is not
	// enabled for the agent, else hit, miss (no recent matching read on this
	// replica), failed (fell back to the claim-time read) or timeout (the
	// claim-time budget ran out while the early read was in flight). waited
	// is how long the decision blocked on the early read.
	window string
	waited time.Duration
}

// shouldPrefetchHistory reports whether Host should read the bounded recent
// DingTalk history before the first model request. Short replies such as
// "6 点" or "好" only make sense against the question this employee asked a
// moment earlier; without that evidence the model asks again or misroutes.
// The same participation gate as scene recall applies: observed group chatter
// that does not address this employee must establish relevance first.
func shouldPrefetchHistory(c *Coordinator, turn Turn) bool {
	if c == nil || c.DWSHistory == nil {
		return false
	}
	if (turn.Loop != "" && turn.Loop != LoopInbound) || turn.Source == SourceWeb {
		return false
	}
	if turn.HistoryStatus != "" && turn.HistoryStatus != "not_loaded" {
		return false
	}
	if len(turn.DingTalkHistory) > 0 || len(turn.History) > 0 {
		return false
	}
	if strings.TrimSpace(turn.ConversationID) == "" || strings.TrimSpace(turn.DWSUID) == "" || strings.TrimSpace(turn.DWSOrgID) == "" || !turn.AgentID.Valid {
		return false
	}
	return !(turn.ProactiveConversation && !turn.Addressed && strings.EqualFold(turn.ChatType, "group"))
}

// startHistoryPrefetch begins the DingTalk history read concurrently with the
// scene recall prefetch. The returned channel yields exactly one result.
func (c *Coordinator) startHistoryPrefetch(ctx context.Context, turn Turn) <-chan historyPrefetchResult {
	out := make(chan historyPrefetchResult, 1)
	readCtx, cancel := context.WithTimeout(ctx, historyPrefetchTimeout)
	window := ""
	var early *windowHistoryRead
	if c.windowHistoryAllowed {
		window = "miss"
		early = c.takeWindowHistory(turn)
	}
	go func() {
		defer cancel()
		started := time.Now()
		var waited time.Duration
		if early != nil {
			// Waiting for the early read and any fallback read share the one
			// claim-time budget, so the first model request never waits
			// longer than it would for a claim-time read.
			select {
			case <-early.done:
				waited = time.Since(started)
				if early.result.err == nil {
					result := early.result
					result.window, result.waited = "hit", waited
					c.shadowWindowHistory(turn, early)
					out <- result
					return
				}
				window = "failed"
			case <-readCtx.Done():
				waited = time.Since(started)
				out <- historyPrefetchResult{err: readCtx.Err(), timedOut: true, elapsed: waited, window: "timeout", waited: waited}
				return
			}
		}
		history, err := c.DWSHistory.Load(readCtx, turn)
		timedOut := err != nil && (readCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
		out <- historyPrefetchResult{history: history, err: err, timedOut: timedOut, elapsed: time.Since(started), window: window, waited: waited}
	}()
	return out
}

// finishHistoryPrefetch joins the concurrent read and projects it as the
// history read snapshot the loop already knows. A timeout keeps history
// not_loaded so the model can still request it; any other failure is an
// explicit unavailable read, never an empty conversation.
func (c *Coordinator) finishHistoryPrefetch(ctx context.Context, turn *Turn, sequence *int, pending <-chan historyPrefetchResult) (functionCall, string, error) {
	arguments := `{"kind":"history"}`
	call := functionCall{ID: "host-history-prefetch", Name: toolContextRead, Arguments: arguments}
	lt := langfuse.TraceFromContext(ctx)
	obs := traceHistoryStart(lt, *turn)
	result := <-pending
	status := "loaded"
	timedOut := false
	if result.err != nil {
		if result.timedOut {
			timedOut = true
			status = "not_loaded"
		} else {
			status = "unavailable"
		}
	} else if len(result.history) == 0 {
		status = "empty"
	}
	traceHistoryEnd(obs, result.history, result.err)
	if lt != nil {
		metadata := map[string]any{"history_prefetch_status": status, "history_prefetch_elapsed_ms": result.elapsed.Milliseconds()}
		if result.window != "" {
			metadata["history_prefetch_window"] = result.window
			metadata["history_prefetch_wait_ms"] = result.waited.Milliseconds()
		}
		lt.AddMetadata(metadata)
	}
	logArgs := append(coordinatorLogIndex(*turn),
		"event", "inbound_coordinator_history_prefetch", "origin", "host_prefetch", "status", status,
		"elapsed_ms", result.elapsed.Milliseconds(), "timeout_ms", historyPrefetchTimeout.Milliseconds(), "message_count", len(result.history))
	if result.window != "" {
		logArgs = append(logArgs, "window", result.window, "wait_ms", result.waited.Milliseconds())
	}
	slog.Info("inbound coordinator history prefetch", logArgs...)
	if timedOut {
		turn.HistoryStatus = "not_loaded"
		return call, "", fmt.Errorf("history prefetch timed out after %s", historyPrefetchTimeout)
	}
	turn.DingTalkHistory = result.history
	turn.HistoryStatus = status
	if result.err != nil {
		turn.HistoryError = "History could not be read; do not infer an answer, consent, or absence."
	}
	data := map[string]any{"status": turn.HistoryStatus, "scope": turn.ConversationID, "before": turn.HistoryBefore.Format(time.RFC3339Nano), "messages": turn.DingTalkHistory, "complete": false, "hint": turn.HistoryError}
	encoded, err := json.Marshal(data)
	if err != nil {
		return call, "", err
	}
	remembered, err := rememberCoordinationRead(turn, sequence, call.Name, call.Arguments, string(encoded), nil)
	return call, remembered, err
}

// retainedHistorySnapshot returns the history read Host already holds for
// this run so a repeated read reuses it instead of spending another round.
func retainedHistorySnapshot(turn Turn) (string, bool) {
	for _, read := range turn.CoordinationReads {
		if read.Tool == toolContextRead && read.Kind == "" {
			return string(read.Result), true
		}
	}
	return "", false
}
