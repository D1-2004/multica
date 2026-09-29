package inboundcoord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// A decision speculated during the collect window could only stand for the
// claimed one if the claimed decision would send the same model requests.
// Before any speculation spends a model call, this shadow measures how often
// the first request already differs: the replica that accepted the message
// assembles the first model request with the same Host code the claim runs,
// without calling the model, starting a trace, logging or writing anything,
// and the claimed decision compares it with the request it actually sends.
// Only hashes, sizes and the input classes that differ are logged.
const (
	// firstRoundShadowTTL drops shadows whose job was claimed elsewhere.
	firstRoundShadowTTL = 90 * time.Second
	// firstRoundShadowMaxEntries bounds the store; a full store builds none.
	firstRoundShadowMaxEntries = 1024
	// firstRoundShadowMaxActive bounds concurrent builds per replica.
	firstRoundShadowMaxActive = 4
	// firstRoundShadowTimeout bounds one build, the early history wait included.
	firstRoundShadowTimeout = 5 * time.Second
)

type firstRoundShadows struct {
	mu      sync.Mutex
	entries map[string]*firstRoundShadow
	active  chan struct{}
}

type firstRoundShadow struct {
	started time.Time
	done    chan struct{}
	// skipped names why no request was built; empty when one was.
	skipped string
	request requestFingerprint
	inputs  map[string]string
	build   time.Duration
}

func newFirstRoundShadows() *firstRoundShadows {
	return &firstRoundShadows{entries: make(map[string]*firstRoundShadow), active: make(chan struct{}, firstRoundShadowMaxActive)}
}

func firstRoundShadowKey(turn Turn) string {
	return strings.TrimSpace(turn.TraceID) + "\x00" + windowHistoryKey(turn)
}

// firstRoundShadowApplies limits the shadow to the pilot scope: undisturbed
// inbound single chats with a message, no user decision and a job trace id.
func firstRoundShadowApplies(turn Turn) bool {
	if turn.Loop != "" && turn.Loop != LoopInbound {
		return false
	}
	if turn.Source != SourceDigitalEmployee || turn.ProactiveConversation || turn.UserDecisionEnabled {
		return false
	}
	if strings.EqualFold(turn.ChatType, "group") {
		return false
	}
	return strings.TrimSpace(turn.Message) != "" && strings.TrimSpace(turn.TraceID) != ""
}

type hostQuietKey struct{}

// hostQuiet reports whether Host reads run for the shadow and must not log.
func hostQuiet(ctx context.Context) bool {
	quiet, _ := ctx.Value(hostQuietKey{}).(bool)
	return quiet
}

// ShadowFirstRound assembles, off the critical path, the first model request
// the claimed decision of turn's job will send. turn must be built exactly as
// the claim builds it, with the job id as TraceID; the collect-window history
// read of the same inputs must already be running, because the shadow only
// looks at that read and never reads DingTalk itself.
func (c *Coordinator) ShadowFirstRound(turn Turn) {
	if c == nil || c.firstRoundShadows == nil || c.windowHistory == nil || !c.historyPrefetchAllowed(turn) || !firstRoundShadowApplies(turn) {
		return
	}
	early := c.peekWindowHistory(turn)
	if early == nil {
		return
	}
	shadows := c.firstRoundShadows
	key := firstRoundShadowKey(turn)
	now := time.Now()
	shadows.mu.Lock()
	for k, entry := range shadows.entries {
		if now.Sub(entry.started) > firstRoundShadowTTL {
			delete(shadows.entries, k)
		}
	}
	if _, exists := shadows.entries[key]; exists || len(shadows.entries) >= firstRoundShadowMaxEntries {
		shadows.mu.Unlock()
		return
	}
	select {
	case shadows.active <- struct{}{}:
	default:
		shadows.mu.Unlock()
		return
	}
	entry := &firstRoundShadow{started: now, done: make(chan struct{})}
	shadows.entries[key] = entry
	shadows.mu.Unlock()

	go func() {
		defer close(entry.done)
		defer func() { <-shadows.active }()
		defer func() {
			if r := recover(); r != nil {
				entry.skipped = "panicked"
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), hostQuietKey{}, true), firstRoundShadowTimeout)
		defer cancel()
		entry.skipped = c.buildFirstRoundShadow(ctx, turn, early, entry)
		entry.build = time.Since(entry.started)
	}()
}

