package redisstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Sealer encrypts what TokenStore writes, e.g. AES-GCM with a key every
// replica holds. Tokens are never written unsealed.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// TokenStore implements dws.TokenStore on Redis (or Tair), sealing every
// token with Sealer.
type TokenStore struct {
	Redis  redis.UniversalClient
	Sealer Sealer
	// Prefix namespaces the keys (default "dws-for-tag:tokens:").
	Prefix string
	// TTL drops a token nobody saved for this long (default 24h; DingTalk
	// access tokens live 2h and are refreshed well within it).
	TTL time.Duration
	// LockPoll is how often a waiting Lock retries (default 100ms).
	LockPoll time.Duration
}

var _ dws.TokenStore = (*TokenStore)(nil)

// storedToken is the sealed form. The key names are the store's own, so a
// change of dws.Token cannot silently change what old replicas read.
type storedToken struct {
	AccessToken  string    `json:"a"`
	RefreshToken string    `json:"r,omitempty"`
	ClientID     string    `json:"c,omitempty"`
	ExpiresAt    time.Time `json:"e,omitempty"`
	CorpID       string    `json:"o,omitempty"`
	MintedAt     time.Time `json:"m,omitempty"`
}

// key hashes the identity so key names carry no account ids.
func (s *TokenStore) key(kind, identity string) string {
	p := s.Prefix
	if p == "" {
		p = "dws-for-tag:tokens:"
	}
	sum := sha256.Sum256([]byte(identity))
	return p + kind + ":" + hex.EncodeToString(sum[:16])
}

func (s *TokenStore) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return 24 * time.Hour
}

func (s *TokenStore) Load(ctx context.Context, identity string) (dws.Token, bool, error) {
	if s.Sealer == nil {
		return dws.Token{}, false, errors.New("redisstore: tokens need a Sealer")
	}
	raw, err := s.Redis.Get(ctx, s.key("token", identity)).Bytes()
	if errors.Is(err, redis.Nil) {
		return dws.Token{}, false, nil
	}
	if err != nil {
		return dws.Token{}, false, err
	}
	plain, err := s.Sealer.Open(raw)
	if err != nil {
		// Unreadable (e.g. sealed with another key): treat as absent, so the
		// caller mints and overwrites it.
		return dws.Token{}, false, nil
	}
	var t storedToken
	if json.Unmarshal(plain, &t) != nil || t.AccessToken == "" {
		return dws.Token{}, false, nil
	}
	return dws.Token{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ClientID: t.ClientID,
		ExpiresAt: t.ExpiresAt, CorpID: t.CorpID, MintedAt: t.MintedAt}, true, nil
}

func (s *TokenStore) Save(ctx context.Context, identity string, tok dws.Token) error {
	if s.Sealer == nil {
		return errors.New("redisstore: tokens need a Sealer")
	}
	plain, err := json.Marshal(storedToken{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		ClientID: tok.ClientID, ExpiresAt: tok.ExpiresAt, CorpID: tok.CorpID, MintedAt: tok.MintedAt})
	if err != nil {
		return err
	}
	sealed, err := s.Sealer.Seal(plain)
	if err != nil {
		return err
	}
	return s.Redis.Set(ctx, s.key("token", identity), sealed, s.ttl()).Err()
}

func (s *TokenStore) Delete(ctx context.Context, identity string) error {
	return s.Redis.Del(ctx, s.key("token", identity)).Err()
}

// Lock takes the identity's lock with SET NX PX, polling while another
// process holds it; unlock deletes it only while this process holds it.
func (s *TokenStore) Lock(ctx context.Context, identity string, ttl time.Duration) (func(), error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	holder := hex.EncodeToString(nonce[:])
	key := s.key("lock", identity)
	poll := s.LockPoll
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	for {
		ok, err := s.Redis.SetNX(ctx, key, holder, ttl).Result()
		if err != nil {
			return nil, err
		}
		if ok {
			return func() {
				// Released with a fresh context: the caller's may have ended.
				rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = releaseScript.Run(rctx, s.Redis, []string{key}, holder).Err()
			}, nil
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}
