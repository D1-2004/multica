package inboundcoord

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
)

// A Coordinator job waits out its collect window (4 s after the latest
// message) before any replica claims it, and only then reads the DingTalk
// history the first model request needs (P50 ~0.75 s in production). The
// replica that accepted the message can run that same read during the
// window instead. The claimed decision uses the result only when every input
// of the read is identical and the read is recent; any other case reads at
// claim time as before, within the same claim-time budget.
//
// Equal inputs do not by themselves prove an equal DWS answer: a message
// before the cutoff may become visible to the list only after the early
// read. Reuse is therefore limited to reads started within
// windowHistoryReuseAge of the claim (a parked job reads again), and every
// reuse is shadow-compared with a claim-time read off the critical path.
const (
	// windowHistoryReuseAge covers the normal claim of the read's own window
	// (4 s after the message that started it) and excludes parked jobs.
	windowHistoryReuseAge = 8 * time.Second
	// windowHistoryTTL drops reads that were never claimed on this replica.
	windowHistoryTTL = 90 * time.Second
	// windowHistoryMaxEntries bounds the cache; a full cache starts no read.
	windowHistoryMaxEntries = 1024
	// windowHistoryMaxActive bounds concurrent early and shadow reads per
	// replica, the same order as the claiming workers; a busy replica reads
	// at claim time instead.
	windowHistoryMaxActive = 8
	// ownerSwitchTimeout bounds the Coordinator switch lookup at enqueue.
	ownerSwitchTimeout = time.Second
)

// windowHistoryReads holds history reads started while a job was collecting.
type windowHistoryReads struct {
	mu      sync.Mutex
	entries map[string]*windowHistoryRead
	active  chan struct{}
}

type windowHistoryRead struct {
	started time.Time
	done    chan struct{}
	result  historyPrefetchResult
}

func newWindowHistoryReads() *windowHistoryReads {
	return &windowHistoryReads{entries: make(map[string]*windowHistoryRead), active: make(chan struct{}, windowHistoryMaxActive)}
}

