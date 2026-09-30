// Package connmgr keeps a set of keyed, long-lived connections (for example
// WebSocket event sources) running across server replicas.
//
// For every desired Target the Manager runs one contender goroutine. A
// shared Coordinator (normally RedisCoordinator) admits exactly TargetReady
// (normally one) connection per key cluster-wide:
//
//   - Claim reserves a CONNECTING member; the Conn dials and calls ready once
//     it can receive, which moves the member to READY. A Conn must not consume
//     events before ready succeeds.
//   - The member is renewed every RenewInterval. A local watchdog, independent
//     of coordinator I/O, cancels the connection when the lease deadline
//     passes without a successful renewal (fail closed).
//   - A dropped link releases the member and reclaims with jittered
//     exponential backoff. A crashed replica's members expire after the lease
//     TTL and are claimed elsewhere.
//   - BeginShutdown moves READY members to DRAINING. A DRAINING connection
//     keeps consuming until a replacement on another replica is READY, then
//     closes (graceful handoff for rolling deploys).
//
// # Balancing
//
// Two mechanisms spread connections across replicas.
//
// Claim delay decides who wins a free key. Before each Claim a contender waits
// ClaimDelayStep × (members this replica holds − the fewest READY
// connections any other live replica reported), capped at ClaimDelayMax,
// plus a random offset in [0, ClaimDelayStep). Members count from the moment
// they are claimed and are re-read during the wait, so replicas that start
// together take turns instead of one of them claiming every key at once. The
// least-loaded replica waits only the offset, so a handoff or crash takeover
// is not slowed by the delay (a crashed replica keeps reporting its old load
// until its membership expires); only more-loaded replicas hold back. When no
// other replica is known (no Peers, or none alive) the least peer load is
// taken as 0 and the delay is ClaimDelayStep × (members held), which still
// spreads replicas that boot together before their heartbeats meet.
//
// Shedding (requires Config.Peers) moves connections that are already held.
// Every sweep a Manager heartbeats its node id and READY count into Peers
// (TTL 3 × PollInterval; BeginShutdown leaves at once). On each
// PollInterval tick, when at least two replicas are alive and this replica
// holds more than fair = ceil(desired keys / live replicas) READY
// connections, and some other replica reports fewer than fair, it hands ONE
// READY connection over through the same DRAINING path as BeginShutdown: the
// connection keeps consuming until a replacement on another replica is READY,
// then its contender ends. The Manager never sheds below fair, runs at most
// one shed at a time, and waits one PollInterval before contending for a shed
// key again. If no replica takes the key within DrainTimeout the shed is
// reported as stalled but the connection is never dropped: it keeps
// consuming as DRAINING until a replacement is READY, and further sheds wait
// until it has ended. Convergence is one connection per replica per
// PollInterval.
//
// The package is a generalized port of the robot connector's coordinated
// stream mode (internal/integrations/channel/engine). It has no knowledge of
// channels, installations, or credentials.
package connmgr

import (
	"context"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultPollInterval      = 10 * time.Second
	defaultRenewInterval     = 2 * time.Second
	defaultDrainTimeout      = 15 * time.Second
	defaultShutdownTimeout   = 15 * time.Second
	defaultMinBackoff        = 2 * time.Second
	defaultMaxBackoff        = 60 * time.Second
	defaultResetBackoffAfter = 60 * time.Second
	defaultClaimDelayStep    = 250 * time.Millisecond
	defaultClaimDelayMax     = 5 * time.Second

	// maxReleaseTimeout caps one Release call made on a fresh context after a
	// connection ends. A release that takes longer than the lease TTL is
	// pointless (the member expires anyway), so the effective timeout is
	// min(LeaseTTL, maxReleaseTimeout).
	maxReleaseTimeout = 5 * time.Second
)

// Target is one desired connection.
type Target struct {
	// Key is stable and unique; it is also the coordinator group. It must be
	// non-empty, at most 200 bytes, and must not contain '{' or '}'.
	Key string
	// Fingerprint is opaque; a change restarts the connection.
	Fingerprint string
	// Value is handed to Dial. The Manager never inspects or logs it.
	Value any
}

