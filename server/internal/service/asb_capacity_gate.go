package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	asbCapacityBusyCooldown = 30 * time.Second
	asbCapacityBackoffMax   = 5 * time.Minute
	asbCapacityGateTimeout  = 2 * time.Second
)

// ASBCapacityGate shares negative capacity results across API replicas.
// The PostgreSQL tenant lock owns reclaim/create serialization; healthy
// launches do not need an additional interval between holders of that lock.
type ASBCapacityGate struct {
	redis *redis.Client
}

func NewASBCapacityGate(client *redis.Client) *ASBCapacityGate {
	return &ASBCapacityGate{redis: client}
}

func (c *ASBClient) capacityGateKeys() []string {
	// Never use a credential or the collision-prone advisory-lock integer in
	// Redis keys. Both keys share a cluster slot for atomic scripts on Tair.
	fingerprint := sha256.Sum256([]byte(c.baseURL.String() + "\x00" + c.apiKey))
	prefix := fmt.Sprintf("multica:asb:capacity:{%x}:", fingerprint)
	return []string{prefix + "cooldown", prefix + "throttles"}
}

var asbCapacityAdmitScript = redis.NewScript(`
local ttl = redis.call('PTTL', KEYS[1])
if ttl > 0 then
  local verdict = redis.call('GET', KEYS[1])
  if verdict ~= 'checking' and verdict ~= 'available' then return ttl end
  redis.call('DEL', KEYS[1])
end
return 0
`)

var asbCapacityThrottleScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == 'throttled' then
  return math.max(0, redis.call('PTTL', KEYS[1]))
end
return 0
`)

// A warm sandbox does not need free capacity, but still respects an upstream
// rate limit shared by the credential. It must not bypass a cold launch's 429.
func (g *ASBCapacityGate) throttleDelay(ctx context.Context, client *ASBClient) (time.Duration, error) {
	if g == nil || g.redis == nil {
		return 0, errors.New("ASB shared capacity coordination requires Redis")
	}
	ctx, cancel := context.WithTimeout(ctx, asbCapacityGateTimeout)
	defer cancel()
	millis, err := asbCapacityThrottleScript.Run(ctx, g.redis, client.capacityGateKeys()[:1]).Int64()
	return time.Duration(millis) * time.Millisecond, err
}

var asbCapacityResultScript = redis.NewScript(`
local delay = tonumber(ARGV[1])
if ARGV[2] == 'throttled' then
  local failures = redis.call('INCR', KEYS[2])
  redis.call('PEXPIRE', KEYS[2], ARGV[4])
  delay = math.max(tonumber(ARGV[5]), math.min(tonumber(ARGV[3]), delay * 2 ^ math.min(failures - 1, 10)))
elseif redis.call('GET', KEYS[1]) ~= 'throttled' then
  redis.call('DEL', KEYS[2])
end
local previous = redis.call('PTTL', KEYS[1])
if ARGV[2] == 'available' then
  local verdict = redis.call('GET', KEYS[1])
  if verdict == 'checking' or verdict == 'available' then
    redis.call('DEL', KEYS[1])
    return 0
  end
  return math.max(0, previous)
end
if delay > previous then
  redis.call('SET', KEYS[1], ARGV[2], 'PX', delay)
end
return math.max(delay, previous)
`)

// admit runs under the tenant lock. A positive marker left by an older replica
// adds no protection once this caller owns that lock. Negative verdicts remain
// durable, including throttles recorded by concurrent warm-sandbox operations.
func (g *ASBCapacityGate) admit(ctx context.Context, client *ASBClient) (time.Duration, error) {
	if g == nil || g.redis == nil {
		return 0, errors.New("ASB shared capacity coordination requires Redis")
	}
	ctx, cancel := context.WithTimeout(ctx, asbCapacityGateTimeout)
	defer cancel()
	millis, err := asbCapacityAdmitScript.Run(ctx, g.redis, client.capacityGateKeys()[:1]).Int64()
	return time.Duration(millis) * time.Millisecond, err
}

func (g *ASBCapacityGate) record(ctx context.Context, client *ASBClient, cause error) (time.Duration, error) {
	if g == nil || g.redis == nil {
		return 0, errors.New("ASB shared capacity coordination requires Redis")
	}
	delay, result, retryAfter := time.Duration(0), "available", time.Duration(0)
	var httpErr *ASBHTTPError
	if errors.As(cause, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests {
		delay, result, retryAfter = asbCapacityBusyCooldown, "throttled", httpErr.RetryAfter
	} else if errors.Is(cause, ErrASBCapacityUnavailable) {
		delay, result = asbCapacityBusyCooldown, "full"
	}
	ctx, cancel := context.WithTimeout(ctx, asbCapacityGateTimeout)
	defer cancel()
	millis, err := asbCapacityResultScript.Run(ctx, g.redis, client.capacityGateKeys(),
		delay.Milliseconds(), result, asbCapacityBackoffMax.Milliseconds(),
		(2 * asbCapacityBackoffMax).Milliseconds(), retryAfter.Milliseconds()).Int64()
	return time.Duration(millis) * time.Millisecond, err
}

func isASBRateLimited(err error) bool {
	var httpErr *ASBHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests
}

func recordASBCapacityResult(ctx context.Context, client *ASBClient, runtimeID string, cause error) error {
	if client.CapacityGate != nil {
		delay, err := client.CapacityGate.record(ctx, client, cause)
		if err != nil {
			slog.Warn("ASB shared capacity result could not be stored", "runtime_id", runtimeID, "error", err)
		} else if errors.Is(cause, ErrASBCapacityUnavailable) || isASBRateLimited(cause) {
			slog.Info("ASB tenant capacity check deferred", "event", "asb_capacity_cooldown_recorded",
				"runtime_id", runtimeID, "retry_after_ms", delay.Milliseconds(), "rate_limited", isASBRateLimited(cause))
		}
	}
	if isASBRateLimited(cause) {
		var upstream *ASBHTTPError
		if errors.As(cause, &upstream) {
			slog.Warn("ASB upstream rate limit deferred task startup", "event", "asb_capacity_rate_limited",
				"runtime_id", runtimeID, "operation", upstream.Operation, "http_status", upstream.StatusCode,
				"request_id", upstream.RequestID, "error_code", upstream.ErrorCode)
		}
		return errors.Join(ErrASBCapacityUnavailable, cause)
	}
	return cause
}
