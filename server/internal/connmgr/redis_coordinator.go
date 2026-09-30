package connmgr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultLeaseTTL is deliberately short: a process that exits without
// releasing its member should be replaceable in seconds.
const DefaultLeaseTTL = 10 * time.Second

// RedisCoordinatorConfig fixes the global steady-state READY target for one
// coordinator instance.
type RedisCoordinatorConfig struct {
	// KeyPrefix namespaces every Redis key, e.g. "multica:dws-events:v1:".
	// Keys are KeyPrefix+"{"+group+"}:members" and KeyPrefix+"{"+group+"}:expiry".
	// Required; must not contain '{' or '}' (the hash tag is the group).
	KeyPrefix string

	// TargetReady is the global READY target per group: 1 or 2.
	TargetReady int

	// LeaseTTL is the member lease; zero uses DefaultLeaseTTL. Non-default
	// values primarily support deterministic expiry tests.
	LeaseTTL time.Duration
}

// RedisCoordinator atomically maintains a small member HASH and expiry ZSET
// per group. The two keys share one Redis Cluster hash tag, and every mutation
// is performed by Lua with Redis TIME as the sole shared clock.
type RedisCoordinator struct {
	redis       redis.Scripter
	keyPrefix   string
	targetReady int
	leaseTTL    time.Duration
	ttlMillis   int64
}

var _ Coordinator = (*RedisCoordinator)(nil)

// NewRedisCoordinator returns an error rather than selecting another lease
// backend. Redis is the sole ownership authority once this coordinator is used.
func NewRedisCoordinator(client redis.Scripter, cfg RedisCoordinatorConfig) (*RedisCoordinator, error) {
	if client == nil {
		return nil, &CoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: Redis client is required", ErrConfig)}
	}
	if cfg.KeyPrefix == "" {
		return nil, &CoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: key prefix is required", ErrConfig)}
	}
	if strings.ContainsAny(cfg.KeyPrefix, "{}") {
		return nil, &CoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: key prefix must not contain '{' or '}'", ErrConfig)}
	}
	if cfg.TargetReady != 1 && cfg.TargetReady != 2 {
		return nil, &CoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: target_ready must be 1 or 2", ErrConfig)}
	}
	ttl := cfg.LeaseTTL
	if ttl == 0 {
		ttl = DefaultLeaseTTL
	}
	if ttl < time.Millisecond {
		return nil, &CoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: lease_ttl must be at least 1ms", ErrConfig)}
	}
	return &RedisCoordinator{
		redis:       client,
		keyPrefix:   cfg.KeyPrefix,
		targetReady: cfg.TargetReady,
		leaseTTL:    ttl,
		ttlMillis:   ttl.Milliseconds(),
	}, nil
}

func (c *RedisCoordinator) TargetReady() int        { return c.targetReady }
func (c *RedisCoordinator) LeaseTTL() time.Duration { return c.leaseTTL }

// Claim reserves one CONNECTING member. CONNECTING counts against capacity,
// preventing concurrent dials from overshooting the target. During a handoff,
// one DRAINING member grants exactly one extra total slot: target one permits
// two connections; target two permits three.
func (c *RedisCoordinator) Claim(ctx context.Context, group, token string) (Lease, Snapshot, bool, error) {
	if err := validateIdentity("claim", group, token); err != nil {
		return Lease{}, Snapshot{}, false, err
	}
	result, err := c.run(ctx, "claim", redisClaimScript, group, token, c.targetReady, c.ttlMillis)
	if err != nil {
		return Lease{}, Snapshot{}, false, err
	}
	snapshot := c.snapshot(result)
	switch result.code {
	case scriptCapacity:
		return Lease{}, snapshot, false, nil
	case scriptOK:
		return Lease{Group: group, Token: token, State: StateConnecting}, snapshot, true, nil
	default:
		return Lease{}, snapshot, false, c.resultError("claim", group, StateConnecting, result)
	}
}