// buildFirstRoundShadow mirrors Decide up to the first model request. It
// returns why it stopped early, or "" once entry holds the request.
func (c *Coordinator) buildFirstRoundShadow(ctx context.Context, turn Turn, early *windowHistoryRead, entry *firstRoundShadow) string {
	snapshot, err := c.decisionSnapshot(ctx, turn)
	if err != nil {
		return "model_configuration_unavailable"
	}
	turn.model = snapshot.model
	if snapshot.Ready != nil {
		if ready, err := snapshot.Ready(ctx); err != nil || !ready {
			return "coordinator_rollout_wait"
		}
	}
	if snapshot.coordinatorOff(ctx, turn) {
		return "coordinator_off"
	}
	if snapshot.Chat == nil && (snapshot.LLM == nil || !snapshot.LLM.Enabled()) {
		return "coordinator_model_unavailable"
	}
	snapshot.prefetchSceneMemory(ctx, &turn)
	normalizeDecisionHistory(&turn)
	first := snapshot.prepareFirstRound(ctx, &turn, func(ctx context.Context, _ Turn) <-chan historyPrefetchResult {
		return waitWindowHistory(ctx, early)
	})
	// Round 0 of runLoop: the work-state snapshot is the feedback unless a
	// history read repair is already pending.
	feedback := first.latestFeedback
	if feedback == "" {
		feedback = workStateSnapshotFeedback(turn, first.initialReadSequence)
	}
	recalled := len(first.recalls) > 0
	messages := buildCoordinationMessages(turn, recalled, feedback, "")
	tools := roundTools(&turn, 0, recalled, first.recalledIssues, newRetryLedger())
	params, err := snapshot.wireParams(snapshot.configuredModel(), messages, tools, snapshot.completionBudget(), temperature, shared.ReasoningEffortNone)
	if err != nil {
		return "request_invalid"
	}
	request, err := fingerprintRequest(params)
	if err != nil {
		return "request_invalid"
	}
	entry.request = request
	entry.inputs = firstRoundInputs(turn, feedback)
	return ""
}

// waitWindowHistory yields the collect-window read without taking it, so the
// claimed decision still reuses it under its own rules.
func waitWindowHistory(ctx context.Context, early *windowHistoryRead) <-chan historyPrefetchResult {
	out := make(chan historyPrefetchResult, 1)
	go func() {
		started := time.Now()
		readCtx, cancel := context.WithTimeout(ctx, historyPrefetchTimeout)
		defer cancel()
		select {
		case <-early.done:
			result := early.result
			result.window, result.waited = "hit", time.Since(started)
			out <- result
		case <-readCtx.Done():
			waited := time.Since(started)
			out <- historyPrefetchResult{err: readCtx.Err(), timedOut: true, elapsed: waited, window: "timeout", waited: waited}
		}
	}()
	return out
}

// peekWindowHistory returns the recent collect-window read for turn without
// removing it.
func (c *Coordinator) peekWindowHistory(turn Turn) *windowHistoryRead {
	reads := c.windowHistory
	reads.mu.Lock()
	defer reads.mu.Unlock()
	read, ok := reads.entries[windowHistoryKey(turn)]
	if !ok || time.Since(read.started) > windowHistoryReuseAge {
		return nil
	}
	return read
}

// compareFirstRoundShadow logs whether the first request of a claimed
// decision equals the shadow built for its job. claimBuild is the claim's own
// assembly time up to that request.
func (c *Coordinator) compareFirstRoundShadow(turn Turn, params openai.ChatCompletionNewParams, feedback string, claimBuild time.Duration) {
	if c == nil || c.firstRoundShadows == nil || !c.windowHistoryAllowed || !firstRoundShadowApplies(turn) {
		return
	}
	compareStarted := time.Now()
	shadows := c.firstRoundShadows
	key := firstRoundShadowKey(turn)
	shadows.mu.Lock()
	entry := shadows.entries[key]
	delete(shadows.entries, key)
	shadows.mu.Unlock()
	logArgs := append(coordinatorLogIndex(turn), "event", "inbound_coordinator_speculation_shadow", "v_loop_first_ms", claimBuild.Milliseconds())
	outcome := ""
	switch {
	case entry == nil:
		outcome = "unavailable"
	default:
		select {
		case <-entry.done:
		default:
			outcome = "not_ready"
		}
	}
	if outcome == "" && entry.skipped != "" {
		outcome = "skipped"
		logArgs = append(logArgs, "skip_reason", entry.skipped)
	}
	if outcome == "" {
		claim, err := fingerprintRequest(params)
		if err != nil {
			outcome = "request_invalid"
		} else {
			inputs := firstRoundInputs(turn, feedback)
			sections := differingKeys(entry.request.sections, claim.sections)
			differing := differingKeys(entry.inputs, inputs)
			outcome = "same"
			if claim.full != entry.request.full {
				outcome = "diff"
			}
			logArgs = append(logArgs,
				"diff_sections", strings.Join(sections, ","), "diff_inputs", strings.Join(differing, ","),
				"unexplained", outcome == "diff" && len(differing) == 0,
				"claim_request", shortHash(claim.full), "shadow_request", shortHash(entry.request.full),
				"claim_request_bytes", claim.bytes, "shadow_request_bytes", entry.request.bytes,
				"shadow_build_ms", entry.build.Milliseconds(), "shadow_age_ms", time.Since(entry.started).Milliseconds())
		}
	}
	logArgs = append(logArgs, "outcome", outcome, "compare_us", time.Since(compareStarted).Microseconds())
	slog.Info("inbound coordinator speculation shadow", logArgs...)
}