func (r *windowHistoryReads) acquire() bool {
	select {
	case r.active <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *windowHistoryReads) release() { <-r.active }

// windowHistoryKey covers every Turn field the DWS history read and its
// parsing depend on: the reader identity, the conversation, the cutoff and
// the window messages excluded from history.
func windowHistoryKey(turn Turn) string {
	ids := make([]string, 0, len(turn.Utterances)+1)
	if turn.EvidenceID != "" {
		ids = append(ids, turn.EvidenceID)
	}
	for _, u := range turn.Utterances {
		if u.EvidenceID != "" {
			ids = append(ids, u.EvidenceID)
		}
	}
	sort.Strings(ids)
	return strings.Join([]string{
		util.UUIDToString(turn.AgentID), strings.TrimSpace(turn.ConversationID),
		strings.TrimSpace(turn.DWSUID), strings.TrimSpace(turn.DWSOrgID),
		strconv.FormatInt(turn.HistoryBefore.UnixNano(), 10), strings.Join(ids, ","),
	}, "\x00")
}

// historyPrefetchAllowed reports whether the collect-window read applies to
// agent. It is sampled once per decision like the other rollout switches.
func (c *Coordinator) historyPrefetchAllowed(turn Turn) bool {
	return c != nil && c.HistoryPrefetchAgentProvider != nil && c.HistoryPrefetchAgentProvider(turn.AgentID)
}

// PrefetchWindowHistory starts the DingTalk history read of a job that is
// still collecting its window. turn must carry the inputs the claimed
// decision will use; a later message changes the cutoff and starts a new
// read. It reports whether a read is running for turn.
func (c *Coordinator) PrefetchWindowHistory(turn Turn) bool {
	if c == nil || c.windowHistory == nil || !c.historyPrefetchAllowed(turn) {
		return false
	}
	if len(turn.Utterances) == 0 || turn.HistoryBefore.IsZero() || !shouldPrefetchHistory(c, turn) {
		return false
	}
	// The claimed decision skips its whole loop, history included, when the
	// owner switched the Coordinator off; an early read would be wasted.
	switchCtx, cancelSwitch := context.WithTimeout(context.Background(), ownerSwitchTimeout)
	off := c.coordinatorOff(switchCtx, turn)
	cancelSwitch()
	if off {
		return false
	}
	key := windowHistoryKey(turn)
	reads := c.windowHistory
	now := time.Now()
	reads.mu.Lock()
	for k, read := range reads.entries {
		if now.Sub(read.started) > windowHistoryTTL {
			delete(reads.entries, k)
		}
	}
	if _, running := reads.entries[key]; running {
		reads.mu.Unlock()
		return true
	}
	if len(reads.entries) >= windowHistoryMaxEntries || !reads.acquire() {
		reads.mu.Unlock()
		return false
	}
	read := &windowHistoryRead{started: now, done: make(chan struct{})}
	reads.entries[key] = read
	reads.mu.Unlock()

	loader := c.DWSHistory
	go func() {
		defer close(read.done)
		defer reads.release()
		defer func() {
			if r := recover(); r != nil {
				read.result = historyPrefetchResult{err: errors.New("window history read panicked")}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), historyPrefetchTimeout)
		defer cancel()
		history, err := loader.Load(ctx, turn)
		timedOut := err != nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
		read.result = historyPrefetchResult{history: history, err: err, timedOut: timedOut, elapsed: time.Since(read.started)}
	}()
	slog.Info("inbound coordinator window history read started", append(coordinatorLogIndex(turn),
		"event", "inbound_coordinator_window_history_started",
		"window_messages", len(turn.Utterances))...)
	return true
}

// takeWindowHistory removes and returns the collect-window read for turn if
// one exists and started recently enough to stand for a claim-time read. The
// claimed decision consumes it once.
func (c *Coordinator) takeWindowHistory(turn Turn) *windowHistoryRead {
	if c == nil || c.windowHistory == nil {
		return nil
	}
	key := windowHistoryKey(turn)
	reads := c.windowHistory
	reads.mu.Lock()
	defer reads.mu.Unlock()
	read, ok := reads.entries[key]
	if !ok {
		return nil
	}
	delete(reads.entries, key)
	if time.Since(read.started) > windowHistoryReuseAge {
		return nil
	}
	return read
}

// shadowWindowHistory reads the history again at claim time, off the
// critical path, and logs whether it equals the reused early read. It is
// the evidence that early and claim-time reads agree; a busy replica skips
// it rather than queue extra DWS reads.
func (c *Coordinator) shadowWindowHistory(turn Turn, early *windowHistoryRead) {
	if c == nil || c.windowHistory == nil || c.DWSHistory == nil {
		return
	}
	logArgs := append(coordinatorLogIndex(turn), "event", "inbound_coordinator_window_history_shadow",
		"early_age_ms", time.Since(early.started).Milliseconds(), "early_count", len(early.result.history))
	if !c.windowHistory.acquire() {
		slog.Info("inbound coordinator window history shadow", append(logArgs, "outcome", "skipped")...)
		return
	}
	loader := c.DWSHistory
	go func() {
		defer c.windowHistory.release()
		ctx, cancel := context.WithTimeout(context.Background(), historyPrefetchTimeout)
		defer cancel()
		fresh, err := loader.Load(ctx, turn)
		outcome := "unavailable"
		if err == nil {
			outcome = compareHistory(early.result.history, fresh)
		}
		slog.Info("inbound coordinator window history shadow", append(logArgs, "outcome", outcome, "claim_count", len(fresh))...)
	}()
}

// compareHistory reports whether two reads hold the same messages, by
// evidence id and content, in the same order.
func compareHistory(a, b []HistoryLine) string {
	if len(a) != len(b) {
		return "mismatch"
	}
	for i := range a {
		if a[i].EvidenceID != b[i].EvidenceID || a[i].Content != b[i].Content || a[i].Role != b[i].Role {
			return "mismatch"
		}
	}
	return "match"
}