// MarkReady records that the connection can receive. Callers must not consume
// events before this succeeds.
func (c *RedisCoordinator) MarkReady(ctx context.Context, lease Lease) (Lease, Snapshot, error) {
	if err := validateLease("mark_ready", lease); err != nil {
		return Lease{}, Snapshot{}, err
	}
	result, err := c.run(ctx, "mark_ready", redisMarkReadyScript, lease.Group, lease.Token, c.ttlMillis)
	if err != nil {
		return Lease{}, Snapshot{}, err
	}
	if result.code != scriptOK {
		return Lease{}, c.snapshot(result), c.resultError("mark_ready", lease.Group, StateConnecting, result)
	}
	lease.State = StateReady
	return lease, c.snapshot(result), nil
}

// Renew extends an existing member only when both token and state still match.
func (c *RedisCoordinator) Renew(ctx context.Context, lease Lease) (Snapshot, error) {
	if err := validateLease("renew", lease); err != nil {
		return Snapshot{}, err
	}
	result, err := c.run(ctx, "renew", redisRenewScript, lease.Group, lease.Token, string(lease.State), c.ttlMillis)
	if err != nil {
		return Snapshot{}, err
	}
	if result.code != scriptOK {
		return c.snapshot(result), c.resultError("renew", lease.Group, lease.State, result)
	}
	return c.snapshot(result), nil
}

// BeginDrain serializes graceful handoffs and keeps the old member alive while
// a replacement claims the one extra slot and reaches READY.
func (c *RedisCoordinator) BeginDrain(ctx context.Context, lease Lease) (Lease, Snapshot, error) {
	if err := validateLease("begin_drain", lease); err != nil {
		return Lease{}, Snapshot{}, err
	}
	result, err := c.run(ctx, "begin_drain", redisBeginDrainScript, lease.Group, lease.Token, c.ttlMillis)
	if err != nil {
		return Lease{}, Snapshot{}, err
	}
	if result.code != scriptOK {
		return Lease{}, c.snapshot(result), c.resultError("begin_drain", lease.Group, StateReady, result)
	}
	lease.State = StateDraining
	return lease, c.snapshot(result), nil
}

// Release removes only the supplied token. A stale predecessor cannot delete a
// successor because each connection generation has a distinct token.
func (c *RedisCoordinator) Release(ctx context.Context, lease Lease) error {
	if err := validateIdentity("release", lease.Group, lease.Token); err != nil {
		return err
	}
	if _, err := redisReleaseScript.Run(ctx, c.redis, c.keys(lease.Group), lease.Token, c.ttlMillis).Result(); err != nil {
		return &CoordinatorError{Op: "release", Group: lease.Group, Kind: err}
	}
	return nil
}

func (c *RedisCoordinator) keys(group string) []string {
	base := c.keyPrefix + "{" + group + "}"
	return []string{base + ":members", base + ":expiry"}
}

func (c *RedisCoordinator) run(ctx context.Context, op string, script *redis.Script, group string, args ...any) (scriptResult, error) {
	raw, err := script.Run(ctx, c.redis, c.keys(group), args...).Result()
	if err != nil {
		return scriptResult{}, &CoordinatorError{Op: op, Group: group, Kind: err}
	}
	result, err := decodeScriptResult(raw)
	if err != nil {
		return scriptResult{}, &CoordinatorError{Op: op, Group: group, Kind: fmt.Errorf("%w: %v", ErrCorruptState, err)}
	}
	return result, nil
}

func (c *RedisCoordinator) snapshot(result scriptResult) Snapshot {
	return Snapshot{
		TargetReady: c.targetReady,
		State:       result.state,
		Total:       int(result.total),
		Connecting:  int(result.connecting),
		Ready:       int(result.ready),
		Draining:    int(result.draining),
		ServerNow:   unixMilliOrZero(result.nowMillis),
		ExpiresAt:   unixMilliOrZero(result.expiresMillis),
	}
}

