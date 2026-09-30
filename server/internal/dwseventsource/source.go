// Package dwseventsource is the server's DingTalk personal event source:
// DWS event streams (WebSockets), one for every identity some consumer
// needs, whatever the number of hosted identities.
//
// It knows DWS and nothing about its consumers beyond Consumer. Where each
// stream runs is connmgr's business: one connection per identity across
// all replicas, reconnected after network loss, handed over during rolling
// deploys and spread across replicas. A stream is a pkg/dws/events Listener
// whose dedupe, subscription records and status live in Redis, so a stream
// that moves to another replica keeps them. Delivery is at least once: a
// handoff overlaps the old and the new stream, and the dedupe is check then
// mark, so consumers dedupe by event id (the decision service does).
package dwseventsource

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/connmgr"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
	"github.com/multica-ai/multica/server/pkg/dws/redisstore"
)

// Consumer asks for one DWS event kind for a set of identities.
type Consumer struct {
	// EventKey is a dws.Event* key, e.g. dws.EventCardAction.
	EventKey string
	// Identities lists who needs the event now; the source follows it on
	// every sweep. An identity nobody lists any more loses its stream.
	Identities func(ctx context.Context) ([]dwsclient.Identity, error)
	// Handle persists one event for identity, given as the dws CLI's ndjson
	// line (dwsclient.EventLine). It runs on whichever replica holds the
	// stream; an error leaves the event unacknowledged, so it comes again.
	Handle func(ctx context.Context, identity dwsclient.Identity, line []byte) error
}

// Config wires a Source.
type Config struct {
	Redis     *redis.Client
	Consumers []Consumer
	// Sessions carries the app secret and DWS environment of the identities.
	Sessions dwsclient.Shared
	// Mint issues an Agent Identity context for identity and redeems it; it
	// runs only when no shared token exists.
	Mint func(ctx context.Context, identity dwsclient.Identity) (dwsclient.Credential, error)
	// Deployment separates deployments sharing a Redis (the public URL).
	Deployment string
	// Enabled gates the source live (runtime.use_dws_for_tag); off, it holds
	// no streams.
	Enabled func() bool
}

// Source owns this replica's share of the event streams.
type Source struct {
	cfg     Config
	prefix  string
	store   *redisstore.Store
	manager *connmgr.Manager
}

const (
	readyTTL     = 6 * time.Second
	readyRefresh = 2 * time.Second
)