// Conn is one connection attempt.
type Conn interface {
	// Run connects and blocks until the connection ends. It calls ready once
	// the connection can receive; ready returns an error when the member
	// could not become READY (the attempt must then end). Run returns nil
	// when ctx ends, an error when the link dropped.
	Run(ctx context.Context, ready func(context.Context) error) error
}

// Config configures a Manager. Zero durations take the documented defaults.
type Config struct {
	// Name is non-empty; it prefixes log events ("<name>_claimed",
	// "<name>_ready", "<name>_draining", ...) and names the log component.
	Name string
	// List returns the desired set; called on each poll and on Kick. An error
	// keeps the current connections.
	List func(ctx context.Context) ([]Target, error)
	// Dial builds one connection attempt. An error backs off like a dropped
	// link.
	Dial func(Target) (Conn, error)
	// Coordinator is the ownership authority.
	Coordinator Coordinator

	PollInterval    time.Duration // default 10s
	RenewInterval   time.Duration // default 2s; must be under half the lease TTL
	DrainTimeout    time.Duration // default 15s (process handoff budget)
	ShutdownTimeout time.Duration // default 15s

	MinBackoff        time.Duration // default 2s
	MaxBackoff        time.Duration // default 60s
	ResetBackoffAfter time.Duration // default 60s

	// Claim delay (see the package doc): before each Claim a replica waits
	// ClaimDelayStep × (members it holds − least READY count reported by a
	// live peer), capped at ClaimDelayMax, plus a random offset in
	// [0, ClaimDelayStep). Defaults 250ms and 5s.
	ClaimDelayStep time.Duration
	ClaimDelayMax  time.Duration

	// Peers is the optional replica membership. When set, the Manager
	// heartbeats every sweep, uses peer loads for the claim delay, and sheds
	// READY connections above its fair share (see the package doc). Nil
	// disables shedding.
	Peers Peers

	Now    func() time.Time
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.PollInterval == 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.RenewInterval == 0 {
		c.RenewInterval = defaultRenewInterval
	}
	if c.DrainTimeout == 0 {
		c.DrainTimeout = defaultDrainTimeout
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = defaultShutdownTimeout
	}
	if c.MinBackoff == 0 {
		c.MinBackoff = defaultMinBackoff
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.ResetBackoffAfter == 0 {
		c.ResetBackoffAfter = defaultResetBackoffAfter
	}
	if c.ClaimDelayStep == 0 {
		c.ClaimDelayStep = defaultClaimDelayStep
	}
	if c.ClaimDelayMax == 0 {
		c.ClaimDelayMax = defaultClaimDelayMax
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: name is required", ErrConfig)
	}
	if c.List == nil {
		return fmt.Errorf("%w: list function is required", ErrConfig)
	}
	if c.Dial == nil {
		return fmt.Errorf("%w: dial function is required", ErrConfig)
	}
	if c.Coordinator == nil {
		return fmt.Errorf("%w: coordinator is required", ErrConfig)
	}
	if c.Coordinator.TargetReady() < 1 {
		return fmt.Errorf("%w: coordinator target_ready must be positive", ErrConfig)
	}
	ttl := c.Coordinator.LeaseTTL()
	if ttl <= 0 {
		return fmt.Errorf("%w: coordinator lease TTL must be positive", ErrConfig)
	}
	for _, d := range []struct {
		name  string
		value time.Duration
	}{
		{"poll_interval", c.PollInterval},
		{"renew_interval", c.RenewInterval},
		{"drain_timeout", c.DrainTimeout},
		{"shutdown_timeout", c.ShutdownTimeout},
		{"min_backoff", c.MinBackoff},
		{"max_backoff", c.MaxBackoff},
		{"reset_backoff_after", c.ResetBackoffAfter},
		{"claim_delay_step", c.ClaimDelayStep},
		{"claim_delay_max", c.ClaimDelayMax},
	} {
		if d.value <= 0 {
			return fmt.Errorf("%w: %s must be positive", ErrConfig, d.name)
		}
	}
	// At least two renewal attempts must fit in one lease, or a single missed
	// renewal fails the connection closed.
	if 2*c.RenewInterval >= ttl {
		return fmt.Errorf("%w: renew_interval %s must be under half the lease TTL %s", ErrConfig, c.RenewInterval, ttl)
	}
	if c.MinBackoff > c.MaxBackoff {
		return fmt.Errorf("%w: min_backoff exceeds max_backoff", ErrConfig)
	}
	return nil
}

// Manager keeps one contender goroutine per desired Target and lets the
// Coordinator decide which replica holds each connection.
//
// Lifecycle:
//
//	m, err := connmgr.New(cfg)
//	go m.Run(ctx)                        // returns when ctx is cancelled
//	...SIGTERM...
//	m.BeginShutdown()                    // DRAINING; keep consuming until handed off
//	m.WaitForHandoffs(m.DrainTimeout())
//	cancel()                             // hard stop for whatever remains
//	m.WaitWithTimeout(m.ShutdownTimeout())
type Manager struct {
	cfg    Config
	coord  Coordinator
	log    *slog.Logger
	nodeID string

	// kick requests an out-of-band sweep. Buffered(1) so a burst of requests
	// coalesces into one sweep instead of queueing.
	kick chan struct{}

	// connecting and ready count this replica's members by coordinator state.
	// DRAINING members are counted in neither.
	connecting atomic.Int64
	ready      atomic.Int64
	// minPeerHeld is the fewest READY connections another live replica
	// reported in the last sweep; 0 when unknown (no Peers, no other live
	// replica, or a membership error).
	minPeerHeld atomic.Int64

	running  atomic.Bool
	stopChan chan struct{}
	// left records that this node has left Peers. Only the Run goroutine
	// touches it.
	left bool

	mu sync.Mutex
	// entries keys each contender goroutine by Target.Key.
	entries map[string]*entry
	// gen is the source of the monotonic generation stored on each entry and
	// embedded in its lease token.
	gen     uint64
	wg      sync.WaitGroup
	stopped bool
	// shedding is the entry being handed over by rebalancing, until its
	// contender ends. At most one shed runs at a time.
	shedding *entry
	// cooldown is, per shed key, the earliest time this replica contends for
	// it again.
	cooldown map[string]time.Time
}

// entry is the per-key state held for each contender goroutine. The map holds
// the pointer, so the contender's deferred cleanup can tell its own entry
// apart from a successor that a fingerprint restart already swapped in.
type entry struct {
	key         string
	cancel      context.CancelFunc
	fingerprint string
	gen         uint64
	drain       chan struct{}
	drainOnce   sync.Once
	done        chan struct{}
	// notBefore delays the first claim (shed cooldown). Set before start.
	notBefore time.Time
	// ready mirrors whether the contender currently holds a READY member.
	ready atomic.Bool

	// Guarded by Manager.mu.
	shed      bool
	shedAt    time.Time
	stalled   bool
	handedOff bool
}

// requestDrain asks the contender to hand its connection over (or stop, when
// it holds no READY member). Shutdown and shedding share it; it is idempotent.
func (e *entry) requestDrain() { e.drainOnce.Do(func() { close(e.drain) }) }

// New validates cfg, applies defaults, and returns a Manager. No goroutine
// starts until Run.
func New(cfg Config) (*Manager, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Manager{
		cfg:      cfg,
		coord:    cfg.Coordinator,
		log:      cfg.Logger.With("component", cfg.Name),
		nodeID:   newNodeID(),
		kick:     make(chan struct{}, 1),
		stopChan: make(chan struct{}),
		entries:  make(map[string]*entry),
		cooldown: make(map[string]time.Time),
	}, nil
}

// NodeID is this process's identity; every lease token starts with it.
func (m *Manager) NodeID() string { return m.nodeID }

// Held reports the READY connections on this replica (DRAINING excluded).
func (m *Manager) Held() int { return int(m.ready.Load()) }

// DrainTimeout is the graceful handoff budget to pass to WaitForHandoffs. It
// is also the budget after which an unclaimed shed is reported as stalled.
func (m *Manager) DrainTimeout() time.Duration { return m.cfg.DrainTimeout }

// ShutdownTimeout is the deadline to pass to WaitWithTimeout.
func (m *Manager) ShutdownTimeout() time.Duration { return m.cfg.ShutdownTimeout }

// Kick requests an immediate sweep instead of waiting for the next
// PollInterval tick. Non-blocking and safe from any goroutine; a kick while a
// sweep request is already pending coalesces with it. Kicked sweeps never
// shed; only PollInterval ticks do.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run sweeps the desired set every PollInterval (and on Kick), starting a
// contender for new keys, restarting keys whose Fingerprint changed, stopping
// removed keys, and (with Peers) heartbeating and rebalancing. It returns when
// ctx is cancelled; call WaitWithTimeout afterwards. Run must be called at
// most once.
func (m *Manager) Run(ctx context.Context) {
	if !m.running.CompareAndSwap(false, true) {
		m.log.Error(m.cfg.Name+" manager already running", "event", m.event("run_twice"))
		return
	}
	defer close(m.stopChan)

	// First sweep immediately so a freshly started server does not wait a
	// full PollInterval before contending for its connections.
	m.sweep(ctx, false)

	t := time.NewTicker(m.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.cancelAll()
			m.leave()
			return
		case <-t.C:
			m.sweep(ctx, true)
		case <-m.kick:
			m.sweep(ctx, false)
		}
	}
}