func (c *RedisCoordinator) resultError(op, group string, expected State, result scriptResult) error {
	kind := ErrCorruptState
	switch result.code {
	case scriptLost:
		kind = ErrLeaseLost
	case scriptStateMismatch:
		kind = ErrLeaseStateMismatch
	case scriptDrainInProgress:
		kind = ErrDrainInProgress
	case scriptInvalidTarget:
		kind = ErrConfig
	}
	return &CoordinatorError{
		Op:       op,
		Group:    group,
		Expected: expected,
		Actual:   result.state,
		Kind:     kind,
	}
}

func validateLease(op string, lease Lease) error {
	if err := validateIdentity(op, lease.Group, lease.Token); err != nil {
		return err
	}
	if err := validateState(lease.State); err != nil {
		return &CoordinatorError{Op: op, Group: lease.Group, Actual: lease.State, Kind: err}
	}
	return nil
}

func validateIdentity(op, group, token string) error {
	if err := validateGroup(group); err != nil {
		// An invalid group is not echoed: it may be oversized.
		return &CoordinatorError{Op: op, Kind: err}
	}
	if err := validateToken(token); err != nil {
		return &CoordinatorError{Op: op, Group: group, Kind: err}
	}
	return nil
}

func unixMilliOrZero(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

const (
	scriptOK              int64 = 1
	scriptCapacity        int64 = 0
	scriptLost            int64 = -1
	scriptStateMismatch   int64 = -2
	scriptDrainInProgress int64 = -3
	scriptCorrupt         int64 = -4
	scriptInvalidTarget   int64 = -5
)

type scriptResult struct {
	code          int64
	state         State
	total         int64
	connecting    int64
	ready         int64
	draining      int64
	nowMillis     int64
	expiresMillis int64
}

func decodeScriptResult(raw any) (scriptResult, error) {
	values, ok := raw.([]any)
	if !ok || len(values) != 8 {
		return scriptResult{}, fmt.Errorf("unexpected Lua result shape %T", raw)
	}
	ints := make([]int64, 0, 7)
	for _, index := range []int{0, 2, 3, 4, 5, 6, 7} {
		value, err := scriptInt64(values[index])
		if err != nil {
			return scriptResult{}, fmt.Errorf("result field %d: %w", index, err)
		}
		ints = append(ints, value)
	}
	state, err := scriptString(values[1])
	if err != nil {
		return scriptResult{}, fmt.Errorf("result state: %w", err)
	}
	return scriptResult{
		code:          ints[0],
		state:         State(state),
		total:         ints[1],
		connecting:    ints[2],
		ready:         ints[3],
		draining:      ints[4],
		nowMillis:     ints[5],
		expiresMillis: ints[6],
	}, nil
}

func scriptInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func scriptString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	default:
		return "", fmt.Errorf("expected string, got %T", value)
	}
}

// All scripts share this prelude. The HASH stores token -> state; the ZSET
// stores token -> absolute expiry in Redis server milliseconds. At most three
// members are valid, so full HASH state counting is deliberately bounded.
//
// The Lua bodies are byte-for-byte the robot connector's coordinator scripts
// (internal/integrations/channel/engine/redis_stream_coordinator.go); only the
// key layout (KeyPrefix) and the Go-side group type differ.
const redisLuaPrelude = `
local members_key = KEYS[1]
local expiry_key = KEYS[2]
local ttl_ms = tonumber(ARGV[#ARGV])
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

local function prune_expired()
    local expired = redis.call('ZRANGEBYSCORE', expiry_key, '-inf', now_ms)
    for _, expired_token in ipairs(expired) do
        redis.call('HDEL', members_key, expired_token)
        redis.call('ZREM', expiry_key, expired_token)
    end
end

local function stats()
    local connecting = 0
    local ready = 0
    local draining = 0
    local corrupt = 0
    local entries = redis.call('HGETALL', members_key)
    for index = 1, #entries, 2 do
        local token = entries[index]
        local state = entries[index + 1]
        if not redis.call('ZSCORE', expiry_key, token) then
            corrupt = corrupt + 1
        end
        if state == 'CONNECTING' then
            connecting = connecting + 1
        elseif state == 'READY' then
            ready = ready + 1
        elseif state == 'DRAINING' then
            draining = draining + 1
        else
            corrupt = corrupt + 1
        end
    end
    local total = #entries / 2
    if redis.call('ZCARD', expiry_key) ~= total then
        corrupt = corrupt + 1
    end
    return total, connecting, ready, draining, corrupt
end

local function touch_keys()
    if redis.call('HLEN', members_key) == 0 then
        redis.call('DEL', members_key)
        redis.call('DEL', expiry_key)
        return
    end
    local retention_ms = ttl_ms * 3
    redis.call('PEXPIRE', members_key, retention_ms)
    redis.call('PEXPIRE', expiry_key, retention_ms)
end

local function write_member(token, state)
    local expires_ms = now_ms + ttl_ms
    redis.call('HSET', members_key, token, state)
    redis.call('ZADD', expiry_key, expires_ms, token)
    touch_keys()
    return expires_ms
end

local function response(code, state, expires_ms)
    local total, connecting, ready, draining, _ = stats()
    return {code, state or '', total, connecting, ready, draining, now_ms, expires_ms or 0}
end

prune_expired()
`

