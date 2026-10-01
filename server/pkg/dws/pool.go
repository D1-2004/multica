package dws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrMintFailed: Pool.Mint could not produce an auth code.
var ErrMintFailed = errors.New("auth code mint failed")

// StageMint: Pool.Mint failed; no code exists yet.
const StageMint InitStage = "mint"

// Pool keeps one Client per identity for a backend serving many agents. The
// first use of a key, and the first use after its session expired, creates
// a Client from a freshly minted auth code. Failures are returned, never
// cached: the next call mints again.
//
// Without a Store the pool is per process: every replica holds its own
// clients, each with its own token lineage. With a Store, replicas share
// one token per identity: a Client is built from the stored token when one
// exists, a code is minted only when none does, and minting and refreshing
// run under the store's lock (see TokenStore).
type Pool struct {
	Config Config
	// Mint returns a fresh single-use auth code for key, e.g. an HSF
	// createAgentIdentityContext followed by AgentIdentity.Redeem.
	Mint func(ctx context.Context, key string) (AuthCode, error)
	// Store shares tokens between processes; nil keeps them per process.
	// It is a cache: when it fails, this process mints and renews on its
	// own instead of failing the call.
	Store TokenStore
	// MaxLineage bounds how long one exchanged auth code keeps serving
	// through refreshes (default 12h); after it the identity is minted
	// again, so the identity grant is re-checked.
	MaxLineage time.Duration
	// MaxClients bounds the pool (default 1024); expired and idle clients
	// are dropped first.
	MaxClients int

	mu      sync.Mutex
	entries map[string]*poolEntry
}

// String names the Pool without the Config's secret.
func (p *Pool) String() string {
	if p == nil {
		return "dws.Pool(nil)"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("dws.Pool{%s, clients=%d}", p.Config, len(p.entries))
}

// GoString keeps %#v as safe as %v.
func (p *Pool) GoString() string { return p.String() }

type poolEntry struct {
	mu     ctxLock
	client *Client
	// users counts callers between entry() and the end of Client(); guarded
	// by Pool.mu. A used entry is never evicted.
	users int
}

// Client returns the live Client for key, creating it when needed.
// Concurrent calls for one key share one initialization; a caller whose
// context ends while another initializes gives up with a temporary
// *InitError at StageMint. A Client whose session expired is replaced on the
// next call.
func (p *Pool) Client(ctx context.Context, key string) (*Client, error) {
	return p.ClientWith(ctx, key, func(ctx context.Context) (AuthCode, error) { return p.Mint(ctx, key) })
}

// ClientWith is Client with a mint for this call, for hosts whose code
// minting depends on the request (who asks, and why). mint runs only when
// neither this process nor the Store has a usable token for key.
func (p *Pool) ClientWith(ctx context.Context, key string, mint func(context.Context) (AuthCode, error)) (*Client, error) {
	e := p.entry(key)
	defer p.done(e)
	if err := e.mu.lock(ctx); err != nil {
		return nil, &InitError{Stage: StageMint, Temporary: true, kind: ErrMintFailed,
			cause: fmt.Errorf("waiting for another initialization: %w", err)}
	}
	defer e.mu.unlock()
	if e.client != nil && e.client.Alive() && !p.tooOld(e.client.Token()) {
		return e.client, nil
	}
	e.client = nil
	if p.Store != nil {
		c, err := p.shared(ctx, key, mint)
		if err != nil {
			return nil, err
		}
		e.client = c
		return c, nil
	}
	code, err := mint(ctx)
	if err != nil {
		return nil, &InitError{Stage: StageMint, Temporary: true, kind: ErrMintFailed, cause: err}
	}
	c, err := New(ctx, p.Config, code)
	if err != nil {
		return nil, err
	}
	e.client = c
	return c, nil
}

// shared returns a Client on key's stored token, minting and storing one
// under the store's lock when there is none.
func (p *Pool) shared(ctx context.Context, key string, mint func(context.Context) (AuthCode, error)) (*Client, error) {
	if c := p.fromStore(ctx, key); c != nil {
		return c, nil
	}
	unlock, err := p.Store.Lock(ctx, key, sharedLockTTL)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &InitError{Stage: StageMint, Temporary: true, kind: ErrMintFailed,
				cause: fmt.Errorf("waiting for another process to mint: %w", err)}
		}
		// The store is a cache: without it this process mints for itself.
		return p.mintOwn(ctx, mint)
	}
	defer unlock()
	work, cancel := context.WithTimeout(ctx, sharedWorkTimeout)
	defer cancel()
	// Another process may have minted while this one waited.
	if c := p.fromStore(work, key); c != nil {
		return c, nil
	}
	c, err := p.mintOwn(work, mint)
	if err != nil {
		return nil, err
	}
	c.shared = &sharedToken{store: p.Store, key: key}
	// Unsaved, the token still serves this process; the others mint their own.
	_ = p.Store.Save(work, key, c.Token())
	return c, nil
}

