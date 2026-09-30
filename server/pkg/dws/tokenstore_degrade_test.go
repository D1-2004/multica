package dws

import (
	"context"
	"errors"
	"testing"
	"time"
)

// deadStore is a store whose backend is down.
type deadStore struct{}

var errStoreDown = errors.New("dial tcp: connection refused")

func (deadStore) Load(context.Context, string) (Token, bool, error) {
	return Token{}, false, errStoreDown
}
func (deadStore) Save(context.Context, string, Token) error { return errStoreDown }
func (deadStore) Delete(context.Context, string) error      { return errStoreDown }
func (deadStore) Lock(context.Context, string, time.Duration) (func(), error) {
	return nil, errStoreDown
}

// The store is a cache: when it is down a replica mints and renews on its
// own instead of failing DingTalk calls.
func TestADeadStoreDegradesToOwnTokens(t *testing.T) {
	f := newSharedFixture(t)
	p := f.replica()
	p.Store = deadStore{}
	c := callOK(t, p)
	if f.mints.Load() != 1 {
		t.Fatalf("mints=%d", f.mints.Load())
	}
	f.clk.add(56 * time.Minute)
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil {
		t.Fatalf("renewal without the store: %v", err)
	}
	if f.refreshes.Load() != 1 {
		t.Fatalf("refreshes=%d", f.refreshes.Load())
	}
}

// A store that fails after the lock (reads) still leaves the call working.
type unreadableStore struct{ MemoryTokenStore }

func (*unreadableStore) Load(context.Context, string) (Token, bool, error) {
	return Token{}, false, errStoreDown
}

func TestAnUnreadableStoreDegradesToOwnTokens(t *testing.T) {
	f := newSharedFixture(t)
	p := f.replica()
	p.Store = &unreadableStore{}
	c := callOK(t, p)
	f.clk.add(56 * time.Minute)
	if _, err := c.Call(context.Background(), ServerIM, "x", nil); err != nil || f.refreshes.Load() != 1 {
		t.Fatalf("renewal: %v, refreshes=%d", err, f.refreshes.Load())
	}
}

// One exchanged code serves at most MaxLineage; then the identity is minted
// again, in this process and from the store alike.
func TestALineageIsMintedAgainAfterItsMaximumAge(t *testing.T) {
	f := newSharedFixture(t)
	a := f.replica()
	a.MaxLineage = 3 * time.Hour
	callOK(t, a)
	for i := 0; i < 3; i++ { // refreshed hourly, as a live identity is
		f.clk.add(56 * time.Minute)
		callOK(t, a)
	}
	if f.mints.Load() != 1 {
		t.Fatalf("minted before the limit: %d", f.mints.Load())
	}
	f.clk.add(20 * time.Minute) // lineage now past 3h
	callOK(t, a)
	if f.mints.Load() != 2 {
		t.Fatalf("mints=%d after the lineage limit", f.mints.Load())
	}
	b := f.replica()
	b.MaxLineage = 3 * time.Hour
	callOK(t, b)
	if f.mints.Load() != 2 {
		t.Fatalf("the fresh stored lineage was not reused: mints=%d", f.mints.Load())
	}
}
