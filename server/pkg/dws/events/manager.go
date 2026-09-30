package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Spec is what one identity should listen to, as configured in the product.
type Spec struct {
	Identity      string
	Subscriptions []dws.SubscriptionSpec
}

func (s Spec) fingerprint() string {
	fps := make([]string, 0, len(s.Subscriptions))
	for _, sub := range s.Subscriptions {
		fps = append(fps, sub.Fingerprint())
	}
	sort.Strings(fps)
	sum := sha256.Sum256([]byte(strings.Join(fps, ",")))
	return hex.EncodeToString(sum[:8])
}

// Manager runs a Listener for every configured identity across replicas.
// Each tick it reads the configuration, holds (or takes over) a lease per
// identity, starts listeners it owns, restarts one whose configuration
// changed, and cancels the subscriptions of identities that were removed.
// After a restart the configuration and the Store are all it needs: leases
// of a dead replica expire and another replica picks the identities up.
type Manager struct {
	Store Store
	// Specs returns the configuration; an error keeps the current set.
	Specs func(ctx context.Context) ([]Spec, error)
	// Client returns an identity's Client, e.g. dws.Pool.Client.
	Client func(ctx context.Context, identity string) (*dws.Client, error)
	// Handle persists one event; see Listener.Handle.
	Handle func(ctx context.Context, identity string, ev Event) error
	// LeaseTTL is how long a lease outlives its last renewal (30s); a
	// crashed replica's identities move after that.
	LeaseTTL time.Duration
	// Tick is how often configuration and leases are reconciled (10s).
	Tick time.Duration
	Options

	mu       sync.Mutex
	running  map[string]*runningListener
	retiring map[string]*retirement
	retires  sync.WaitGroup
}

// retirement tracks the cleanup of an identity the configuration dropped. It
// runs beside the reconcile loop, so a slow mint or cancel never delays the
// lease renewals of healthy listeners, and backs off while it fails.
type retirement struct {
	running bool
	next    time.Time
	backoff time.Duration
}

// maxRetireBackoff caps the retry interval of a failing retirement.
const maxRetireBackoff = 10 * time.Minute

type runningListener struct {
	fingerprint string
	cancel      context.CancelFunc
	done        chan struct{}
	// watchdog stops the listener when its lease has not been renewed for
	// most of the TTL, whatever holds up the reconcile loop (a slow Specs,
	// an unreachable Store): past the TTL another replica may connect.
	watchdog *time.Timer
}

// leaseGuard is how long a listener runs without a renewed lease: short of
// the TTL, so it is down before another replica can take the lease.
func leaseGuard(ttl time.Duration) time.Duration { return ttl * 4 / 5 }