type requestFingerprint struct {
	full     string
	sections map[string]string
	bytes    int
}

// fingerprintRequest hashes the request a model call sends in canonical JSON
// (keys sorted at every level), whole and by section: the system message,
// the other messages, the tools and the remaining route fields.
func fingerprintRequest(params openai.ChatCompletionNewParams) (requestFingerprint, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return requestFingerprint{}, err
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		return requestFingerprint{}, err
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return requestFingerprint{}, err
	}
	sections := map[string]string{}
	messages, _ := wire["messages"].([]any)
	if len(messages) > 0 {
		sections["system"] = hashJSON(messages[0])
		sections["messages"] = hashJSON(messages[1:])
	} else {
		sections["messages"] = hashJSON(messages)
	}
	sections["tools"] = hashJSON(wire["tools"])
	route := map[string]any{}
	for k, v := range wire {
		if k != "messages" && k != "tools" {
			route[k] = v
		}
	}
	sections["route"] = hashJSON(route)
	return requestFingerprint{full: hashBytes(canonical), sections: sections, bytes: len(canonical)}, nil
}

// firstRoundInputs hashes the Turn inputs of the first request by class, so a
// request difference can be traced to the input that moved.
func firstRoundInputs(turn Turn, feedback string) map[string]string {
	recall, reads := []CoordinationRead{}, []CoordinationRead{}
	for _, read := range turn.CoordinationReads {
		switch {
		case read.Tool == toolAssocRecall:
			recall = append(recall, read)
		case read.Tool == toolContextRead && read.Kind == "":
			// The history read is part of the history class.
		default:
			reads = append(reads, read)
		}
	}
	return map[string]string{
		"message": hashJSON([]any{turn.Message, turn.Utterances, turn.EvidenceID, turn.SenderName, turn.ConversationTitle,
			turn.MessageTimestamp, turn.Addressed, turn.ChatType, turn.Source, turn.Kind, turn.PersonID, turn.ConversationID, turn.IssueDispatchContext}),
		"agent": hashJSON([]any{turn.AgentName, turn.EmployeeAccountName, turn.Instructions, turn.InstructionsUnavailable,
			turn.CoordinatorContractState, turn.CoordinatorContractHash, turn.Persona, turn.ReplyTone, turn.Skills, turn.SkillsStatus,
			turn.IdentityNote, turn.TaskDeliveryContext, turn.OutstandingFollowUps}),
		"busy":         hashJSON(turn.Busy),
		"scene_memory": hashJSON([]any{turn.SceneMemory, turn.SceneMemoryRevision, turn.SceneMemoryStatus, turn.SceneTitle}),
		"history": hashJSON([]any{turn.DingTalkHistory, turn.History, turn.HistoryStatus, turn.HistoryError, turn.HistoryBefore,
			historyReads(turn.CoordinationReads)}),
		// The recall scope is the read's own time window; the items are what
		// it found. They move for different reasons, so they are separate.
		"recall_scope": hashJSON(recallParts(recall, true)),
		"recall_items": hashJSON([]any{recallParts(recall, false), turn.RelatedTasks, sortedCopy(turn.recalledIssueIDs)}),
		"reads":        hashJSON([]any{reads, turn.CoordinationReadsTruncated}),
		"work_state":   hashJSON(feedback),
		"route":        hashJSON(turn.model),
	}
}

// recallParts returns the scope of each recall read, or everything else.
func recallParts(reads []CoordinationRead, scope bool) []any {
	out := []any{}
	for _, read := range reads {
		var fields map[string]json.RawMessage
		if json.Unmarshal(read.Result, &fields) != nil {
			out = append(out, string(read.Result))
			continue
		}
		if scope {
			out = append(out, fields["scope"])
			continue
		}
		delete(fields, "scope")
		out = append(out, fields, read.Failed)
	}
	return out
}

func historyReads(reads []CoordinationRead) []CoordinationRead {
	out := []CoordinationRead{}
	for _, read := range reads {
		if read.Tool == toolContextRead && read.Kind == "" {
			out = append(out, read)
		}
	}
	return out
}

func differingKeys(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if b[k] != v {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func hashJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "unencodable"
	}
	return hashBytes(raw)
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func shortHash(full string) string {
	if len(full) > 12 {
		return full[:12]
	}
	return full
}
