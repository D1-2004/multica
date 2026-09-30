package connmgr

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// targetUnmetLogInterval delays and rate-limits the "READY target unmet"
// warning so a normal handoff never logs it.
const targetUnmetLogInterval = 30 * time.Second

var (
	errAttemptEnded = errors.New("connmgr: connection attempt already ended")
	errNilConn      = errors.New("connmgr: dial returned a nil Conn")
)

// supervise owns one key's contender. It loops: wait for its claim turn →
// Claim → Dial → Run (READY gating, renewal, drain) → release → back off.
// One process runs at most one contender per key; the coordinator admits the
// global READY target across every process. It returns when ctx is cancelled
// or once a drain request (shutdown or shed) has been honoured.
func (m *Manager) supervise(ctx context.Context, target Target, e *entry) {
	defer m.wg.Done()
	defer close(e.done)
	defer m.finishEntry(e)

	drain := e.drain
	token := leaseToken(m.nodeID, e.gen)
	log := m.log.With(
		"key", target.Key,
		"node_id", m.nodeID,
		"connection_id", token,
	)
	backoff := m.cfg.MinBackoff
	var unmet targetUnmetLimiter

	// Shed cooldown: do not contend for a key this replica just handed over.
	if !e.notBefore.IsZero() {
		if wait := e.notBefore.Sub(m.cfg.Now()); wait > 0 && sleepWithDrain(ctx, drain, wait) {
			return
		}
	}

	for {
		if ctx.Err() != nil || channelClosed(drain) {
			return
		}
		if m.waitClaimTurn(ctx, drain) {
			return
		}

		lease, snapshot, acquired, err := m.coord.Claim(ctx, target.Key, token)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn(m.cfg.Name+" claim failed",
					"event", m.event("claim_failed"),
					"error_class", errorClass(err),
					"error", err,
				)
			}
			if sleepWithDrain(ctx, drain, m.cfg.RenewInterval) {
				return
			}
			continue
		}
		m.logTargetUnmet(log, &unmet, "claim", snapshot)
		if !acquired {
			if sleepWithDrain(ctx, drain, m.cfg.RenewInterval) {
				return
			}
			continue
		}
		log.Info(m.cfg.Name+" member claimed", append([]any{
			"event", m.event("claimed"),
			"state", string(lease.State),
		}, snapshotAttrs(snapshot)...)...)

		drainRequested, uptime, stopReason := m.runAttempt(ctx, target, e, lease, snapshot, log, &unmet)
		if drainRequested && stopReason == "handoff_ready" {
			m.mu.Lock()
			e.handedOff = true
			m.mu.Unlock()
		}
		if ctx.Err() != nil || drainRequested || channelClosed(drain) {
			return
		}
		// A connection that lived long enough counts as stable: a single late
		// failure must not restart the schedule at the cap.
		if uptime >= m.cfg.ResetBackoffAfter {
			backoff = m.cfg.MinBackoff
		}
		if sleepWithDrain(ctx, drain, jitter(backoff)) {
			return
		}
		backoff = nextBackoff(backoff, m.cfg.MaxBackoff)
	}
}

// attempt is one claimed member from Claim to Release.
type attempt struct {
	m         *Manager
	entry     *entry
	log       *slog.Logger
	unmet     *targetUnmetLimiter
	runCancel context.CancelFunc
	watchdog  *leaseWatchdog

	mu          sync.Mutex // guards every field below
	lease       Lease
	deadline    time.Time
	counted     State // this attempt's contribution to m.connecting / m.ready
	wasReady    bool
	readyFailed bool
	finished    bool
	// shedDeferred records that a shed met another member's handoff and
	// waits for it (logged once).
	shedDeferred bool
}

// shedding reports whether this attempt's drain request is a rebalancing
// shed rather than a shutdown. It takes Manager.mu under a.mu, the only
// order in which the two are ever held together.
func (a *attempt) shedding() bool {
	a.m.mu.Lock()
	defer a.m.mu.Unlock()
	return a.entry.shed && !a.m.stopped
}

