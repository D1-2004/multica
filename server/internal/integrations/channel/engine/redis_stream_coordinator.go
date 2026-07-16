package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/util"
)

const (
	// DefaultRedisStreamLeaseTTL is deliberately short: a process that exits
	// without releasing its member should be replaceable in seconds, not after
	// the legacy PostgreSQL lease's 90-second window.
	DefaultRedisStreamLeaseTTL = 10 * time.Second

	redisStreamKeyPrefix = "multica:dingtalk-stream:v1:"
	maxStreamLeaseToken  = 512
)

// StreamLeaseState is the Redis-visible lifecycle of one physical Stream
// connection. State transitions are monotonic:
//
//	CONNECTING -> READY -> DRAINING
//
// A DRAINING member remains connected and consumes callbacks until the
// coordinator reports that the configured READY target is met without it.
type StreamLeaseState string

const (
	StreamLeaseConnecting StreamLeaseState = "CONNECTING"
	StreamLeaseReady      StreamLeaseState = "READY"
	StreamLeaseDraining   StreamLeaseState = "DRAINING"
)

var (
	// ErrStreamLeaseLost means the token is no longer present. The caller must
	// tear its connection down; continuing after this error can exceed the
	// globally coordinated connection limit.
	ErrStreamLeaseLost = errors.New("stream coordinator: lease lost")

	// ErrStreamLeaseStateMismatch means the token still exists but is not in
	// the state required by the requested transition.
	ErrStreamLeaseStateMismatch = errors.New("stream coordinator: lease state mismatch")

	// ErrStreamDrainInProgress serializes rolling handoffs. Only one member per
	// installation may be DRAINING, which caps a target-two handoff at three
	// physical connections.
	ErrStreamDrainInProgress = errors.New("stream coordinator: another drain is in progress")

	// ErrStreamCoordinatorCorruptState indicates that the shared Redis record
	// contains an unknown state or more than one DRAINING member.
	ErrStreamCoordinatorCorruptState = errors.New("stream coordinator: corrupt shared state")

	// ErrStreamCoordinatorConfig is the sentinel for invalid constructor or
	// call inputs. It is intentionally distinct from Redis transport errors.
	ErrStreamCoordinatorConfig = errors.New("stream coordinator: invalid configuration")
)

// StreamCoordinatorError carries machine-readable failure information without
// including the lease token or any platform credential.
type StreamCoordinatorError struct {
	Op             string
	InstallationID string
	Expected       StreamLeaseState
	Actual         StreamLeaseState
	Kind           error
}

func (e *StreamCoordinatorError) Error() string {
	if e == nil {
		return "stream coordinator: <nil>"
	}
	message := "stream coordinator " + e.Op
	if e.InstallationID != "" {
		message += " for installation " + e.InstallationID
	}
	if e.Expected != "" || e.Actual != "" {
		message += fmt.Sprintf(": expected state %q, actual %q", e.Expected, e.Actual)
	}
	if e.Kind != nil {
		message += ": " + e.Kind.Error()
	}
	return message
}

