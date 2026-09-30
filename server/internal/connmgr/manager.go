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
//   - Before each Claim a replica waits in proportion to the connections it
//     already holds, so replicas holding fewer win free members first and
//     connections spread after restarts and rolling deploys.
//
// The package is a generalized port of the robot connector's coordinated
// stream mode (internal/integrations/channel/engine). It has no knowledge of
// channels, installations, or credentials.
package connmgr

import (
	"context"
	"fmt"
	"log/slog"
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

	// Balancing: before each Claim a replica waits ClaimDelayStep × (members
	// it holds), capped at ClaimDelayMax, plus a random offset in
	// [0, ClaimDelayStep) that breaks ties between replicas that start
	// together. Defaults 250ms and 5s.
	ClaimDelayStep time.Duration
	ClaimDelayMax  time.Duration

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

	running  atomic.Bool
	stopChan chan struct{}

	mu sync.Mutex
	// entries keys each contender goroutine by Target.Key.
	entries map[string]entry
	// gen is the source of the monotonic generation stored on each entry and
	// embedded in its lease token.
	gen     uint64
	wg      sync.WaitGroup
	stopped bool
}

// entry is the per-key state held for each contender goroutine. gen lets the
// goroutine's deferred cleanup tell its own entry apart from a successor that
// a fingerprint restart already swapped in.
type entry struct {
	cancel      context.CancelFunc
	fingerprint string
	gen         uint64
	drain       chan struct{}
	done        chan struct{}
}

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
		entries:  make(map[string]entry),
	}, nil
}

// NodeID is this process's identity; every lease token starts with it.
func (m *Manager) NodeID() string { return m.nodeID }

// Held reports the READY connections on this replica.
func (m *Manager) Held() int { return int(m.ready.Load()) }

// DrainTimeout is the graceful handoff budget to pass to WaitForHandoffs.
func (m *Manager) DrainTimeout() time.Duration { return m.cfg.DrainTimeout }

// ShutdownTimeout is the deadline to pass to WaitWithTimeout.
func (m *Manager) ShutdownTimeout() time.Duration { return m.cfg.ShutdownTimeout }

// Kick requests an immediate sweep instead of waiting for the next
// PollInterval tick. Non-blocking and safe from any goroutine; a kick while a
// sweep request is already pending coalesces with it.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run sweeps the desired set every PollInterval (and on Kick), starting a
// contender for new keys, restarting keys whose Fingerprint changed, and
// stopping removed keys. It returns when ctx is cancelled; call
// WaitWithTimeout afterwards. Run must be called at most once.
func (m *Manager) Run(ctx context.Context) {
	if !m.running.CompareAndSwap(false, true) {
		m.log.Error(m.cfg.Name+" manager already running", "event", m.event("run_twice"))
		return
	}
	defer close(m.stopChan)

	// First sweep immediately so a freshly started server does not wait a
	// full PollInterval before contending for its connections.
	m.sweep(ctx)

	t := time.NewTicker(m.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.cancelAll()
			return
		case <-t.C:
			m.sweep(ctx)
		case <-m.kick:
			m.sweep(ctx)
		}
	}
}

// BeginShutdown starts graceful handoff of every connection without
// cancelling Run's context: READY connections enter DRAINING and keep
// consuming until replacement READY capacity exists elsewhere; contenders that
// hold nothing (or are still CONNECTING) stop. No new contender starts after
// this call. It is idempotent.
func (m *Manager) BeginShutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	for _, e := range m.entries {
		close(e.drain)
	}
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
// whole fleet.
func (m *Manager) sweep(ctx context.Context) {
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
	if m.stopped {
		return
	}
	for _, key := range order {
		if _, exists := m.entries[key]; exists {
			continue
		}
		m.startLocked(ctx, desired[key])
	}
}

// startLocked starts one contender. Callers hold m.mu and have checked that
// the Manager is not stopped.
func (m *Manager) startLocked(parent context.Context, target Target) {
	ctx, cancel := context.WithCancel(parent)
	m.gen++
	e := entry{
		cancel:      cancel,
		fingerprint: target.Fingerprint,
		gen:         m.gen,
		drain:       make(chan struct{}),
		done:        make(chan struct{}),
	}
	m.entries[target.Key] = e
	m.wg.Add(1)
	go m.supervise(ctx, target, e.gen, e.drain, e.done)
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