// runAttempt runs one claimed member to completion and releases it. It reports
// whether a drain was requested, how long the attempt lived, and why it
// stopped.
func (m *Manager) runAttempt(
	ctx context.Context,
	target Target,
	e *entry,
	lease Lease,
	snapshot Snapshot,
	log *slog.Logger,
	unmet *targetUnmetLimiter,
) (bool, time.Duration, string) {
	startedAt := m.cfg.Now()
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	a := &attempt{m: m, entry: e, log: log, unmet: unmet, runCancel: runCancel, lease: lease}
	a.account(StateConnecting)
	a.deadline = m.cfg.Now().Add(m.remaining(snapshot))
	a.watchdog = newLeaseWatchdog(runCancel, m.cfg.Now, a.deadline)

	stopReason, runErr, drainRequested := a.run(ctx, runCtx, target, e.drain)

	a.watchdog.Stop()
	runCancel()
	final, wasReady := a.finish()
	m.release(final, log)

	uptime := m.cfg.Now().Sub(startedAt)
	log.Info(m.cfg.Name+" connection stopped",
		"event", m.event("stopped"),
		"stop_reason", stopReason,
		"uptime_ms", uptime.Milliseconds(),
		"ready", wasReady,
		"error", runErr,
	)
	return drainRequested, uptime, stopReason
}

// run dials and supervises the Conn until a stop reason is reached, then
// cancels it and waits for Run to return.
func (a *attempt) run(ctx, runCtx context.Context, target Target, drain <-chan struct{}) (stopReason string, runErr error, drainRequested bool) {
	m := a.m
	if ctx.Err() != nil {
		return "context_cancelled", nil, false
	}
	if channelClosed(drain) {
		return "shutdown_before_ready", nil, true
	}
	conn, err := m.cfg.Dial(target)
	if err == nil && conn == nil {
		err = errNilConn
	}
	if err != nil {
		a.log.Error(m.cfg.Name+" dial failed",
			"event", m.event("dial_failed"),
			"error_class", "dial",
			"error", err,
		)
		return "dial_failed", err, false
	}
	if a.watchdog.Expired() {
		// Dial outlived the lease; a successor may already be eligible.
		return "lease_deadline", nil, false
	}

	connectDone := make(chan error, 1)
	ready := a.readyFunc(runCtx)
	go func() { connectDone <- conn.Run(runCtx, ready) }()

	ticker := time.NewTicker(m.cfg.RenewInterval)
	defer ticker.Stop()
	drainCh := drain
	connectReturned := false

	for stopReason == "" {
		select {
		case runErr = <-connectDone:
			connectReturned = true
			switch {
			case a.watchdog.Expired():
				stopReason = "lease_deadline"
			case a.isReadyFailed():
				stopReason = "ready_failed"
			default:
				stopReason = "transport_exit"
			}
		case <-ctx.Done():
			stopReason = "context_cancelled"
		case <-drainCh:
			drainCh = nil
			drainRequested = true
			if !a.isReady() {
				stopReason = "shutdown_before_ready"
			}
		case <-ticker.C:
			stopReason = a.tick(runCtx, drainRequested)
		}
	}
	a.runCancel()
	if !connectReturned {
		runErr = <-connectDone
	}
	return stopReason, runErr, drainRequested
}

// readyFunc is the callback handed to Conn.Run. It moves the member to READY
// under the lease-bounded context; on failure it also cancels the attempt so a
// Conn that ignores the error still cannot consume without a READY member.
func (a *attempt) readyFunc(runCtx context.Context) func(context.Context) error {
	m := a.m
	return func(readyCtx context.Context) error {
		if readyCtx == nil {
			readyCtx = runCtx
		}
		a.mu.Lock()
		if a.finished {
			a.mu.Unlock()
			return errAttemptEnded
		}
		if a.lease.State != StateConnecting {
			// Already READY (or DRAINING): ready is idempotent.
			a.mu.Unlock()
			return nil
		}
		opCtx, opCancel := operationContext(readyCtx, m.cfg.Now, a.deadline)
		next, snapshot, err := m.coord.MarkReady(opCtx, a.lease)
		opCancel()
		if err == nil {
			a.update(next, snapshot)
			a.account(StateReady)
			a.wasReady = true
		} else {
			a.readyFailed = true
		}
		a.mu.Unlock()

		if err != nil {
			a.log.Warn(m.cfg.Name+" ready transition failed",
				"event", m.event("ready_failed"),
				"error_class", errorClass(err),
				"error", err,
			)
			a.runCancel()
			return err
		}
		a.log.Info(m.cfg.Name+" connection ready", append([]any{
			"event", m.event("ready"),
		}, snapshotAttrs(snapshot)...)...)
		return nil
	}
}

