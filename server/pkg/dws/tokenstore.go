package dws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// TokenStore shares identities' tokens between processes, so replicas
// behind a load balancer exchange an identity once and then use, and
// refresh, one token lineage: any replica can serve any identity.
//
// The refresh token rotates, and two processes refreshing the same token
// end the session. Minting and refreshing therefore run under Lock, and
// re-read the store first, adopting a token another process has just
// written. Implementations hold credentials: they must keep them sealed at
// rest (see redisstore.TokenStore).
type TokenStore interface {
	// Load returns key's token; ok is false when there is none.
	Load(ctx context.Context, key string) (tok Token, ok bool, err error)
	// Save stores tok as key's token.
	Save(ctx context.Context, key string, tok Token) error
	// Delete drops key's token, after DingTalk refused its refresh token.
	Delete(ctx context.Context, key string) error
	// Lock waits until this process holds key's lock or ctx ends. The lock
	// frees itself after ttl if its holder dies; unlock releases it and
	// never releases a lock another process took since.
	Lock(ctx context.Context, key string, ttl time.Duration) (unlock func(), err error)
}

// sharedLockTTL bounds a mint or a refresh held under the store's lock;
// the work itself is cut off at sharedWorkTimeout, well inside it, so the
// lock never expires under a live holder (an HTTP call may take 30s, a
// mint is an identity call, a redeem and an exchange).
const (
	sharedLockTTL     = 90 * time.Second
	sharedWorkTimeout = 60 * time.Second
)

// sharedToken ties a Client to its identity's stored token.
type sharedToken struct {
	store TokenStore
	key   string
}

// usable reports a token worth adopting: it works now, or can be renewed.
func (c *Client) usable(t Token) bool {
	return t.AccessToken != "" && (t.RefreshToken != "" || t.ExpiresAt.IsZero() || c.cfg.now().Before(t.ExpiresAt))
}

func (c *Client) adopt(t Token) {
	c.token = t
	c.current.Store(&t)
}

// renewShared renews the token of a Client whose identity is shared, under
// the store's lock: a newer token another process stored is adopted, and
// only when there is none does this process refresh, then store the
// result. Called with c.mu held, when the token is near expiry or
// rejected is the token the gateway refused.
func (c *Client) renewShared(ctx context.Context, rejected string, stillValid bool) (string, error) {
	s := c.shared
	unlock, err := s.store.Lock(ctx, s.key, sharedLockTTL)
	if err != nil {
		if ctx.Err() != nil {
			if stillValid {
				return c.token.AccessToken, nil
			}
			return "", fmt.Errorf("dws: waiting for the shared token: %w", err)
		}
		// The store is a cache: without it this process renews its own
		// lineage, unshared. A lineage that forks this way heals when the
		// other copy's refresh is refused and it mints anew.
		return c.renewOwn(ctx, stillValid)
	}
	defer unlock()
	work, cancel := context.WithTimeout(ctx, sharedWorkTimeout)
	defer cancel()
	stored, ok, err := s.store.Load(work, s.key)
	if err != nil {
		return c.renewOwn(work, stillValid)
	}
	if ok && stored.AccessToken != c.token.AccessToken && stored.AccessToken != rejected && c.usable(stored) {
		c.adopt(stored)
		if stored.ExpiresAt.IsZero() || c.cfg.now().Add(c.cfg.skew()).Before(stored.ExpiresAt) {
			return stored.AccessToken, nil
		}
		// Adopted, but due for a refresh itself: fall through with it.
		stillValid = c.cfg.now().Before(stored.ExpiresAt)
	}
	// Only the lineage this Client holds may be dropped from the store:
	// another process may have stored a newer one.
	dropOurs := func() {
		if ok && stored.RefreshToken == c.token.RefreshToken && stored.AccessToken == c.token.AccessToken {
			_ = s.store.Delete(work, s.key)
		}
	}
	if c.token.RefreshToken == "" {
		if stillValid {
			return c.token.AccessToken, nil
		}
		c.expired.Store(true)
		dropOurs()
		return "", ErrSessionExpired
	}
	next, err := refresh(work, c.cfg, c.token)
	switch {
	case err == nil:
		c.adopt(next)
		// A failed save leaves the other processes the token this refresh
		// spent: their refresh is refused and they mint anew. The call
		// itself goes on with the fresh token.
		_ = s.store.Save(work, s.key, next)
		return next.AccessToken, nil
	case errors.Is(err, errRefreshRejected):
		dropOurs()
		c.expired.Store(true)
		return "", fmt.Errorf("%w: %v", ErrSessionExpired, err)
	case stillValid:
		return c.token.AccessToken, nil
	default:
		return "", err
	}
}

// MemoryTokenStore is a TokenStore for one process and for tests.
type MemoryTokenStore struct {
	mu     sync.Mutex
	tokens map[string]Token
	locks  map[string]chan struct{}
}

var _ TokenStore = (*MemoryTokenStore)(nil)

func (m *MemoryTokenStore) Load(_ context.Context, key string) (Token, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[key]
	return t, ok, nil
}

func (m *MemoryTokenStore) Save(_ context.Context, key string, tok Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tokens == nil {
		m.tokens = map[string]Token{}
	}
	m.tokens[key] = tok
	return nil
}

func (m *MemoryTokenStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, key)
	return nil
}

func (m *MemoryTokenStore) Lock(ctx context.Context, key string, _ time.Duration) (func(), error) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = map[string]chan struct{}{}
	}
	l, ok := m.locks[key]
	if !ok {
		l = make(chan struct{}, 1)
		m.locks[key] = l
	}
	m.mu.Unlock()
	select {
	case l <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
