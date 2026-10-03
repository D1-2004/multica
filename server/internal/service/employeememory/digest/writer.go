package digest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/langfuse"
	openai "github.com/openai/openai-go/v3"
)

// Writer is the lease-claimed scene digest worker. It is safe on every
// replica: all coordination lives in employee_scene_digest_state.
type Writer struct {
	DB         DB
	Transcript Transcript
	Facts      FactStore
	Model      Model
	Fence      Fence
	// Ready gates claims on MemoryMarker across all live replicas and a
	// normal deployment fence. A non-nil error means "not now", not failure.
	Ready    func(context.Context) error
	Langfuse *langfuse.Client
}

// Enabled reports whether every dependency is wired.
func (w *Writer) Enabled() bool {
	return w != nil && w.DB != nil && w.Transcript != nil && w.Facts != nil && w.Model != nil && w.Fence != nil && w.Ready != nil
}

// ProcessNext claims at most one due scene and settles it.
func (w *Writer) ProcessNext(ctx context.Context) (bool, error) {
	if !w.Enabled() {
		return false, nil
	}
	if err := w.Ready(ctx); err != nil {
		return false, nil
	}
	c, err := claimNext(ctx, w.DB)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = w.process(ctx, c)
	if errors.Is(err, ErrLeaseLost) {
		slog.WarnContext(ctx, "employee scene digest lease lost", "event", "employee_scene_digest_lease_lost", "scene_id", c.Key.SceneID, "agent_id", c.Key.AgentID)
		return true, nil
	}
	return true, err
}

// short settles a claim that makes no model request.
func (w *Writer) short(ctx context.Context, c claim, outcome, reason string, b pageBounds, s settle) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockLease(ctx, tx, c); err != nil {
		return err
	}
	if s.RunID, err = recordShortRun(ctx, tx, c, c.Trigger, outcome, reason, b); err != nil {
		return err
	}
	blocked, err := release(ctx, tx, c, s)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	logRun(ctx, c, s.RunID, outcome, reason, 0, 0, 0, blocked)
	return nil
}

type readResult struct {
	page      *page
	blockedBy string
	exhausted bool
	caughtUp  bool
}