// tick performs one renewal round: begin (or continue) a requested drain,
// then renew. It returns a non-empty stop reason when the attempt must end.
func (a *attempt) tick(runCtx context.Context, drainRequested bool) string {
	m := a.m
	a.mu.Lock()
	defer a.mu.Unlock()

	stopReason := ""
	if drainRequested && a.lease.State == StateReady {
		opCtx, opCancel := operationContext(runCtx, m.cfg.Now, a.deadline)
		next, snapshot, err := m.coord.BeginDrain(opCtx, a.lease)
		opCancel()
		switch {
		case err == nil:
			a.update(next, snapshot)
			a.account("")
			a.log.Info(m.cfg.Name+" graceful handoff started", append([]any{
				"event", m.event("draining"),
			}, snapshotAttrs(snapshot)...)...)
			if snapshot.ReadyTargetMet() {
				stopReason = "handoff_ready"
			}
		case errors.Is(err, ErrDrainInProgress) && a.shedding():
			// A shed is optional: keep serving and begin the handoff again on
			// a later renewal, once the other member's handoff has finished.
			if !a.shedDeferred {
				a.shedDeferred = true
				a.log.Info(m.cfg.Name+" handoff already in progress; shed deferred",
					"event", m.event("shed_deferred"),
				)
			}
		case errors.Is(err, ErrDrainInProgress):
			// Another old member already owns the one handoff credit. Closing
			// this READY member lets replacements fill the non-draining slots
			// without ever exceeding target+1 members.
			stopReason = "parallel_drain"
			a.log.Info(m.cfg.Name+" handoff already in progress; closing",
				"event", m.event("parallel_drain"),
			)
		default:
			a.log.Warn(m.cfg.Name+" begin drain failed",
				"event", m.event("drain_failed"),
				"error_class", errorClass(err),
				"error", err,
			)
		}
	}
	if stopReason != "" {
		return stopReason
	}

	opCtx, opCancel := operationContext(runCtx, m.cfg.Now, a.deadline)
	snapshot, err := m.coord.Renew(opCtx, a.lease)
	opCancel()
	if err == nil {
		a.deadline = m.cfg.Now().Add(m.remaining(snapshot))
		a.watchdog.Reset(a.deadline)
		m.logTargetUnmet(a.log, a.unmet, "renew", snapshot)
		if a.lease.State == StateDraining && snapshot.ReadyTargetMet() {
			stopReason = "handoff_ready"
		}
		return stopReason
	}
	fatal := errors.Is(err, ErrLeaseLost) ||
		errors.Is(err, ErrLeaseStateMismatch) ||
		errors.Is(err, ErrCorruptState)
	if fatal || a.watchdog.Expired() || !m.cfg.Now().Before(a.deadline) {
		stopReason = "lease_lost"
	}
	a.log.Warn(m.cfg.Name+" lease renewal failed",
		"event", m.event("renew_failed"),
		"error_class", errorClass(err),
		"fail_closed", stopReason == "lease_lost",
		"error", err,
	)
	return stopReason
}

// update records a new lease state and moves the local deadline. Callers hold
// a.mu.
func (a *attempt) update(next Lease, snapshot Snapshot) {
	a.lease = next
	a.deadline = a.m.cfg.Now().Add(a.m.remaining(snapshot))
	a.watchdog.Reset(a.deadline)
}

// account moves this attempt's contribution to the replica counters: CONNECTING
// and READY are counted, DRAINING and ended ("") are not. Callers hold a.mu
// (or own the attempt exclusively).
func (a *attempt) account(next State) {
	switch a.counted {
	case StateConnecting:
		a.m.connecting.Add(-1)
	case StateReady:
		a.m.ready.Add(-1)
	}
	switch next {
	case StateConnecting:
		a.m.connecting.Add(1)
	case StateReady:
		a.m.ready.Add(1)
	default:
		next = ""
	}
	a.counted = next
	a.entry.ready.Store(next == StateReady)
}

