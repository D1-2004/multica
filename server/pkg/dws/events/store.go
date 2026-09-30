package events

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Store is the listener state every replica shares. Keep it in Redis (see
// the redisstore package) or a database; MemoryStore is for tests and a
// single process. Nothing about a connection lives only in process memory.
type Store interface {
	// Lease claims the connection for identity, or renews it when holder
	// already owns it, until ttl passes. At most one holder owns an identity.
	Lease(ctx context.Context, identity, holder string, ttl time.Duration) (bool, error)
	// Release gives the lease up if holder owns it.
	Release(ctx context.Context, identity, holder string) error
	// Handled reports whether an event id was already handled; MarkHandled
	// records it after Handle succeeded. Marking only afterwards means a
	// crash in between re-handles the redelivery instead of losing it, so
	// Handle must be idempotent on Event.ID (a unique key in the host's DB).
	Handled(ctx context.Context, identity, eventID string) (bool, error)
	MarkHandled(ctx context.Context, identity, eventID string, ttl time.Duration) error
	// Subscriptions are the ones this SDK created for identity; they outlive
	// connections and are cancelled when the configuration drops them.
	Subscriptions(ctx context.Context, identity string) ([]dws.Subscription, error)
	SetSubscriptions(ctx context.Context, identity string, subs []dws.Subscription) error
	// SubscribedIdentities lists identities with recorded subscriptions, so
	// ones dropped from the configuration are cleaned up even after a restart.
	SubscribedIdentities(ctx context.Context) ([]string, error)
	// SetStatus publishes connection health for the product page and alerts;
	// a listener that restarts or moves to another replica resumes its
	// alert state from Status.
	SetStatus(ctx context.Context, st Status) error
	Status(ctx context.Context, identity string) (Status, bool, error)
	Statuses(ctx context.Context) ([]Status, error)
}

// State is where a listener is in its lifecycle.
type State string

const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateStopped      State = "stopped"
)

// Status is one identity's connection health.
type Status struct {
	Identity        string    `json:"identity"`
	Holder          string    `json:"holder,omitempty"`
	State           State     `json:"state"`
	Since           time.Time `json:"since"`
	LastConnectedAt time.Time `json:"lastConnectedAt,omitempty"`
	LastEventAt     time.Time `json:"lastEventAt,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	// Failures counts consecutive failed connection attempts.
	Failures      int `json:"failures,omitempty"`
	Subscriptions int `json:"subscriptions"`
	// DownSince is when the current outage began; zero once a connection
	// proved healthy (see Options.StableAfter). A connected state with a
	// DownSince is a connection still proving itself.
	DownSince time.Time `json:"downSince,omitempty"`
	// Alerting: a "down" alert went out and no "recovered" yet.
	Alerting bool `json:"alerting,omitempty"`
	// HandleFailing: the host failed the last event it was given and has not
	// handled one since; until it does, a stable connection is no recovery.
	HandleFailing bool      `json:"handleFailing,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// MemoryStore is a Store in process memory.
type MemoryStore struct {
	Now func() time.Time

	mu       sync.Mutex
	leases   map[string]lease
	seen     map[string]time.Time
	marks    int
	subs     map[string][]dws.Subscription
	statuses map[string]Status
}

type lease struct {
	holder string
	until  time.Time
}

func (m *MemoryStore) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MemoryStore) init() {
	if m.leases == nil {
		m.leases, m.seen = map[string]lease{}, map[string]time.Time{}
		m.subs, m.statuses = map[string][]dws.Subscription{}, map[string]Status{}
	}
}

func (m *MemoryStore) Lease(_ context.Context, identity, holder string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	now := m.now()
	if l, ok := m.leases[identity]; ok && l.holder != holder && now.Before(l.until) {
		return false, nil
	}
	m.leases[identity] = lease{holder: holder, until: now.Add(ttl)}
	return true, nil
}

func (m *MemoryStore) Release(_ context.Context, identity, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if m.leases[identity].holder == holder {
		delete(m.leases, identity)
	}
	return nil
}

func (m *MemoryStore) Handled(_ context.Context, identity, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	until, ok := m.seen[identity+"\x00"+eventID]
	return ok && m.now().Before(until), nil
}

func (m *MemoryStore) MarkHandled(_ context.Context, identity, eventID string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	now := m.now()
	m.seen[identity+"\x00"+eventID] = now.Add(ttl)
	// Sweep expired ids now and then so memory follows the TTL.
	if m.marks++; m.marks%1024 == 0 {
		for k, until := range m.seen {
			if !now.Before(until) {
				delete(m.seen, k)
			}
		}
	}
	return nil
}

func (m *MemoryStore) Subscriptions(_ context.Context, identity string) ([]dws.Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return append([]dws.Subscription(nil), m.subs[identity]...), nil
}

func (m *MemoryStore) SetSubscriptions(_ context.Context, identity string, subs []dws.Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	if len(subs) == 0 {
		delete(m.subs, identity)
		return nil
	}
	m.subs[identity] = append([]dws.Subscription(nil), subs...)
	return nil
}

func (m *MemoryStore) SubscribedIdentities(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	ids := make([]string, 0, len(m.subs))
	for id := range m.subs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (m *MemoryStore) SetStatus(_ context.Context, st Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	m.statuses[st.Identity] = st
	return nil
}

func (m *MemoryStore) Status(_ context.Context, identity string) (Status, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	st, ok := m.statuses[identity]
	return st, ok, nil
}

func (m *MemoryStore) Statuses(context.Context) ([]Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	out := make([]Status, 0, len(m.statuses))
	for _, st := range m.statuses {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out, nil
}