// BeginShutdown starts graceful handoff of every connection without
// cancelling Run's context: READY connections enter DRAINING and keep
// consuming until replacement READY capacity exists elsewhere; contenders that
// hold nothing (or are still CONNECTING) stop. No new contender starts after
// this call, and the next sweep removes this node from Peers so the others
// stop counting it. It is idempotent.
func (m *Manager) BeginShutdown() {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	for _, e := range m.entries {
		e.requestDrain()
	}
	m.mu.Unlock()
	m.Kick()
}

// WaitForHandoffs waits for the contenders that existed when the call began to
// finish their graceful handoff. It does not wait for Run; callers use it
// between BeginShutdown and cancelling Run's context. A non-positive timeout
// waits without a deadline. It returns false on timeout.
func (m *Manager) WaitForHandoffs(timeout time.Duration) bool {
	m.mu.Lock()
	done := make([]<-chan struct{}, 0, len(m.entries))
	for _, e := range m.entries {
		done = append(done, e.done)
	}
	m.mu.Unlock()

	wait := make(chan struct{})
	go func() {
		for _, ch := range done {
			<-ch
		}
		close(wait)
	}()
	if timeout <= 0 {
		<-wait
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-wait:
		return true
	case <-timer.C:
		return false
	}
}

// WaitWithTimeout waits for Run to return and every contender goroutine to
// exit. Call it after cancelling Run's context. It returns false on timeout;
// unreleased members then expire after the lease TTL. A non-positive timeout
// waits without a deadline.
func (m *Manager) WaitWithTimeout(timeout time.Duration) bool {
	if timeout <= 0 {
		m.wait()
		return true
	}
	done := make(chan struct{})
	go func() {
		m.wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// wait first waits for Run to return and only then joins the WaitGroup. Run
// is the sole caller of wg.Add, and Add concurrent with Wait is a data race;
// once Run has returned no further Add can happen.
func (m *Manager) wait() {
	<-m.stopChan
	m.wg.Wait()
}

// sweep reconciles running contenders with the desired set. A List error keeps
// every current connection: a transient store failure must not tear down the
// whole fleet. tick marks a PollInterval sweep, the only kind that may shed.
func (m *Manager) sweep(ctx context.Context, tick bool) {
	loads, membershipKnown := m.heartbeat(ctx)

	targets, err := m.cfg.List(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.log.Warn(m.cfg.Name+" list targets failed; keeping current connections",
			"event", m.event("list_failed"),
			"node_id", m.nodeID,
			"held", m.Held(),
			"error", err,
		)
		return
	}

	desired := make(map[string]Target, len(targets))
	order := make([]string, 0, len(targets))
	for _, target := range targets {
		if err := validateGroup(target.Key); err != nil {
			m.log.Warn(m.cfg.Name+" target skipped: invalid key",
				"event", m.event("invalid_target"),
				"node_id", m.nodeID,
				"key", truncateKey(target.Key),
				"error", err,
			)
			continue
		}
		if _, dup := desired[target.Key]; dup {
			m.log.Warn(m.cfg.Name+" target skipped: duplicate key",
				"event", m.event("duplicate_target"),
				"node_id", m.nodeID,
				"key", target.Key,
			)
			continue
		}
		desired[target.Key] = target
		order = append(order, target.Key)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, e := range m.entries {
		target, ok := desired[key]
		switch {
		case !ok:
			// The contender exits on its next boundary, releases its member,
			// and returns.
			m.log.Info(m.cfg.Name+" target removed; stopping connection",
				"event", m.event("removed"),
				"key", key,
				"node_id", m.nodeID,
			)
			e.cancel()
			delete(m.entries, key)
		case e.fingerprint != target.Fingerprint:
			// Dropped from the map inline so the start loop below builds the
			// replacement in this same sweep.
			m.log.Info(m.cfg.Name+" target changed; restarting connection",
				"event", m.event("restart"),
				"key", key,
				"node_id", m.nodeID,
			)
			e.cancel()
			delete(m.entries, key)
		}
	}
	for key := range m.cooldown {
		if _, ok := desired[key]; !ok {
			delete(m.cooldown, key)
		}
	}
	if m.stopped {
		return
	}
	for _, key := range order {
		if _, exists := m.entries[key]; exists {
			continue
		}
		m.startLocked(ctx, desired[key])
	}
	if tick && membershipKnown {
		m.rebalanceLocked(len(desired), loads)
	}
}

// startLocked starts one contender. Callers hold m.mu and have checked that
// the Manager is not stopped.
func (m *Manager) startLocked(parent context.Context, target Target) {
	ctx, cancel := context.WithCancel(parent)
	m.gen++
	e := &entry{
		key:         target.Key,
		cancel:      cancel,
		fingerprint: target.Fingerprint,
		gen:         m.gen,
		drain:       make(chan struct{}),
		done:        make(chan struct{}),
	}
	if until, ok := m.cooldown[target.Key]; ok {
		delete(m.cooldown, target.Key)
		if until.After(m.cfg.Now()) {
			e.notBefore = until
		}
	}
	m.entries[target.Key] = e
	m.wg.Add(1)
	go m.supervise(ctx, target, e)
}

// finishEntry is a contender's exit bookkeeping: drop its map entry (unless a
// successor already replaced it) and close out a shed.
func (m *Manager) finishEntry(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.entries[e.key]; ok && cur == e {
		delete(m.entries, e.key)
	}
	if !e.shed {
		return
	}
	if m.shedding == e {
		m.shedding = nil
	}
	if e.handedOff {
		// The replacement is READY elsewhere, so a new contender here cannot
		// win the key back while it lives; the cooldown also keeps this
		// replica from reclaiming it right after a quick replacement failure.
		m.cooldown[e.key] = m.cfg.Now().Add(m.cfg.PollInterval)
	} else {
		// The shed stream ended before anyone took it over (it dropped while
		// DRAINING): contend again now, not at the next PollInterval.
		m.Kick()
	}
	m.log.Info(m.cfg.Name+" shed finished",
		"event", m.event("shed_finished"),
		"key", e.key,
		"node_id", m.nodeID,
		"handed_off", e.handedOff,
		"duration_ms", m.cfg.Now().Sub(e.shedAt).Milliseconds(),
	)
}

// rebalanceLocked hands one READY connection to another replica when this
// replica holds more than its fair share and some other replica holds less.
// It runs only on PollInterval ticks, so at most one shed starts per
// PollInterval, and never while an earlier shed's connection is still
// DRAINING. Callers hold m.mu.
func (m *Manager) rebalanceLocked(desired int, loads []PeerLoad) {
	now := m.cfg.Now()
	if s := m.shedding; s != nil {
		if !s.stalled && now.Sub(s.shedAt) >= m.cfg.DrainTimeout {
			// Abandon waiting, never the stream: the member keeps consuming as
			// DRAINING until some replica's replacement is READY.
			s.stalled = true
			m.log.Warn(m.cfg.Name+" shed not taken over within the drain budget; connection keeps consuming",
				"event", m.event("shed_stalled"),
				"key", s.key,
				"node_id", m.nodeID,
				"waited_ms", now.Sub(s.shedAt).Milliseconds(),
			)
		}
		return
	}
	alive := len(loads)
	if alive < 2 || desired <= 0 {
		return
	}
	fair := (desired + alive - 1) / alive
	held := m.Held()
	if held <= fair {
		return
	}
	// Only shed when some replica can take the key without itself exceeding
	// fair. With accurate reports this always holds when held > fair; it
	// stops ping-pong while a vanished replica still counts as alive.
	receiver := false
	for _, p := range loads {
		if p.Node != m.nodeID && p.Held < fair && p.Held <= held-2 {
			receiver = true
			break
		}
	}
	if !receiver {
		return
	}
	candidates := make([]*entry, 0, held)
	for _, e := range m.entries {
		if e.ready.Load() && !e.shed {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return
	}
	e := candidates[mathrand.IntN(len(candidates))]
	e.shed = true
	e.shedAt = now
	m.shedding = e
	m.log.Info(m.cfg.Name+" shedding one connection to rebalance",
		"event", m.event("shed"),
		"key", e.key,
		"node_id", m.nodeID,
		"held", held,
		"fair", fair,
		"alive", alive,
		"desired", desired,
	)
	e.requestDrain()
}

// heartbeat reports this replica to Peers and returns the live membership
// (including this node). It returns false when membership is unknown: no
// Peers, shutting down, or a membership error. It also refreshes the least
// peer load used by the claim delay.
func (m *Manager) heartbeat(ctx context.Context) ([]PeerLoad, bool) {
	if m.cfg.Peers == nil {
		return nil, false
	}
	m.mu.Lock()
	stopped := m.stopped
	m.mu.Unlock()
	if stopped {
		m.leave()
		return nil, false
	}
	opCtx, cancel := context.WithTimeout(ctx, m.peersTimeout())
	defer cancel()
	held := m.Held()
	if err := m.cfg.Peers.Heartbeat(opCtx, m.nodeID, held, m.membershipTTL()); err != nil {
		m.membershipFailed(ctx, "heartbeat", err)
		return nil, false
	}
	loads, err := m.cfg.Peers.Alive(opCtx)
	if err != nil {
		m.membershipFailed(ctx, "alive", err)
		return nil, false
	}
	self := false
	least := -1
	for _, p := range loads {
		if p.Node == m.nodeID {
			self = true
			continue
		}
		if least < 0 || p.Held < least {
			least = p.Held
		}
	}
	if !self {
		loads = append(loads, PeerLoad{Node: m.nodeID, Held: held})
	}
	if least < 0 {
		least = 0
	}
	m.minPeerHeld.Store(int64(least))
	return loads, true
}

func (m *Manager) membershipFailed(ctx context.Context, op string, err error) {
	m.minPeerHeld.Store(0)
	if ctx.Err() != nil {
		return
	}
	m.log.Warn(m.cfg.Name+" peer membership "+op+" failed; not rebalancing this sweep",
		"event", m.event("peers_failed"),
		"node_id", m.nodeID,
		"error_class", errorClass(err),
		"error", err,
	)
}

// leave removes this node from Peers once. Only the Run goroutine calls it.
func (m *Manager) leave() {
	if m.cfg.Peers == nil || m.left {
		return
	}
	m.left = true
	ctx, cancel := context.WithTimeout(context.Background(), m.peersTimeout())
	defer cancel()
	if err := m.cfg.Peers.Leave(ctx, m.nodeID); err != nil {
		m.log.Warn(m.cfg.Name+" leaving peer membership failed; it expires with the heartbeat TTL",
			"event", m.event("peers_leave_failed"),
			"node_id", m.nodeID,
			"error", err,
		)
		return
	}
	m.log.Info(m.cfg.Name+" left peer membership", "event", m.event("peers_left"), "node_id", m.nodeID)
}

// membershipTTL keeps a replica alive across two missed sweeps.
func (m *Manager) membershipTTL() time.Duration { return 3 * m.cfg.PollInterval }

func (m *Manager) peersTimeout() time.Duration {
	if m.cfg.PollInterval < maxReleaseTimeout {
		return m.cfg.PollInterval
	}
	return maxReleaseTimeout
}

func (m *Manager) cancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	for key, e := range m.entries {
		e.cancel()
		delete(m.entries, key)
	}
}

func (m *Manager) event(suffix string) string { return m.cfg.Name + "_" + suffix }

func truncateKey(key string) string {
	if len(key) <= maxGroupLen {
		return key
	}
	return key[:maxGroupLen]
}