func (a *attempt) isReady() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wasReady
}

func (a *attempt) isReadyFailed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.readyFailed
}

// finish seals the attempt after Run returned: late ready calls fail, counters
// drop this member, and the final lease is returned for release.
func (a *attempt) finish() (Lease, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.finished = true
	a.account("")
	return a.lease, a.wasReady
}

// remaining is the local lease budget implied by a snapshot, falling back to
// the full TTL when the coordinator returned no timestamps.
func (m *Manager) remaining(snapshot Snapshot) time.Duration {
	if remaining := snapshot.RemainingTTL(); remaining > 0 {
		return remaining
	}
	return m.coord.LeaseTTL()
}

// release removes the member on a fresh bounded context (the parent is
// usually cancelled by now). On failure the member expires after its TTL.
func (m *Manager) release(lease Lease, log *slog.Logger) {
	timeout := m.coord.LeaseTTL()
	if timeout <= 0 || timeout > maxReleaseTimeout {
		timeout = maxReleaseTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := m.coord.Release(ctx, lease); err != nil {
		log.Warn(m.cfg.Name+" lease release failed",
			"event", m.event("release_failed"),
			"error_class", errorClass(err),
			"error", err,
		)
		return
	}
	log.Info(m.cfg.Name+" lease released", "event", m.event("released"))
}

// waitClaimTurn delays a claim for balancing: ClaimDelayStep per member this
// replica holds (CONNECTING or READY) beyond the least-loaded live peer,
// capped at ClaimDelayMax, plus a random offset in [0, ClaimDelayStep). The
// held count is re-read whenever the wait runs out, so a sibling contender's
// win during the wait pushes this claim back by another step; replicas that
// start together therefore take turns instead of one replica claiming every
// free key in the same instant. The least-loaded replica waits only the
// offset, so takeovers are not slowed. The wait is measured by elapsed
// timers, not the injectable clock. It returns true when the contender must
// stop.
func (m *Manager) waitClaimTurn(ctx context.Context, drain <-chan struct{}) bool {
	offset := randomDuration(m.cfg.ClaimDelayStep)
	var waited time.Duration
	for {
		need := offset + m.claimDelay()
		if waited >= need {
			return ctx.Err() != nil || channelClosed(drain)
		}
		d := need - waited
		if sleepWithDrain(ctx, drain, d) {
			return true
		}
		waited += d
	}
}

func (m *Manager) claimDelay() time.Duration {
	held := m.connecting.Load() + m.ready.Load() - m.minPeerHeld.Load()
	if held <= 0 {
		return 0
	}
	step, limit := m.cfg.ClaimDelayStep, m.cfg.ClaimDelayMax
	if held >= int64(limit/step) {
		return limit
	}
	return time.Duration(held) * step
}

// leaseWatchdog is independent of coordinator I/O. A blocked Renew must not
// keep a connection consuming after its member has expired and a successor is
// eligible to claim capacity. Reset is called only after a successful
// transition or renewal; expiry cancels the connection.
type leaseWatchdog struct {
	mu         sync.Mutex
	timer      *time.Timer
	generation uint64
	stopped    bool
	expired    atomic.Bool
	cancel     context.CancelFunc
	now        func() time.Time
}

func newLeaseWatchdog(cancel context.CancelFunc, now func() time.Time, deadline time.Time) *leaseWatchdog {
	w := &leaseWatchdog{cancel: cancel, now: now}
	w.Reset(deadline)
	return w
}

func (w *leaseWatchdog) Reset(deadline time.Time) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.expired.Load() {
		return
	}
	w.generation++
	generation := w.generation
	if w.timer != nil {
		w.timer.Stop()
	}
	delay := deadline.Sub(w.now())
	if delay < 0 {
		delay = 0
	}
	w.timer = time.AfterFunc(delay, func() {
		w.mu.Lock()
		if w.stopped || generation != w.generation || !w.expired.CompareAndSwap(false, true) {
			w.mu.Unlock()
			return
		}
		cancel := w.cancel
		w.mu.Unlock()
		cancel()
	})
}

