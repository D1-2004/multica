// Package redisstore keeps events.Store state in Redis (or Tair), so every
// replica shares leases, dedupe claims, subscriptions and health.
package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/dws/events"
)

// Store implements events.Store on Redis.
type Store struct {
	Redis redis.UniversalClient
	// Prefix namespaces the keys (default "dws-for-tag:events:").
	Prefix string
}

var _ events.Store = (*Store)(nil)

func (s *Store) key(parts ...string) string {
	k := s.Prefix
	if k == "" {
		k = "dws-for-tag:events:"
	}
	for i, p := range parts {
		if i > 0 {
			k += ":"
		}
		k += p
	}
	return k
}

// leaseScript acquires a free lease or renews one the holder owns.
var leaseScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
if v == ARGV[1] then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return 1
end
return 0`)

// releaseScript deletes the lease only for its holder.
var releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

func (s *Store) Lease(ctx context.Context, identity, holder string, ttl time.Duration) (bool, error) {
	n, err := leaseScript.Run(ctx, s.Redis, []string{s.key("lease", identity)}, holder, ttl.Milliseconds()).Int()
	return n == 1, err
}

func (s *Store) Release(ctx context.Context, identity, holder string) error {
	return releaseScript.Run(ctx, s.Redis, []string{s.key("lease", identity)}, holder).Err()
}

func (s *Store) Handled(ctx context.Context, identity, eventID string) (bool, error) {
	n, err := s.Redis.Exists(ctx, s.key("handled", identity, eventID)).Result()
	return n == 1, err
}

func (s *Store) MarkHandled(ctx context.Context, identity, eventID string, ttl time.Duration) error {
	return s.Redis.Set(ctx, s.key("handled", identity, eventID), 1, ttl).Err()
}

func (s *Store) Subscriptions(ctx context.Context, identity string) ([]dws.Subscription, error) {
	raw, err := s.Redis.Get(ctx, s.key("subs", identity)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var subs []dws.Subscription
	return subs, json.Unmarshal(raw, &subs)
}

// SetSubscriptions uses single-key commands in a safe order instead of a
// transaction: the keys live in different Redis Cluster slots. The identity
// is listed before its subscriptions are written and unlisted after they
// are removed, so a crash in between never hides an identity from cleanup.
func (s *Store) SetSubscriptions(ctx context.Context, identity string, subs []dws.Subscription) error {
	if len(subs) == 0 {
		if err := s.Redis.Del(ctx, s.key("subs", identity)).Err(); err != nil {
			return err
		}
		return s.Redis.SRem(ctx, s.key("subscribed"), identity).Err()
	}
	if err := s.Redis.SAdd(ctx, s.key("subscribed"), identity).Err(); err != nil {
		return err
	}
	raw, _ := json.Marshal(subs)
	return s.Redis.Set(ctx, s.key("subs", identity), raw, 0).Err()
}

func (s *Store) SubscribedIdentities(ctx context.Context) ([]string, error) {
	ids, err := s.Redis.SMembers(ctx, s.key("subscribed")).Result()
	sort.Strings(ids)
	return ids, err
}

func (s *Store) SetStatus(ctx context.Context, st events.Status) error {
	raw, _ := json.Marshal(st)
	return s.Redis.HSet(ctx, s.key("status"), st.Identity, raw).Err()
}

func (s *Store) Status(ctx context.Context, identity string) (events.Status, bool, error) {
	raw, err := s.Redis.HGet(ctx, s.key("status"), identity).Bytes()
	if errors.Is(err, redis.Nil) {
		return events.Status{}, false, nil
	}
	if err != nil {
		return events.Status{}, false, err
	}
	var st events.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return events.Status{}, false, err
	}
	return st, true, nil
}

func (s *Store) Statuses(ctx context.Context) ([]events.Status, error) {
	all, err := s.Redis.HGetAll(ctx, s.key("status")).Result()
	if err != nil {
		return nil, err
	}
	out := make([]events.Status, 0, len(all))
	for _, raw := range all {
		var st events.Status
		if json.Unmarshal([]byte(raw), &st) == nil {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out, nil
}