// Run blocks until ctx is done, then stops every listener and releases its
// leases (subscriptions stay, so a restart resumes them).
func (m *Manager) Run(ctx context.Context) error {
	if m.Store == nil || m.Specs == nil || m.Client == nil || m.Handle == nil {
		return errors.New("events: manager needs Store, Specs, Client and Handle")
	}
	if m.Holder == "" {
		host, _ := os.Hostname()
		m.Holder = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	m.mu.Lock()
	m.running = map[string]*runningListener{}
	m.retiring = map[string]*retirement{}
	m.mu.Unlock()
	tick := orDefault(m.Tick, 10*time.Second)
	for {
		m.reconcile(ctx)
		select {
		case <-ctx.Done():
			m.stopAll(context.WithoutCancel(ctx))
			return nil
		case <-time.After(tick):
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	specs, err := m.Specs(ctx)
	desired := map[string]Spec{}
	for _, s := range specs {
		if s.Identity != "" && len(s.Subscriptions) > 0 {
			desired[s.Identity] = s
		}
	}
	ttl := orDefault(m.LeaseTTL, 30*time.Second)
	m.mu.Lock()
	running := make(map[string]*runningListener, len(m.running))
	for id, r := range m.running {
		running[id] = r
	}
	m.mu.Unlock()

	for id, r := range running {
		select {
		case <-r.done:
			m.stop(id, r) // its watchdog fired; restarted below if still ours
			continue
		default:
		}
		spec, want := desired[id]
		if err == nil && !want {
			m.stop(id, r) // its subscriptions are retired below
			continue
		}
		// The lease runs from when the Store applied it, which is no earlier
		// than this.
		asked := time.Now()
		ok, lerr := m.Store.Lease(ctx, id, m.Holder, ttl)
		switch {
		case lerr == nil && ok:
			if guard := leaseGuard(ttl) - time.Since(asked); guard > 0 {
				r.watchdog.Reset(guard)
			} else {
				m.stop(id, r) // renewed too late to trust; restarted below
				continue
			}
		case lerr == nil:
			m.stop(id, r) // another replica took over
			continue
		}
		// A renewal error leaves the watchdog running down.
		if err == nil && spec.fingerprint() != r.fingerprint {
			m.stop(id, r) // restarted below with the new configuration
		}
	}
	if err != nil {
		return
	}
	// Identities dropped from the configuration, possibly while no replica
	// ran: cancel their subscriptions under the lease, off this loop.
	for id := range m.retirable(ctx) {
		if _, want := desired[id]; !want {
			m.startRetire(ctx, id, ttl, tickOf(m))
		}
	}
	for id, spec := range desired {
		m.mu.Lock()
		_, busy := m.running[id]
		if r := m.retiring[id]; r != nil {
			if r.running {
				busy = true // a retirement still running finishes before a restart
			} else {
				delete(m.retiring, id) // configured again: stop retrying
			}
		}
		m.mu.Unlock()
		if busy {
			continue
		}
		asked := time.Now()
		if ok, lerr := m.Store.Lease(ctx, id, m.Holder, ttl); lerr == nil && ok {
			if guard := leaseGuard(ttl) - time.Since(asked); guard > 0 {
				m.start(ctx, spec, guard)
			}
		}
	}
}

func tickOf(m *Manager) time.Duration { return orDefault(m.Tick, 10*time.Second) }

// retirable lists identities that may need retiring: those with recorded
// subscriptions, and those whose status is not a finished retirement (still
// alerting, or not stopped), which covers an identity that never got a
// subscription recorded, e.g. one whose mint kept failing.
func (m *Manager) retirable(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if ids, err := m.Store.SubscribedIdentities(ctx); err == nil {
		for _, id := range ids {
			out[id] = true
		}
	}
	if sts, err := m.Store.Statuses(ctx); err == nil {
		for _, st := range sts {
			if st.Alerting || st.State != StateStopped {
				out[st.Identity] = true
			}
		}
	}
	return out
}

// startRetire starts retiring id unless a retirement of it is running or
// backing off.
func (m *Manager) startRetire(ctx context.Context, id string, ttl, tick time.Duration) {
	m.mu.Lock()
	r := m.retiring[id]
	if r == nil {
		r = &retirement{}
		m.retiring[id] = r
	}
	if r.running || time.Now().Before(r.next) {
		m.mu.Unlock()
		return
	}
	r.running = true
	m.mu.Unlock()
	m.retires.Add(1)
	go func() {
		defer m.retires.Done()
		// Bounded by the lease it runs under.
		rctx, cancel := context.WithTimeout(ctx, leaseGuard(ttl))
		defer cancel()
		done := false
		if ok, lerr := m.Store.Lease(rctx, id, m.Holder, ttl); lerr == nil && ok {
			done = m.retire(rctx, id)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if done {
			delete(m.retiring, id)
			return
		}
		r.running = false
		r.backoff = min(max(2*r.backoff, tick), maxRetireBackoff)
		r.next = time.Now().Add(r.backoff)
	}()
}

// start runs a listener whose lease is good for guard more.
func (m *Manager) start(ctx context.Context, spec Spec, guard time.Duration) {
	lctx, cancel := context.WithCancel(ctx)
	r := &runningListener{fingerprint: spec.fingerprint(), cancel: cancel, done: make(chan struct{}),
		watchdog: time.AfterFunc(guard, cancel)}
	m.mu.Lock()
	m.running[spec.Identity] = r
	m.mu.Unlock()
	id := spec.Identity
	l := &Listener{
		Identity:      id,
		Client:        func(ctx context.Context) (*dws.Client, error) { return m.Client(ctx, id) },
		Subscriptions: spec.Subscriptions,
		Handle:        func(ctx context.Context, ev Event) error { return m.Handle(ctx, id, ev) },
		Store:         m.Store,
		Options:       m.Options,
	}
	go func() {
		defer close(r.done)
		_ = l.Run(lctx)
	}()
}

func (m *Manager) stop(id string, r *runningListener) {
	r.watchdog.Stop()
	r.cancel()
	<-r.done
	m.mu.Lock()
	if m.running[id] == r {
		delete(m.running, id)
	}
	m.mu.Unlock()
}

// retire cancels the subscriptions of an identity the configuration dropped
// and reports whether none are left. It then releases the lease and records
// the identity stopped, closing an open alert with "retired".
func (m *Manager) retire(ctx context.Context, id string) bool {
	subs, err := m.Store.Subscriptions(ctx, id)
	if err != nil {
		return false
	}
	if len(subs) > 0 && !m.cancelAll(ctx, id, subs) {
		return false
	}
	// The status must end stopped and not alerting, or the alert would stay
	// open for good: a failure here is retried like a failed cancel. A retry
	// may repeat the "retired" alert; the host dedupes it.
	prev, ok, err := m.Store.Status(ctx, id)
	if err != nil {
		return false
	}
	if ok && prev.Alerting && m.Alert != nil {
		m.Alert(ctx, Alert{Identity: id, Kind: "retired", Since: prev.DownSince})
	}
	now := m.now()
	if err := m.Store.SetStatus(ctx, Status{Identity: id, Holder: m.Holder, State: StateStopped, Since: now, UpdatedAt: now}); err != nil {
		return false
	}
	_ = m.Store.Release(ctx, id, m.Holder)
	return true
}

// cancelAll cancels an identity's recorded subscriptions and reports whether
// none are left on record.
func (m *Manager) cancelAll(ctx context.Context, id string, subs []dws.Subscription) bool {
	client, err := m.Client(ctx, id)
	if err != nil {
		return false // retried after a backoff
	}
	var left []dws.Subscription
	for _, sub := range subs {
		id := sub.ID
		if id == "" && sub.Spec != nil {
			// Pending from a crash: subscribing the spec again returns its ID.
			resolved, err := client.Events.Subscribe(ctx, *sub.Spec)
			if err != nil {
				left = append(left, sub)
				continue
			}
			id = resolved.ID
		}
		if id == "" {
			continue
		}
		if err := client.Events.Cancel(ctx, id); err != nil {
			left = append(left, sub)
		}
	}
	return m.Store.SetSubscriptions(ctx, id, left) == nil && len(left) == 0
}

func (m *Manager) stopAll(ctx context.Context) {
	m.mu.Lock()
	running := m.running
	m.running = map[string]*runningListener{}
	m.mu.Unlock()
	for id, r := range running {
		r.watchdog.Stop()
		r.cancel()
		<-r.done
		_ = m.Store.Release(ctx, id, m.Holder)
	}
	m.retires.Wait() // their context is done, so they end promptly
}
