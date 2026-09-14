package dshhost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type nativeAccessStub struct {
	record      NativeAccess
	hash        string
	exchanges   int
	err         error
	exchangeErr error
}

func (s *nativeAccessStub) InsertNativeAccess(_ context.Context, access NativeAccess, hash string) (NativeAccess, error) {
	access.ExpiresAt = time.Now().Add(NativeEntryLifetime)
	s.record, s.hash = access, hash
	return access, s.err
}
func (s *nativeAccessStub) GetNativeAccess(_ context.Context, hash, kind string) (NativeAccess, error) {
	if hash != s.hash || kind != s.record.Kind {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	return s.record, s.err
}
func (s *nativeAccessStub) ExchangeNativeAccess(_ context.Context, access NativeAccess, entryHash, sessionHash string) (NativeAccess, error) {
	s.exchanges++
	if s.err != nil {
		return NativeAccess{}, s.err
	}
	if entryHash != s.hash || s.record.Kind != "entry" {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	s.record.Kind, s.hash = "session", sessionHash
	s.record.ExpiresAt = time.Now().Add(NativeSessionLifetime)
	return s.record, s.exchangeErr
}
func (s *nativeAccessStub) RevokeNativeAccess(_ context.Context, key Key, id uuid.UUID) error {
	if s.record.Key == key && s.record.ID == id {
		s.record.Kind = "revoked"
	}
	return s.err
}

func TestNativeAccessTokensAndRepeatedAuthorization(t *testing.T) {
	ctx := context.Background()
	host := Host{Key: Key{uuid.New(), uuid.New()}, Generation: 1, SandboxID: "sbx-fixture", State: "running"}
	user := uuid.New()
	store := &nativeAccessStub{}
	denied, checks := false, 0
	manager := NativeAccessManager{Store: store, CheckManage: func(_ context.Context, key Key, actor uuid.UUID) error {
		checks++
		if denied || key != host.Key || actor != user {
			return errors.New("private permission diagnostic")
		}
		return nil
	}}
	entry, code, err := manager.Issue(ctx, host, user)
	if err != nil || code == "" || entry.Kind != "entry" {
		t.Fatal("entry was not issued")
	}
	if len(store.hash) != 64 || strings.Contains(store.hash, code) {
		t.Fatal("store must receive only a digest")
	}
	for _, changed := range []Host{
		{Key: Key{uuid.New(), host.AgentID}, Generation: 1, SandboxID: host.SandboxID},
		{Key: Key{host.WorkspaceID, uuid.New()}, Generation: 1, SandboxID: host.SandboxID},
		{Key: host.Key, Generation: 2, SandboxID: host.SandboxID},
		{Key: host.Key, Generation: 1, SandboxID: "sbx-different"},
	} {
		if _, token, err := manager.Exchange(ctx, code, changed); !errors.Is(err, ErrNativeAccessDenied) || token != "" {
			t.Fatal("cross-Host exchange allowed")
		}
	}
	if store.exchanges != 0 {
		t.Fatal("invalid identity consumed entry")
	}
	active, token, err := manager.Exchange(ctx, code, host)
	if err != nil || active.ID != entry.ID || token == code || active.Kind != "session" {
		t.Fatal("exchange failed")
	}
	if _, _, err := manager.Exchange(ctx, code, host); !errors.Is(err, ErrNativeAccessDenied) {
		t.Fatal("entry replay allowed")
	}
	for range 2 {
		if _, err := manager.Authorize(ctx, token, host); err != nil {
			t.Fatal(err)
		}
	}
	before := checks
	denied = true
	if _, err := manager.Authorize(ctx, token, host); !errors.Is(err, ErrNativeAccessDenied) {
		t.Fatal("membership revocation ignored")
	}
	if checks != before+1 {
		t.Fatal("authorization reused stale permission")
	}
	denied = false
	if err := manager.Revoke(ctx, host.Key, active.ID, user); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authorize(ctx, token, host); !errors.Is(err, ErrNativeAccessDenied) {
		t.Fatal("revoked grant accepted")
	}
}

func TestNativeAccessRejectsMalformedAndWrongTokenKinds(t *testing.T) {
	entry, _, err := newNativeAccessToken("entry")
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := newNativeAccessToken("session")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ token, kind string }{
		{entry, "session"}, {session, "entry"}, {entry + "=", "entry"}, {" " + entry, "entry"},
		{"dnge_" + strings.Repeat("!", 43), "entry"}, {"", "entry"}, {entry, "invalid"},
	} {
		if _, err := nativeAccessHash(input.token, input.kind); !errors.Is(err, ErrNativeAccessDenied) {
			t.Fatal("invalid capability accepted")
		}
	}
}

func TestNativeAccessUnknownExchangeReturnsNoCredential(t *testing.T) {
	store := &nativeAccessStub{}
	manager := NativeAccessManager{Store: store, CheckManage: func(context.Context, Key, uuid.UUID) error { return nil }}
	host := Host{Key: Key{uuid.New(), uuid.New()}, State: "running", Generation: 1, SandboxID: "sbx-test"}
	_, entry, err := manager.Issue(context.Background(), host, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	// Model a committed exchange whose database response was lost.
	store.exchangeErr = errors.New("database unavailable")
	if _, token, err := manager.Exchange(context.Background(), entry, host); err == nil || token != "" {
		t.Fatal("failed exchange leaked a credential")
	}
	if store.record.Kind != "session" || store.exchanges != 1 {
		t.Fatal("fixture did not commit the uncertain exchange")
	}
	if _, token, err := manager.Exchange(context.Background(), entry, host); err == nil || token != "" || store.exchanges != 1 {
		t.Fatal("uncertain exchange was repeated")
	}
}

func TestNativeAccessPostgresSingleExchangeExpiryAndHostRetirement(t *testing.T) {
	a, b := stores(t)
	ctx := context.Background()
	host := bind(t, a)
	var err error
	host, err = a.BeginCreate(ctx, host.Key, host.Generation, uuid.New(), "template")
	if err != nil {
		t.Fatal(err)
	}
	host, err = a.CompleteCreate(ctx, host, "sbx-native-access")
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, Key, uuid.UUID) error { return nil }
	first, second := NativeAccessManager{a, check}, NativeAccessManager{b, check}
	access, entry, err := first.Issue(ctx, host, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var group sync.WaitGroup
	tokens := make(chan string, 24)
	for i := range 24 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			manager := first
			if i%2 == 1 {
				manager = second
			}
			_, token, err := manager.Exchange(ctx, entry, host)
			if err == nil {
				wins.Add(1)
				tokens <- token
			} else if !errors.Is(err, ErrNativeAccessDenied) {
				t.Error(err)
			}
		}(i)
	}
	group.Wait()
	close(tokens)
	if wins.Load() != 1 {
		t.Fatalf("successful exchanges = %d", wins.Load())
	}
	token := <-tokens
	if _, err := second.Authorize(ctx, token, host); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `UPDATE dsh_native_access SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, access.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Authorize(ctx, token, host); !errors.Is(err, ErrNativeAccessDenied) {
		t.Fatal("expired session accepted")
	}
	_, entry, err = first.Issue(ctx, host, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginRetire(ctx, host); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Exchange(ctx, entry, host); !errors.Is(err, ErrNativeAccessDenied) {
		t.Fatal("retiring Host accepted entry")
	}
}