// mintOwn creates a Client from a freshly minted code.
func (p *Pool) mintOwn(ctx context.Context, mint func(context.Context) (AuthCode, error)) (*Client, error) {
	code, err := mint(ctx)
	if err != nil {
		return nil, &InitError{Stage: StageMint, Temporary: true, kind: ErrMintFailed, cause: err}
	}
	return New(ctx, p.Config, code)
}

// tooOld reports a lineage past MaxLineage.
func (p *Pool) tooOld(t Token) bool {
	limit := p.MaxLineage
	if limit <= 0 {
		limit = 12 * time.Hour
	}
	return !t.MintedAt.IsZero() && p.Config.now().Sub(t.MintedAt) > limit
}

// fromStore builds a Client on key's stored token, or returns nil. The
// token was verified when it was minted, so it is not verified again.
func (p *Pool) fromStore(ctx context.Context, key string) *Client {
	tok, ok, err := p.Store.Load(ctx, key)
	if err != nil || !ok {
		return nil
	}
	if tok.MintedAt.IsZero() {
		// Stored before lineages were dated: count from now.
		tok.MintedAt = p.Config.now()
	}
	cfg := p.Config
	cfg.SkipVerify = true
	c, err := NewWithToken(ctx, cfg, tok)
	if err != nil || !c.usable(tok) || p.tooOld(tok) {
		return nil
	}
	c.shared = &sharedToken{store: p.Store, key: key}
	return c
}

// Forget drops the Client for key; the next use creates a new one.
func (p *Pool) Forget(key string) {
	p.mu.Lock()
	delete(p.entries, key)
	p.mu.Unlock()
}

func (p *Pool) entry(key string) *poolEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.entries[key]; ok {
		e.users++
		return e
	}
	if p.entries == nil {
		p.entries = map[string]*poolEntry{}
	}
	limit := p.MaxClients
	if limit <= 0 {
		limit = 1024
	}
	if len(p.entries) >= limit {
		p.evict(limit)
	}
	e := &poolEntry{users: 1, mu: newCtxLock()}
	p.entries[key] = e
	return e
}

func (p *Pool) done(e *poolEntry) {
	p.mu.Lock()
	e.users--
	p.mu.Unlock()
}

// evict drops dead clients, then idle ones, until there is room. An entry
// someone is using is never dropped, or a second Client for the same
// identity would mint a second code; when all are in use the pool briefly
// exceeds its limit. Called with p.mu held.
func (p *Pool) evict(limit int) {
	for _, deadOnly := range []bool{true, false} {
		for key, e := range p.entries {
			if len(p.entries) < limit {
				return
			}
			if e.users > 0 || !e.mu.tryLock() {
				continue
			}
			drop := !deadOnly || e.client == nil || !e.client.Alive()
			e.mu.unlock()
			if drop {
				delete(p.entries, key)
			}
		}
	}
}