// read loads everything a claim needs in one short read transaction: the
// fences, today's budget, the transcript page, active facts and the ledger.
func (w *Writer) read(ctx context.Context, c claim) (readResult, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return readResult{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	reason, err := w.Fence(ctx, tx, c.Key)
	if err != nil {
		return readResult{}, err
	}
	if reason != "" {
		return readResult{blockedBy: reason}, nil
	}
	var sceneCalls, agentCalls int
	if err = tx.QueryRow(ctx, `SELECT CASE WHEN budget_day=`+budgetDaySQL+` THEN budget_calls ELSE 0 END FROM employee_scene_digest_state WHERE `+stateKey, c.Key.args()...).Scan(&sceneCalls); err != nil {
		return readResult{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(budget_calls),0) FROM employee_scene_digest_state WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND budget_day=`+budgetDaySQL, c.Key.WorkspaceID, c.Key.AgentID).Scan(&agentCalls); err != nil {
		return readResult{}, err
	}
	if sceneCalls >= SceneDailyCalls || agentCalls >= AgentDailyCalls {
		return readResult{exhausted: true}, nil
	}
	lines, err := w.Transcript.After(ctx, tx, c.Key, c.CursorAt, c.CursorID, PageLimit+1)
	if err != nil {
		return readResult{}, err
	}
	full := len(lines) > PageLimit
	if full {
		lines = lines[:PageLimit]
	}
	if len(lines) == 0 {
		return readResult{caughtUp: true}, nil
	}
	contextLines, err := w.Transcript.Before(ctx, tx, c.Key, lines[0].SentAt, lines[0].ProviderMessageID, ContextLines)
	if err != nil {
		return readResult{}, err
	}
	facts, err := w.Facts.ActiveFacts(ctx, tx, c.Key, 200)
	if err != nil {
		return readResult{}, err
	}
	ledger, err := ListLedger(ctx, tx, c.Key, lines[0].SentAt.Add(-time.Hour), 10)
	if err != nil {
		return readResult{}, err
	}
	return readResult{page: buildPage(c.Key, contextLines, lines, facts, ledger, FlushActor(c.Key.AgentID), full)}, nil
}

func backoffSeconds(noProgress int) int64 {
	seconds := int64(300)
	for i := 0; i < noProgress && seconds < 7200; i++ {
		seconds *= 2
	}
	if seconds > 7200 {
		seconds = 7200
	}
	return seconds
}

func (w *Writer) process(ctx context.Context, c claim) error {
	r, err := w.read(ctx, c)
	if err != nil {
		// A read failure keeps the cursor and backs off; it is not progress.
		if errors.Is(err, context.Canceled) {
			return err
		}
		if shortErr := w.short(ctx, c, OutcomeError, "read: "+err.Error(), pageBounds{}, settle{HoldSeconds: backoffSeconds(c.NoProgress)}); shortErr != nil {
			return errors.Join(err, shortErr)
		}
		return err
	}
	switch {
	case r.blockedBy != "":
		return w.short(ctx, c, OutcomeBlocked, r.blockedBy, pageBounds{}, settle{Progress: true, BlockReason: r.blockedBy})
	case r.exhausted:
		return w.short(ctx, c, OutcomeBudgetExhausted, "daily budget", pageBounds{}, settle{Neutral: true, HoldSeconds: holdNextDay})
	case r.caughtUp:
		return w.short(ctx, c, OutcomeNoChange, "caught up", pageBounds{}, settle{Progress: true, ConsumedHuman: c.Pending})
	}
	p := r.page
	advance := settle{Advance: true, CursorAt: p.Bounds.ToAt, CursorID: p.Bounds.ToID, ConsumedHuman: p.Bounds.Human, MorePages: p.Full, Progress: true}
	if p.trivial() {
		return w.short(ctx, c, OutcomeSkippedTrivial, "", p.Bounds, advance)
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	if err = lockLease(ctx, tx, c); err != nil {
		tx.Rollback(context.WithoutCancel(ctx))
		return err
	}
	run, err := openRun(ctx, tx, c, c.Trigger, p.hash(), p.Bounds)
	if err == nil {
		err = tx.Commit(ctx)
	}
	tx.Rollback(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	trace := w.startTrace(ctx, c, run, p)
	result, applyErr := w.generate(ctx, c, &run, p, trace)
	if applyErr != nil {
		trace.End(langfuse.EndOptions{Err: applyErr})
		return applyErr
	}
	trace.AddMetadata(map[string]any{"outcome": result.Outcome, "ops_proposed": result.Proposed, "ops_accepted": result.Accepted, "ops_rejected": len(result.Rejected), "calls": run.Calls, "resumed": run.Resumed})
	trace.End(langfuse.EndOptions{Output: map[string]any{"outcome": result.Outcome, "accepted": result.Accepted, "rejected": boundRejections(result.Rejected), "error": result.Error}})
	return nil
}

func (w *Writer) startTrace(ctx context.Context, c claim, run runRow, p *page) *langfuse.Trace {
	trace := w.Langfuse.StartTrace(ctx, langfuse.TraceOptions{
		TraceID: run.ID, Name: "employee_scene_digest", Type: langfuse.TypeAgent, SessionID: c.Key.SceneID,
		Tags: []string{"employee_memory", langfuse.Tag("workspace", c.Key.WorkspaceID), langfuse.Tag("agent", c.Key.AgentID)},
		Metadata: map[string]any{
			"loop": "employee_scene_digest", "run_id": run.ID, "scene_id": c.Key.SceneID, "workspace_id": c.Key.WorkspaceID,
			"agent_id": c.Key.AgentID, "tenant_org_id": c.Key.TenantOrgID, "trigger": c.Trigger, "lease_generation": c.Generation,
			"page_messages": p.Bounds.Messages, "page_human": p.Bounds.Human, "page_full": p.Full, "page_hash": run.PageHash,
			"active_own_items": len(p.Own), "active_member_items": len(p.Others),
		},
	})
	trace.Index(map[string]string{"employee_digest_run_id": run.ID, "scene_id": c.Key.SceneID})
	return trace
}

// generate runs at most MaxCallsPerClaim journaled calls, validates, and
// settles the claim. Provider failures keep the cursor and back off.
func (w *Writer) generate(ctx context.Context, c claim, run *runRow, p *page, trace *langfuse.Trace) (runResult, error) {
	pl := newPlan()
	messages := requestMessages(p)
	original := messages
	failure := ""
	exhausted := false
	for ordinal := 0; ordinal < MaxCallsPerClaim; ordinal++ {
		params := completionParams(messages)
		raw, callFailure, err := w.call(ctx, c, run, ordinal, params, trace)
		if errors.Is(err, errBudgetExhausted) {
			exhausted = ordinal == 0
			break
		}
		if err != nil {
			return runResult{}, err
		}
		if callFailure == "interrupted" && ordinal == 0 {
			// The previous process died during I/O; the call is spent. Retry
			// the original request once with the remaining budget.
			messages = original
			continue
		}
		if callFailure != "" {
			failure = callFailure
			break
		}
		failure = ""
		ops, msg, callID, perr := parseCompletion(raw)
		base := pl.Proposed
		if perr != nil {
			reason := "invalid_arguments"
			if errors.Is(perr, errOutputTruncated) {
				reason = "output_truncated"
			} else if errors.Is(perr, errNoToolCall) {
				reason = "no_tool_call"
			}
			pl.Rejections = append(pl.Rejections, Rejection{Index: base, Reason: reason})
		} else {
			validate(pl, p, ops, w.Facts, base)
		}
		if ordinal+1 >= MaxCallsPerClaim || !pl.fixable() || msg == nil {
			break
		}
		messages = repairMessages(original, callID, pl)
	}
	if exhausted {
		return w.finish(ctx, c, run, runResult{Outcome: OutcomeBudgetExhausted, Error: "daily budget"}, settle{Neutral: true, HoldSeconds: holdNextDay}, nil)
	}
	if failure != "" && pl.Proposed == 0 && len(pl.Rejections) == 0 {
		outcome := OutcomeError
		if strings.HasPrefix(failure, "timeout") {
			outcome = OutcomeTimeout
		}
		return w.finish(ctx, c, run, runResult{Outcome: outcome, Error: failure}, settle{HoldSeconds: backoffSeconds(c.NoProgress)}, nil)
	}
	return w.finish(ctx, c, run, runResult{Proposed: pl.Proposed}, settle{Advance: true, CursorAt: p.Bounds.ToAt, CursorID: p.Bounds.ToID, ConsumedHuman: p.Bounds.Human, MorePages: p.Full}, pl)
}

// call returns the journaled response of ordinal, performing provider I/O
// only when the journal has no entry for it.
func (w *Writer) call(ctx context.Context, c claim, run *runRow, ordinal int, params openai.ChatCompletionNewParams, trace *langfuse.Trace) (json.RawMessage, string, error) {
	if ordinal < len(run.Journal) {
		entry := run.Journal[ordinal]
		switch {
		case len(entry.Response) > 0:
			trace.Event(langfuse.ObservationOptions{Name: "employee_scene_digest.replay", Metadata: map[string]any{"ordinal": ordinal}}, langfuse.EndOptions{})
			return entry.Response, "", nil
		case entry.Failure != "":
			return nil, entry.Failure, nil
		default:
			return nil, "interrupted", nil
		}
	}
	sha := requestSHA(params)
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	if err = lockLease(ctx, tx, c); err == nil {
		if err = reserveCall(ctx, tx, c, run, ordinal, sha); err == nil {
			err = tx.Commit(ctx)
		}
	}
	tx.Rollback(context.WithoutCancel(ctx))
	if err != nil {
		return nil, "", err
	}
	generation := trace.StartObservation(langfuse.ObservationOptions{
		Type: langfuse.TypeGeneration, Name: "employee_scene_digest.call", Input: params.Messages,
		ModelParameters: map[string]any{"tools": []string{proposeTool}, "tool_choice": proposeTool, "strict": true, "max_completion_tokens": MaxCompletionTokens, "temperature": 0},
		Metadata:        map[string]any{"ordinal": ordinal, "request_sha256": sha},
	})
	callCtx, cancel := context.WithTimeout(ctx, CallTimeout)
	completion, model, callErr := w.Model.Chat(callCtx, params)
	if callErr == nil && callCtx.Err() != nil {
		callErr = callCtx.Err()
	}
	cancel()
	if callErr == nil && completion == nil {
		callErr = errors.New("empty completion")
	}
	end := langfuse.EndOptions{Err: callErr, Metadata: map[string]any{"model": model}}
	var raw json.RawMessage
	failure := ""
	var promptTokens, completionTokens int64
	if callErr != nil {
		failure = "error: " + callErr.Error()
		if errors.Is(callErr, context.DeadlineExceeded) {
			failure = "timeout: " + callErr.Error()
		}
	} else {
		if raw, err = json.Marshal(completion); err != nil {
			return nil, "", err
		}
		promptTokens, completionTokens = completion.Usage.PromptTokens, completion.Usage.CompletionTokens
		end.Usage = &langfuse.Usage{Input: promptTokens, Output: completionTokens, Total: completion.Usage.TotalTokens}
		if len(completion.Choices) > 0 {
			end.Output = completion.Choices[0].Message
			end.Metadata["finish_reason"] = completion.Choices[0].FinishReason
		}
	}
	generation.End(end)
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	tx, err = w.DB.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockLease(ctx, tx, c); err != nil {
		return nil, "", err
	}
	if err = saveCall(ctx, tx, run, ordinal, model, raw, failure, promptTokens, completionTokens); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return raw, failure, nil
}

// finish applies the validated plan (if any) and settles the claim in one
// transaction: lease CAS, use-time fence, per-operation savepoints, run
// outcome, cursor and retention.
func (w *Writer) finish(ctx context.Context, c claim, run *runRow, r runResult, s settle, pl *plan) (runResult, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if err = lockLease(ctx, tx, c); err != nil {
		return r, err
	}
	if pl != nil {
		reason, fenceErr := w.Fence(ctx, tx, c.Key)
		if fenceErr != nil {
			return r, fenceErr
		}
		if reason != "" {
			r = runResult{Outcome: OutcomeBlocked, Error: reason, Proposed: pl.Proposed}
			s = settle{Progress: true, BlockReason: reason}
			pl = nil
		}
	}
	if pl != nil {
		accepted, policyOnly, applyErr := w.apply(ctx, tx, c, pl)
		if applyErr != nil {
			return r, applyErr
		}
		r.Accepted, r.Rejected, r.Proposed = accepted, pl.Rejections, pl.Proposed
		switch {
		case accepted > 0:
			r.Outcome, s.Progress = OutcomeCommitted, true
		case len(pl.Rejections) == 0 || policyOnly:
			r.Outcome, s.Progress = OutcomeNoChange, true
		default:
			r.Outcome = OutcomeRejected
		}
	}
	if err = finishRun(ctx, tx, run.ID, r); err != nil {
		return r, err
	}
	s.RunID = run.ID
	blocked, err := release(ctx, tx, c, s)
	if err != nil {
		return r, err
	}
	if err = pruneScene(ctx, tx, c.Key); err != nil {
		return r, err
	}
	if err = tx.Commit(ctx); err != nil {
		return r, err
	}
	logRun(ctx, c, run.ID, r.Outcome, r.Error, run.Calls, r.Accepted, len(r.Rejected), blocked)
	return r, nil
}

// apply writes retracts, then upserts, each in its own savepoint so one
// refused operation never aborts the batch. It reports whether every
// non-accepted operation was a policy refusal or an idempotent replay.
func (w *Writer) apply(ctx context.Context, tx pgx.Tx, c claim, pl *plan) (int, bool, error) {
	actor := FlushActor(c.Key.AgentID)
	accepted := 0
	attempt := func(index int, ref, op string, fn func(pgx.Tx) error) error {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		if err = fn(sp); err != nil {
			_ = sp.Rollback(ctx)
			var refused *OpRejectedError
			if errors.As(err, &refused) {
				pl.Rejections = append(pl.Rejections, Rejection{Index: index, Op: op, Ref: ref, Reason: "store_" + refused.Reason})
				return nil
			}
			return err
		}
		return sp.Commit(ctx)
	}
	for _, r := range pl.Retracts {
		if err := attempt(r.Index, r.Tag, "retract", func(sp pgx.Tx) error {
			if err := w.Facts.Retract(ctx, sp, c.Key, r.Fact.ID, actor); err != nil {
				return err
			}
			accepted++
			return nil
		}); err != nil {
			return 0, false, err
		}
	}
	for _, u := range pl.Upserts {
		if err := attempt(u.Index, u.Tag, "upsert", func(sp pgx.Tx) error {
			res, err := w.Facts.Upsert(ctx, sp, c.Key, actor, u.Op)
			if err != nil {
				return err
			}
			if res.Replayed && !res.Changed {
				pl.Rejections = append(pl.Rejections, Rejection{Index: u.Index, Op: "upsert", Ref: u.Tag, Reason: "replayed_message"})
				return nil
			}
			accepted++
			return nil
		}); err != nil {
			return 0, false, err
		}
	}
	return accepted, pl.onlyPolicyRejections(), nil
}

func logRun(ctx context.Context, c claim, runID, outcome, reason string, calls, accepted, rejected int, blocked bool) {
	fields := []any{"event", "employee_scene_digest_run", "run_id", runID, "workspace_id", c.Key.WorkspaceID, "agent_id", c.Key.AgentID, "scene_id", c.Key.SceneID,
		"trigger", c.Trigger, "lease_generation", c.Generation, "outcome", outcome, "reason", clip(reason, 200), "calls", calls, "ops_accepted", accepted, "ops_rejected", rejected}
	if blocked {
		slog.ErrorContext(ctx, "employee scene digest blocked", append(fields, "blocked", true)...)
		return
	}
	slog.InfoContext(ctx, "employee scene digest run", fields...)
}