// New builds a Source; Run starts it.
func New(cfg Config) (*Source, error) {
	if cfg.Redis == nil || cfg.Mint == nil {
		return nil, errors.New("dwseventsource: Redis and Mint are required")
	}
	sum := sha256.Sum256([]byte(cfg.Deployment))
	deployment := hex.EncodeToString(sum[:6])
	s := &Source{cfg: cfg, prefix: "multica:dws-events:v1:" + deployment + ":"}
	s.store = &redisstore.Store{Redis: cfg.Redis, Prefix: s.prefix + "state:"}
	coordinator, err := connmgr.NewRedisCoordinator(cfg.Redis, connmgr.RedisCoordinatorConfig{
		KeyPrefix: s.prefix + "conn:", TargetReady: 1,
	})
	if err != nil {
		return nil, err
	}
	s.manager, err = connmgr.New(connmgr.Config{
		Name:        "dws_event_stream",
		List:        s.targets,
		Dial:        s.dial,
		Coordinator: coordinator,
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Source) Run(ctx context.Context) { s.manager.Run(ctx) }
func (s *Source) Kick()                   { s.manager.Kick() }
func (s *Source) BeginShutdown()          { s.manager.BeginShutdown() }
func (s *Source) WaitForHandoffs(timeout time.Duration) bool {
	return s.manager.WaitForHandoffs(timeout)
}
func (s *Source) WaitWithTimeout(timeout time.Duration) bool {
	return s.manager.WaitWithTimeout(timeout)
}
func (s *Source) DrainTimeout() time.Duration    { return s.manager.DrainTimeout() }
func (s *Source) ShutdownTimeout() time.Duration { return s.manager.ShutdownTimeout() }

// Active reports whether the source is switched on.
func (s *Source) Active() bool { return s.cfg.Enabled == nil || s.cfg.Enabled() }

// Ready reports whether some replica holds a connected stream for identity.
func (s *Source) Ready(ctx context.Context, identity dwsclient.Identity) bool {
	n, err := s.cfg.Redis.Exists(ctx, s.readyKey(targetKey(identity))).Result()
	return err == nil && n == 1
}

func (s *Source) readyKey(key string) string { return s.prefix + "ready:" + key }

// targetKey names an identity without its account ids.
func targetKey(id dwsclient.Identity) string {
	sum := sha256.Sum256([]byte(id.AgentID + "\x00" + id.UID + "\x00" + id.OrgID))
	return hex.EncodeToString(sum[:16])
}

// subscription is one identity's stream: who, and which events.
type subscription struct {
	Identity  dwsclient.Identity
	EventKeys []string
}

// targets is every identity some consumer needs, with the union of the
// event keys they need.
func (s *Source) targets(ctx context.Context) ([]connmgr.Target, error) {
	if s.cfg.Enabled != nil && !s.cfg.Enabled() {
		return nil, nil
	}
	wanted := map[string]*subscription{}
	for _, consumer := range s.cfg.Consumers {
		ids, err := consumer.Identities(ctx)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if id.AgentID == "" || id.UID == "" || id.OrgID == "" {
				continue
			}
			key := targetKey(id)
			sub := wanted[key]
			if sub == nil {
				sub = &subscription{Identity: id}
				wanted[key] = sub
			}
			if !contains(sub.EventKeys, consumer.EventKey) {
				sub.EventKeys = append(sub.EventKeys, consumer.EventKey)
			}
		}
	}
	out := make([]connmgr.Target, 0, len(wanted))
	for key, sub := range wanted {
		sort.Strings(sub.EventKeys)
		out = append(out, connmgr.Target{Key: key, Fingerprint: strings.Join(sub.EventKeys, ","), Value: *sub})
	}
	return out, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Source) dial(t connmgr.Target) (connmgr.Conn, error) {
	sub, ok := t.Value.(subscription)
	if !ok || len(sub.EventKeys) == 0 {
		return nil, errors.New("dwseventsource: invalid target")
	}
	return &stream{s: s, key: t.Key, sub: sub}, nil
}

var errDisconnected = errors.New("dwseventsource: event stream failed or disconnected")

// releaseMarker deletes the ready marker only while it is this stream's, so
// a stream ending after a handoff never clears its replacement's marker.
var releaseMarker = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

// stream is one identity's event connection attempt.
type stream struct {
	s   *Source
	key string
	sub subscription
}

// Run keeps the identity's Listener until ctx ends (handed over, lease
// lost, shutdown) or one connection attempt fails, before or after it was
// connected: connmgr then releases the member and claims again with
// backoff, possibly on another replica, so a replica that cannot connect
// never keeps an identity.
func (st *stream) Run(ctx context.Context, ready func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s, id := st.s, st.sub.Identity
	var connected, failed, marked atomic.Bool
	marker := newMarkerValue()
	markerDone := make(chan struct{})
	defer close(markerDone)
	status := &statusStore{Store: s.store, onState: func(state dwsevents.State) {
		switch {
		case state == dwsevents.StateConnected && connected.CompareAndSwap(false, true):
			if ready(ctx) != nil {
				cancel()
				return
			}
			marked.Store(true)
			go st.keepReady(ctx, marker, markerDone)
		case state == dwsevents.StateReconnecting:
			// The Listener would retry by itself; connmgr decides instead.
			failed.Store(true)
			cancel()
		}
	}}
	defer func() {
		if marked.Load() {
			rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = releaseMarker.Run(rctx, s.cfg.Redis, []string{s.readyKey(st.key)}, marker).Err()
			rcancel()
		}
	}()
	specs := make([]dws.SubscriptionSpec, 0, len(st.sub.EventKeys))
	for _, k := range st.sub.EventKeys {
		specs = append(specs, dws.SubscriptionSpec{EventKey: k})
	}
	listener := &dwsevents.Listener{
		Identity: st.key,
		Client: func(ctx context.Context) (*dws.Client, error) {
			return s.cfg.Sessions.Client(ctx, id, func(ctx context.Context) (dwsclient.Credential, error) {
				return s.cfg.Mint(ctx, id)
			})
		},
		Subscriptions: specs,
		Handle: func(ctx context.Context, ev dwsevents.Event) error {
			return s.dispatch(ctx, id, ev)
		},
		Store: status,
	}
	err := listener.Run(ctx)
	if failed.Load() {
		return errDisconnected
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// keepReady publishes the connected state for every replica's Ready.
func (st *stream) keepReady(ctx context.Context, marker string, done <-chan struct{}) {
	t := time.NewTicker(readyRefresh)
	defer t.Stop()
	for {
		_ = st.s.cfg.Redis.Set(ctx, st.s.readyKey(st.key), marker, readyTTL).Err()
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-t.C:
		}
	}
}

func newMarkerValue() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// dispatch hands an event to the consumers of its key. An event of a key no
// consumer needs any more is acknowledged.
func (s *Source) dispatch(ctx context.Context, id dwsclient.Identity, ev dwsevents.Event) error {
	slog.Info("DWS event received", "event", "dws_event_received", "agent_id", id.AgentID,
		"event_key", ev.Key, "event_id", ev.ID, "malformed", ev.Malformed)
	line := dwsclient.EventLine(ev)
	for _, consumer := range s.cfg.Consumers {
		if consumer.EventKey == ev.Key || (ev.Malformed && ev.Key == "") {
			if err := consumer.Handle(ctx, id, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// statusStore reports the Listener's state transitions.
type statusStore struct {
	*redisstore.Store
	onState func(dwsevents.State)
}

func (s *statusStore) SetStatus(ctx context.Context, st dwsevents.Status) error {
	s.onState(st.State)
	return s.Store.SetStatus(ctx, st)
}
