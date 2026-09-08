package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var errFCE2BCheckLeaseLost = errors.New("FC sandbox check lease lost")

type fcE2BCheckCoordinator struct {
	client *redis.Client
	keys []string
	ttl time.Duration
	interval time.Duration
}

func newFCE2BCheckCoordinator(client *redis.Client, namespace string, ttl, interval time.Duration) *fcE2BCheckCoordinator {
	prefix := fmt.Sprintf("multica:{fc-sandbox-check:%x}:", sha256.Sum256([]byte(namespace)))
	return &fcE2BCheckCoordinator{client: client, keys: []string{prefix + "lease", prefix + "next"}, ttl: ttl, interval: interval}
}

// Both keys share a cluster slot. Redis TIME owns the scheduling clock.
var fcE2BCheckClaimScript = redis.NewScript(`
local clock = redis.call('TIME')
local now = clock[1] * 1000 + math.floor(clock[2] / 1000)
local next_at = tonumber(redis.call('GET', KEYS[2]) or '0')
if next_at > now then return 0 end
if redis.call('SET', KEYS[1], ARGV[1], 'NX', 'PX', ARGV[2]) then return 1 end
return 0
`)

var fcE2BCheckRenewScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('PEXPIRE', KEYS[1], ARGV[2])
`)

var fcE2BCheckFinishScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
local clock = redis.call('TIME')
local now = clock[1] * 1000 + math.floor(clock[2] / 1000)
redis.call('SET', KEYS[2], now + tonumber(ARGV[2]), 'PX', tonumber(ARGV[2]) + 86400000)
redis.call('DEL', KEYS[1])
return 1
`)

func (c *fcE2BCheckCoordinator) claim(ctx context.Context) (string, bool, error) {
	if c == nil || c.client == nil || c.ttl <= 0 || c.interval <= 0 {
		return "", false, errors.New("FC sandbox Redis coordination is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	token := uuid.NewString()
	result, err := fcE2BCheckClaimScript.Run(ctx, c.client, c.keys, token, c.ttl.Milliseconds()).Int()
	if err != nil {
		return "", false, errors.New("claim FC sandbox check lease failed")
	}
	return token, result == 1, nil
}

func (c *fcE2BCheckCoordinator) renew(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := fcE2BCheckRenewScript.Run(ctx, c.client, c.keys[:1], token, c.ttl.Milliseconds()).Int()
	if err != nil || result != 1 {
		return errFCE2BCheckLeaseLost
	}
	return nil
}

func (c *fcE2BCheckCoordinator) finish(ctx context.Context, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := fcE2BCheckFinishScript.Run(ctx, c.client, c.keys, token, c.interval.Milliseconds()).Int()
	if err != nil || result != 1 {
		return errFCE2BCheckLeaseLost
	}
	return nil
}