func (e *StreamCoordinatorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

// StreamLease is the token-fenced identity of one coordinated connection.
// Callers must use the value returned by MarkReady/BeginDrain after a state
// transition; Renew checks both Token and State.
type StreamLease struct {
	InstallationID pgtype.UUID
	Token          string
	State          StreamLeaseState
}

// StreamLeaseSnapshot is returned by every successful coordination operation.
// ServerNow and ExpiresAt are both derived from Redis TIME, so their difference
// is safe to turn into a local monotonic deadline even when app-node clocks
// differ.
type StreamLeaseSnapshot struct {
	TargetReady int
	State       StreamLeaseState
	Total       int
	Connecting  int
	Ready       int
	Draining    int
	ServerNow   time.Time
	ExpiresAt   time.Time
}

// ReadyTargetMet reports whether a DRAINING caller can close without reducing
// the remaining READY population below the configured target.
func (s StreamLeaseSnapshot) ReadyTargetMet() bool {
	return s.TargetReady > 0 && s.Ready >= s.TargetReady
}

// RemainingTTL converts Redis server timestamps into a duration. Supervisors
// should add this duration to time.Now() and fail closed when that local
// monotonic deadline elapses without a successful Renew.
func (s StreamLeaseSnapshot) RemainingTTL() time.Duration {
	if s.ServerNow.IsZero() || s.ExpiresAt.IsZero() || !s.ExpiresAt.After(s.ServerNow) {
		return 0
	}
	return s.ExpiresAt.Sub(s.ServerNow)
}

// StreamCoordinator is the narrow lifecycle seam consumed by Supervisor.
// Capacity exhaustion is a normal Claim result (acquired=false), not an error.
type StreamCoordinator interface {
	Claim(ctx context.Context, installationID pgtype.UUID, token string) (lease StreamLease, snapshot StreamLeaseSnapshot, acquired bool, err error)
	MarkReady(ctx context.Context, lease StreamLease) (StreamLease, StreamLeaseSnapshot, error)
	Renew(ctx context.Context, lease StreamLease) (StreamLeaseSnapshot, error)
	BeginDrain(ctx context.Context, lease StreamLease) (StreamLease, StreamLeaseSnapshot, error)
	Release(ctx context.Context, lease StreamLease) error
	TargetReady() int
	LeaseTTL() time.Duration
}

// RedisStreamCoordinatorConfig fixes the global steady-state READY target for
// one coordinator instance. TargetReady must be 1 or 2. A zero LeaseTTL uses
// DefaultRedisStreamLeaseTTL; non-zero values primarily support deterministic
// expiry tests.
type RedisStreamCoordinatorConfig struct {
	TargetReady int
	LeaseTTL    time.Duration
}

// RedisStreamCoordinator atomically maintains a small member HASH and expiry
// ZSET. The two keys use the same Redis Cluster hash tag, and every mutation is
// performed by Lua with Redis TIME as the sole shared clock.
type RedisStreamCoordinator struct {
	redis       redis.Scripter
	targetReady int
	leaseTTL    time.Duration
	ttlMillis   int64
}

var _ StreamCoordinator = (*RedisStreamCoordinator)(nil)

// NewRedisStreamCoordinator returns an error rather than selecting another
// lease backend. Redis is the sole authority once this coordinator is enabled.
func NewRedisStreamCoordinator(client redis.Scripter, cfg RedisStreamCoordinatorConfig) (*RedisStreamCoordinator, error) {
	if client == nil {
		return nil, &StreamCoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: Redis client is required", ErrStreamCoordinatorConfig)}
	}
	if cfg.TargetReady != 1 && cfg.TargetReady != 2 {
		return nil, &StreamCoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: target_ready must be 1 or 2", ErrStreamCoordinatorConfig)}
	}
	ttl := cfg.LeaseTTL
	if ttl == 0 {
		ttl = DefaultRedisStreamLeaseTTL
	}
	if ttl < time.Millisecond {
		return nil, &StreamCoordinatorError{Op: "configure", Kind: fmt.Errorf("%w: lease_ttl must be at least 1ms", ErrStreamCoordinatorConfig)}
	}
	return &RedisStreamCoordinator{
		redis:       client,
		targetReady: cfg.TargetReady,
		leaseTTL:    ttl,
		ttlMillis:   ttl.Milliseconds(),
	}, nil
}

func (c *RedisStreamCoordinator) TargetReady() int        { return c.targetReady }
func (c *RedisStreamCoordinator) LeaseTTL() time.Duration { return c.leaseTTL }

// Claim reserves one CONNECTING member. CONNECTING counts against capacity,
// preventing concurrent dials from overshooting the target. During a handoff,
// one DRAINING member grants exactly one extra total slot: target one permits
// two connections; target two permits three.
func (c *RedisStreamCoordinator) Claim(ctx context.Context, installationID pgtype.UUID, token string) (StreamLease, StreamLeaseSnapshot, bool, error) {
	id, err := validateStreamLeaseIdentity(installationID, token)
	if err != nil {
		return StreamLease{}, StreamLeaseSnapshot{}, false, err
	}
	result, err := c.run(ctx, "claim", redisStreamClaimScript, id, token, c.targetReady, c.ttlMillis)
	if err != nil {
		return StreamLease{}, StreamLeaseSnapshot{}, false, err
	}
	snapshot := c.snapshot(result)
	switch result.code {
	case streamScriptCapacity:
		return StreamLease{}, snapshot, false, nil
	case streamScriptOK:
		lease := StreamLease{InstallationID: installationID, Token: token, State: StreamLeaseConnecting}
		return lease, snapshot, true, nil
	default:
		return StreamLease{}, snapshot, false, c.resultError("claim", id, StreamLeaseConnecting, result)
	}
}

// MarkReady records that the platform WebSocket has successfully dialled.
// Callers must not consume callback frames before this succeeds.
func (c *RedisStreamCoordinator) MarkReady(ctx context.Context, lease StreamLease) (StreamLease, StreamLeaseSnapshot, error) {
	id, err := validateStreamLease(lease)
	if err != nil {
		return StreamLease{}, StreamLeaseSnapshot{}, err
	}
	result, err := c.run(ctx, "mark_ready", redisStreamMarkReadyScript, id, lease.Token, c.ttlMillis)
	if err != nil {
		return StreamLease{}, StreamLeaseSnapshot{}, err
	}
	if result.code != streamScriptOK {
		return StreamLease{}, c.snapshot(result), c.resultError("mark_ready", id, StreamLeaseConnecting, result)
	}
	lease.State = StreamLeaseReady
	return lease, c.snapshot(result), nil
}

// Renew extends an existing member only when both token and state still match.
func (c *RedisStreamCoordinator) Renew(ctx context.Context, lease StreamLease) (StreamLeaseSnapshot, error) {
	id, err := validateStreamLease(lease)
	if err != nil {
		return StreamLeaseSnapshot{}, err
	}
	result, err := c.run(ctx, "renew", redisStreamRenewScript, id, lease.Token, string(lease.State), c.ttlMillis)
	if err != nil {
		return StreamLeaseSnapshot{}, err
	}
	if result.code != streamScriptOK {
		return c.snapshot(result), c.resultError("renew", id, lease.State, result)
	}
	return c.snapshot(result), nil
}

// BeginDrain serializes graceful handoffs and keeps the old member alive while
// a replacement claims the one extra slot and reaches READY.
func (c *RedisStreamCoordinator) BeginDrain(ctx context.Context, lease StreamLease) (StreamLease, StreamLeaseSnapshot, error) {
	id, err := validateStreamLease(lease)
	if err != nil {
		return StreamLease{}, StreamLeaseSnapshot{}, err
	}
	result, err := c.run(ctx, "begin_drain", redisStreamBeginDrainScript, id, lease.Token, c.ttlMillis)
	if err != nil {
		return StreamLease{}, StreamLeaseSnapshot{}, err
	}
	if result.code != streamScriptOK {
		return StreamLease{}, c.snapshot(result), c.resultError("begin_drain", id, StreamLeaseReady, result)
	}
	lease.State = StreamLeaseDraining
	return lease, c.snapshot(result), nil
}

// Release removes only the supplied token. A stale predecessor cannot delete
// a successor because each connection generation has a distinct token.
func (c *RedisStreamCoordinator) Release(ctx context.Context, lease StreamLease) error {
	id, err := validateStreamLeaseIdentity(lease.InstallationID, lease.Token)
	if err != nil {
		return err
	}
	if _, err := redisStreamReleaseScript.Run(ctx, c.redis, c.keys(id), lease.Token, c.ttlMillis).Result(); err != nil {
		return &StreamCoordinatorError{Op: "release", InstallationID: id, Kind: err}
	}
	return nil
}

func (c *RedisStreamCoordinator) keys(installationID string) []string {
	base := redisStreamKeyPrefix + "{" + installationID + "}"
	return []string{base + ":members", base + ":expiry"}
}

func (c *RedisStreamCoordinator) run(ctx context.Context, op string, script *redis.Script, installationID string, args ...any) (streamScriptResult, error) {
	raw, err := script.Run(ctx, c.redis, c.keys(installationID), args...).Result()
	if err != nil {
		return streamScriptResult{}, &StreamCoordinatorError{Op: op, InstallationID: installationID, Kind: err}
	}
	result, err := decodeStreamScriptResult(raw)
	if err != nil {
		return streamScriptResult{}, &StreamCoordinatorError{Op: op, InstallationID: installationID, Kind: fmt.Errorf("%w: %v", ErrStreamCoordinatorCorruptState, err)}
	}
	return result, nil
}

func (c *RedisStreamCoordinator) snapshot(result streamScriptResult) StreamLeaseSnapshot {
	return StreamLeaseSnapshot{
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

func (c *RedisStreamCoordinator) resultError(op, installationID string, expected StreamLeaseState, result streamScriptResult) error {
	kind := ErrStreamCoordinatorCorruptState
	switch result.code {
	case streamScriptLost:
		kind = ErrStreamLeaseLost
	case streamScriptStateMismatch:
		kind = ErrStreamLeaseStateMismatch
	case streamScriptDrainInProgress:
		kind = ErrStreamDrainInProgress
	case streamScriptInvalidTarget:
		kind = ErrStreamCoordinatorConfig
	}
	return &StreamCoordinatorError{
		Op:             op,
		InstallationID: installationID,
		Expected:       expected,
		Actual:         result.state,
		Kind:           kind,
	}
}

func validateStreamLease(lease StreamLease) (string, error) {
	id, err := validateStreamLeaseIdentity(lease.InstallationID, lease.Token)
	if err != nil {
		return "", err
	}
	switch lease.State {
	case StreamLeaseConnecting, StreamLeaseReady, StreamLeaseDraining:
		return id, nil
	default:
		return "", &StreamCoordinatorError{
			Op:             "validate",
			InstallationID: id,
			Actual:         lease.State,
			Kind:           fmt.Errorf("%w: unknown lease state", ErrStreamCoordinatorConfig),
		}
	}
}

func validateStreamLeaseIdentity(installationID pgtype.UUID, token string) (string, error) {
	id := util.UUIDToString(installationID)
	if id == "" {
		return "", &StreamCoordinatorError{Op: "validate", Kind: fmt.Errorf("%w: installation_id is required", ErrStreamCoordinatorConfig)}
	}
	if token == "" || len(token) > maxStreamLeaseToken {
		return "", &StreamCoordinatorError{Op: "validate", InstallationID: id, Kind: fmt.Errorf("%w: lease token length is invalid", ErrStreamCoordinatorConfig)}
	}
	return id, nil
}

func unixMilliOrZero(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

const (
	streamScriptOK              int64 = 1
	streamScriptCapacity        int64 = 0
	streamScriptLost            int64 = -1
	streamScriptStateMismatch   int64 = -2
	streamScriptDrainInProgress int64 = -3
	streamScriptCorrupt         int64 = -4
	streamScriptInvalidTarget   int64 = -5
)

type streamScriptResult struct {
	code          int64
	state         StreamLeaseState
	total         int64
	connecting    int64
	ready         int64
	draining      int64
	nowMillis     int64
	expiresMillis int64
}

func decodeStreamScriptResult(raw any) (streamScriptResult, error) {
	values, ok := raw.([]any)
	if !ok || len(values) != 8 {
		return streamScriptResult{}, fmt.Errorf("unexpected Lua result shape %T", raw)
	}
	ints := make([]int64, 0, 7)
	for _, index := range []int{0, 2, 3, 4, 5, 6, 7} {
		value, err := streamScriptInt64(values[index])
		if err != nil {
			return streamScriptResult{}, fmt.Errorf("result field %d: %w", index, err)
		}
		ints = append(ints, value)
	}
	state, err := streamScriptString(values[1])
	if err != nil {
		return streamScriptResult{}, fmt.Errorf("result state: %w", err)
	}
	return streamScriptResult{
		code:          ints[0],
		state:         StreamLeaseState(state),
		total:         ints[1],
		connecting:    ints[2],
		ready:         ints[3],
		draining:      ints[4],
		nowMillis:     ints[5],
		expiresMillis: ints[6],
	}, nil
}

func streamScriptInt64(value any) (int64, error) {
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

func streamScriptString(value any) (string, error) {
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
const redisStreamLuaPrelude = `
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

var redisStreamClaimScript = redis.NewScript(redisStreamLuaPrelude + `
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

var redisStreamMarkReadyScript = redis.NewScript(redisStreamLuaPrelude + `
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

var redisStreamRenewScript = redis.NewScript(redisStreamLuaPrelude + `
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

var redisStreamBeginDrainScript = redis.NewScript(redisStreamLuaPrelude + `
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

var redisStreamReleaseScript = redis.NewScript(redisStreamLuaPrelude + `
local token = ARGV[1]
redis.call('HDEL', members_key, token)
redis.call('ZREM', expiry_key, token)
touch_keys()
return 1
`)
