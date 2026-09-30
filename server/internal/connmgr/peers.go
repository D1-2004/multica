package connmgr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// PeerLoad is one live replica and the READY count it last reported.
type PeerLoad struct {
	Node string
	Held int
}

// Peers is the replica membership a Manager uses to rebalance. Every sweep a
// Manager heartbeats its node id and READY count with a TTL of three poll
// intervals; a replica that stops heartbeating drops out once the TTL passes,
// and a gracefully stopping replica leaves at once.
type Peers interface {
	// Heartbeat records node as alive, holding held READY connections, until
	// ttl passes without another heartbeat.
	Heartbeat(ctx context.Context, node string, held int, ttl time.Duration) error
	// Alive returns every live node's last report (expired nodes are pruned).
	Alive(ctx context.Context) ([]PeerLoad, error)
	// Leave removes node immediately.
	Leave(ctx context.Context, node string) error
}

// RedisPeersConfig names the membership record.
type RedisPeersConfig struct {
	// KeyPrefix is the namespace shared with the coordinator, e.g.
	// "multica:dws-events:v1:". Required; must not contain '{' or '}'.
	KeyPrefix string
	// Name is the Manager's Config.Name. The record lives at
	// KeyPrefix+"nodes:"+Name.
	Name string
}

// RedisPeers keeps membership in one Redis HASH (node -> "expiry_ms:held").
// Expiry is computed from Redis TIME, so replica clocks never matter, and
// expired nodes are pruned on read.
type RedisPeers struct {
	redis redis.Scripter
	key   string
}

var _ Peers = (*RedisPeers)(nil)

// NewRedisPeers validates cfg and returns the Redis membership record.
func NewRedisPeers(client redis.Scripter, cfg RedisPeersConfig) (*RedisPeers, error) {
	switch {
	case client == nil:
		return nil, fmt.Errorf("%w: Redis client is required", ErrConfig)
	case cfg.KeyPrefix == "" || strings.ContainsAny(cfg.KeyPrefix, "{}"):
		return nil, fmt.Errorf("%w: peers key prefix is required and must not contain '{' or '}'", ErrConfig)
	case strings.TrimSpace(cfg.Name) == "" || strings.ContainsAny(cfg.Name, "{}"):
		return nil, fmt.Errorf("%w: peers name is required and must not contain '{' or '}'", ErrConfig)
	}
	return &RedisPeers{redis: client, key: cfg.KeyPrefix + "nodes:" + cfg.Name}, nil
}

// Key is the Redis key holding the membership record.
func (p *RedisPeers) Key() string { return p.key }

func (p *RedisPeers) Heartbeat(ctx context.Context, node string, held int, ttl time.Duration) error {
	if err := validatePeerNode(node); err != nil {
		return err
	}
	if held < 0 || ttl < time.Millisecond {
		return fmt.Errorf("%w: heartbeat held must be >= 0 and ttl >= 1ms", ErrConfig)
	}
	if err := redisPeersHeartbeatScript.Run(ctx, p.redis, []string{p.key}, node, held, ttl.Milliseconds()).Err(); err != nil {
		return fmt.Errorf("connmgr peers heartbeat: %w", err)
	}
	return nil
}

func (p *RedisPeers) Alive(ctx context.Context) ([]PeerLoad, error) {
	raw, err := redisPeersAliveScript.Run(ctx, p.redis, []string{p.key}).Result()
	if err != nil {
		return nil, fmt.Errorf("connmgr peers alive: %w", err)
	}
	values, ok := raw.([]any)
	if !ok || len(values)%2 != 0 {
		return nil, fmt.Errorf("connmgr peers alive: %w: unexpected Lua result shape %T", ErrCorruptState, raw)
	}
	out := make([]PeerLoad, 0, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		node, err := scriptString(values[i])
		if err != nil {
			return nil, fmt.Errorf("connmgr peers alive: %w: %v", ErrCorruptState, err)
		}
		heldText, err := scriptString(values[i+1])
		if err != nil {
			return nil, fmt.Errorf("connmgr peers alive: %w: %v", ErrCorruptState, err)
		}
		held, err := strconv.Atoi(heldText)
		if err != nil || held < 0 {
			return nil, fmt.Errorf("connmgr peers alive: %w: bad load for a node", ErrCorruptState)
		}
		out = append(out, PeerLoad{Node: node, Held: held})
	}
	return out, nil
}

func (p *RedisPeers) Leave(ctx context.Context, node string) error {
	if err := validatePeerNode(node); err != nil {
		return err
	}
	if err := redisPeersLeaveScript.Run(ctx, p.redis, []string{p.key}, node).Err(); err != nil {
		return fmt.Errorf("connmgr peers leave: %w", err)
	}
	return nil
}

func validatePeerNode(node string) error {
	if node == "" || len(node) > maxTokenLen {
		return fmt.Errorf("%w: peer node id length is invalid", ErrConfig)
	}
	return nil
}

const redisPeersClock = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
`

// The record's own expiry only ever grows, so a replica with a shorter poll
// interval cannot expire a slower replica's entry.
var redisPeersHeartbeatScript = redis.NewScript(redisPeersClock + `
local ttl_ms = tonumber(ARGV[3])
redis.call('HSET', KEYS[1], ARGV[1], tostring(now_ms + ttl_ms) .. ':' .. ARGV[2])
local retention_ms = ttl_ms * 3
if redis.call('PTTL', KEYS[1]) < retention_ms then
    redis.call('PEXPIRE', KEYS[1], retention_ms)
end
return 1
`)

var redisPeersAliveScript = redis.NewScript(redisPeersClock + `
local entries = redis.call('HGETALL', KEYS[1])
local out = {}
for index = 1, #entries, 2 do
    local node = entries[index]
    local value = entries[index + 1]
    local sep = string.find(value, ':', 1, true)
    local expires_ms = nil
    if sep then
        expires_ms = tonumber(string.sub(value, 1, sep - 1))
    end
    if not expires_ms or expires_ms <= now_ms then
        redis.call('HDEL', KEYS[1], node)
    else
        out[#out + 1] = node
        out[#out + 1] = string.sub(value, sep + 1)
    end
end
return out
`)

var redisPeersLeaveScript = redis.NewScript(`
redis.call('HDEL', KEYS[1], ARGV[1])
return 1
`)