func (w *leaseWatchdog) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	w.generation++
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
}

func (w *leaseWatchdog) Expired() bool {
	return w != nil && w.expired.Load()
}

// operationContext bounds one coordinator operation by the currently fenced
// local lease deadline. The watchdog independently cancels the connection at
// the same boundary; this context makes a conforming client return promptly
// instead of holding the contender beyond it.
func operationContext(parent context.Context, now func() time.Time, deadline time.Time) (context.Context, context.CancelFunc) {
	remaining := deadline.Sub(now())
	if remaining < 0 {
		remaining = 0
	}
	return context.WithTimeout(parent, remaining)
}

// targetUnmetLimiter delays the first warning and then emits at most one
// warning per interval while READY remains below target. Meeting the target
// resets the observation window, so a later degradation gets its own signal.
type targetUnmetLimiter struct {
	firstObserved time.Time
	lastLogged    time.Time
}

func (l *targetUnmetLimiter) shouldLog(now time.Time, snapshot Snapshot) bool {
	if snapshot.TargetReady <= 0 || snapshot.Ready >= snapshot.TargetReady {
		l.firstObserved = time.Time{}
		l.lastLogged = time.Time{}
		return false
	}
	if l.firstObserved.IsZero() || now.Before(l.firstObserved) {
		l.firstObserved = now
		l.lastLogged = time.Time{}
		return false
	}
	if now.Sub(l.firstObserved) < targetUnmetLogInterval {
		return false
	}
	if !l.lastLogged.IsZero() && now.Sub(l.lastLogged) < targetUnmetLogInterval {
		return false
	}
	l.lastLogged = now
	return true
}

func (m *Manager) logTargetUnmet(log *slog.Logger, limiter *targetUnmetLimiter, phase string, snapshot Snapshot) {
	if limiter == nil || !limiter.shouldLog(m.cfg.Now(), snapshot) {
		return
	}
	log.Warn(m.cfg.Name+" READY target remains unmet", append([]any{
		"event", m.event("target_unmet"),
		"phase", phase,
	}, snapshotAttrs(snapshot)...)...)
}

func snapshotAttrs(s Snapshot) []any {
	return []any{
		"target_ready", s.TargetReady,
		"member_total", s.Total,
		"member_connecting", s.Connecting,
		"member_ready", s.Ready,
		"member_draining", s.Draining,
	}
}

func errorClass(err error) string {
	switch {
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, ErrLeaseStateMismatch):
		return "state_mismatch"
	case errors.Is(err, ErrDrainInProgress):
		return "drain_in_progress"
	case errors.Is(err, ErrCorruptState):
		return "corrupt_state"
	case errors.Is(err, ErrConfig):
		return "configuration"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	default:
		return "transport"
	}
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// sleepWithDrain sleeps d and reports true when ctx ended or drain closed
// first.
func sleepWithDrain(ctx context.Context, drain <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil || channelClosed(drain)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-drain:
		return true
	case <-timer.C:
		return false
	}
}

// leaseToken composes the per-contender lease token: the process-wide nodeID
// (for cross-replica observability) plus the contender's generation, so a
// restarted contender for the same key in the same process carries a
// different token and a stale release cannot delete its successor's member.
// The token is an identity, not a secret.
func leaseToken(nodeID string, gen uint64) string {
	return nodeID + "-g" + strconv.FormatUint(gen, 10)
}

// newNodeID returns a 16-byte hex random string unique to this process.
func newNodeID() string {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		// crypto/rand failure is catastrophic and rare; fall back to a
		// timestamp-derived id rather than panicking on boot.
		return fmt.Sprintf("nodeid-fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// nextBackoff doubles the current backoff up to max.
func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

// jitter spreads reconnect storms across the [0.5d, 1.5d] window so many keys
// do not all retry on the same timer edge.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := d / 2
	return d - delta + time.Duration(mathrand.Int64N(int64(2*delta)+1))
}

// randomDuration returns a uniform duration in [0, d).
func randomDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(mathrand.Int64N(int64(d)))
}