var redisClaimScript = redis.NewScript(redisLuaPrelude + `
local token = ARGV[1]
local target = tonumber(ARGV[2])
if target ~= 1 and target ~= 2 then
    return response(-5, '', 0)
end

local total, connecting, ready, draining, corrupt = stats()
if corrupt > 0 or draining > 1 then
    return response(-4, '', 0)
end

local current = redis.call('HGET', members_key, token)
if current then
    if current ~= 'CONNECTING' then
        return response(-2, current, tonumber(redis.call('ZSCORE', expiry_key, token)) or 0)
    end
    return response(1, current, write_member(token, current))
end

local non_draining = connecting + ready
if draining == 0 then
    if non_draining >= target or total >= target then
        return response(0, '', 0)
    end
else
    if non_draining >= target or total >= target + 1 then
        return response(0, '', 0)
    end
end

return response(1, 'CONNECTING', write_member(token, 'CONNECTING'))
`)

var redisMarkReadyScript = redis.NewScript(redisLuaPrelude + `
local token = ARGV[1]
local current = redis.call('HGET', members_key, token)
local _, _, _, draining, corrupt = stats()
if corrupt > 0 or draining > 1 then
    return response(-4, current or '', 0)
end
if not current then
    return response(-1, '', 0)
end
if current ~= 'CONNECTING' and current ~= 'READY' then
    return response(-2, current, tonumber(redis.call('ZSCORE', expiry_key, token)) or 0)
end
return response(1, 'READY', write_member(token, 'READY'))
`)

var redisRenewScript = redis.NewScript(redisLuaPrelude + `
local token = ARGV[1]
local expected = ARGV[2]
local current = redis.call('HGET', members_key, token)
local _, _, _, draining, corrupt = stats()
if corrupt > 0 or draining > 1 then
    return response(-4, current or '', 0)
end
if not current then
    return response(-1, '', 0)
end
if current ~= expected then
    return response(-2, current, tonumber(redis.call('ZSCORE', expiry_key, token)) or 0)
end
return response(1, current, write_member(token, current))
`)

var redisBeginDrainScript = redis.NewScript(redisLuaPrelude + `
local token = ARGV[1]
local current = redis.call('HGET', members_key, token)
local _, _, _, draining, corrupt = stats()
if corrupt > 0 or draining > 1 then
    return response(-4, current or '', 0)
end
if not current then
    return response(-1, '', 0)
end
if current == 'DRAINING' then
    return response(1, current, write_member(token, current))
end
if current ~= 'READY' then
    return response(-2, current, tonumber(redis.call('ZSCORE', expiry_key, token)) or 0)
end
if draining > 0 then
    return response(-3, current, tonumber(redis.call('ZSCORE', expiry_key, token)) or 0)
end
return response(1, 'DRAINING', write_member(token, 'DRAINING'))
`)

var redisReleaseScript = redis.NewScript(redisLuaPrelude + `
local token = ARGV[1]
redis.call('HDEL', members_key, token)
redis.call('ZREM', expiry_key, token)
touch_keys()
return 1
`)
