package dwseventsource

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
	"github.com/multica-ai/multica/server/pkg/dws/redisstore"
)

func testSource(t *testing.T, consumers []Consumer, enabled bool) *Source {
	t.Helper()
	s, err := New(Config{
		Redis:     redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}), // never dialled here
		Consumers: consumers,
		Mint: func(context.Context, dwsclient.Identity) (dwsclient.Credential, error) {
			return dwsclient.Credential{}, nil
		},
		Deployment: "https://pre.example",
		Enabled:    func() bool { return enabled },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func identities(ids ...dwsclient.Identity) func(context.Context) ([]dwsclient.Identity, error) {
	return func(context.Context) ([]dwsclient.Identity, error) { return ids, nil }
}

// Every hosted identity a consumer needs is one target carrying all its
// event keys; targets are stable across sweeps and name no account ids.
func TestTargetsFollowConsumersForManyIdentities(t *testing.T) {
	var ids []dwsclient.Identity
	for _, uid := range []string{"41", "42", "43", "44"} {
		ids = append(ids, dwsclient.Identity{AgentID: "agent-" + uid, UID: uid, OrgID: "org-1"})
	}
	a := ids[0]
	s := testSource(t, []Consumer{
		{EventKey: dws.EventCardAction, Identities: identities(append(ids, dwsclient.Identity{UID: "incomplete"})...)},
		{EventKey: dws.EventIMAt, Identities: identities(a)},
	}, true)
	got, err := s.targets(context.Background())
	if err != nil || len(got) != 4 {
		t.Fatalf("targets = %v, %v", got, err)
	}
	for _, target := range got {
		sub := target.Value.(subscription)
		if target.Key != targetKey(sub.Identity) || len(target.Key) != 32 {
			t.Fatalf("target key %q", target.Key)
		}
		raw, _ := json.Marshal(target.Key)
		if json.Valid(raw) && (containsAny(target.Key, sub.Identity.UID, sub.Identity.AgentID)) {
			t.Fatalf("target key names the account: %q", target.Key)
		}
		if sub.Identity == a && (len(sub.EventKeys) != 2 || target.Fingerprint != dws.EventIMAt+","+dws.EventCardAction && target.Fingerprint != dws.EventCardAction+","+dws.EventIMAt) {
			t.Fatalf("a = %+v %q", sub, target.Fingerprint)
		}
	}
	again, _ := s.targets(context.Background())
	seen := map[string]string{}
	for _, target := range got {
		seen[target.Key] = target.Fingerprint
	}
	for _, target := range again {
		if seen[target.Key] != target.Fingerprint {
			t.Fatal("targets are not stable across sweeps")
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 3 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func TestSwitchedOffHoldsNoStreams(t *testing.T) {
	s := testSource(t, []Consumer{{EventKey: dws.EventCardAction,
		Identities: identities(dwsclient.Identity{AgentID: "a", UID: "1", OrgID: "o"})}}, false)
	if got, _ := s.targets(context.Background()); len(got) != 0 {
		t.Fatalf("switched off: %v", got)
	}
}

func TestAConsumerFailureIsReported(t *testing.T) {
	boom := errors.New("db down")
	s := testSource(t, []Consumer{{EventKey: dws.EventCardAction,
		Identities: func(context.Context) ([]dwsclient.Identity, error) { return nil, boom }}}, true)
	if _, err := s.targets(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// Events reach the consumers of their key as the dws CLI's ndjson line.
func TestDispatchByEventKey(t *testing.T) {
	var got []string
	s := testSource(t, []Consumer{
		{EventKey: dws.EventCardAction, Identities: identities(), Handle: func(_ context.Context, id dwsclient.Identity, line []byte) error {
			got = append(got, "card:"+id.UID+":"+string(line[:15]))
			return nil
		}},
		{EventKey: dws.EventIMAt, Identities: identities(), Handle: func(context.Context, dwsclient.Identity, []byte) error {
			got = append(got, "at")
			return errors.New("persist failed")
		}},
	}, true)
	id := dwsclient.Identity{AgentID: "a", UID: "42", OrgID: "o"}
	if err := s.dispatch(context.Background(), id, dwsevents.Event{ID: "e1", Key: dws.EventCardAction, Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.dispatch(context.Background(), id, dwsevents.Event{ID: "e2", Key: dws.EventIMAt}); err == nil {
		t.Fatal("a consumer failure must leave the event unacknowledged")
	}
	if err := s.dispatch(context.Background(), id, dwsevents.Event{ID: "e3", Key: "user_other_event"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != `card:42:{"type":"event"` || got[1] != "at" {
		t.Fatalf("dispatched = %v", got)
	}
}

// A stream authenticates and dispatches as its account's current agent: the
// agent it was dialled for may have stopped needing it.
func TestStreamsFollowTheAccountsCurrentAgent(t *testing.T) {
	first := dwsclient.Identity{AgentID: "agent-1", UID: "42", OrgID: "org-1"}
	second := dwsclient.Identity{AgentID: "agent-2", UID: "42", OrgID: "org-1"}
	listed := []dwsclient.Identity{first}
	s := testSource(t, []Consumer{{EventKey: dws.EventCardAction,
		Identities: func(context.Context) ([]dwsclient.Identity, error) { return listed, nil }}}, true)
	targets, err := s.targets(context.Background())
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %v, %v", targets, err)
	}
	key := targets[0].Key
	if got := s.identity(key, first); got != first {
		t.Fatalf("identity = %+v", got)
	}
	// The first agent's decision resolved; another agent of the account
	// still needs the stream, which keeps its key and fingerprint.
	listed = []dwsclient.Identity{second}
	again, _ := s.targets(context.Background())
	if len(again) != 1 || again[0].Key != key || again[0].Fingerprint != targets[0].Fingerprint {
		t.Fatalf("the stream must not restart for a new agent: %v", again)
	}
	if got := s.identity(key, first); got != second {
		t.Fatalf("identity = %+v, want the account's current agent", got)
	}
	// A key the latest sweep did not list keeps the dialled identity.
	if got := s.identity("unknown", first); got != first {
		t.Fatalf("identity = %+v", got)
	}
}

// redisTestDB is dedicated to this package (see connmgr's REDIS_TEST_URL
// suites for the others).
const redisTestDB = 9

// newRedisTestClient returns a client on the flushed test DB, or skips when
// REDIS_TEST_URL is unset or unreachable.
func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	rawURL := os.Getenv("REDIS_TEST_URL")
	if rawURL == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	opts.DB = redisTestDB
	client := redis.NewClient(opts)
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		t.Skipf("REDIS_TEST_URL unreachable: %v", err)
	}
	if err := client.FlushDB(ctx).Err(); err != nil {
		client.Close()
		t.Fatalf("flush Redis test DB: %v", err)
	}
	t.Cleanup(func() {
		_ = client.FlushDB(context.Background()).Err()
		_ = client.Close()
	})
	return client
}

// StreamStatus joins every replica's view: the ready marker says whether a
// stream is up, the stored status what it last reported.
func TestStreamStatusReadsTheSharedState(t *testing.T) {
	client := newRedisTestClient(t)
	ctx := context.Background()
	s, err := New(Config{
		Redis: client,
		Mint: func(context.Context, dwsclient.Identity) (dwsclient.Credential, error) {
			return dwsclient.Credential{}, nil
		},
		Deployment: "native-v2:https://pre.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := dwsclient.Identity{AgentID: "agent-1", UID: "42", OrgID: "org-1"}

	if got, err := s.StreamStatus(ctx, id); err != nil || got.Connected || got.Reported {
		t.Fatalf("no stream yet: %+v, %v", got, err)
	}
	failed := dwsevents.Status{Identity: targetKey(id), State: dwsevents.StateReconnecting, LastError: "dial: refused", Failures: 2}
	if err := s.store.SetStatus(ctx, failed); err != nil {
		t.Fatal(err)
	}
	got, err := s.StreamStatus(ctx, id)
	if err != nil || got.Connected || !got.Reported || got.Status.LastError != "dial: refused" || got.Status.Failures != 2 {
		t.Fatalf("failing stream: %+v, %v", got, err)
	}
	if err := claimMarker.Run(ctx, client, []string{s.readyKey(targetKey(id))}, "m", readyTTL.Milliseconds()).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.StreamStatus(ctx, id); err != nil || !got.Connected {
		t.Fatalf("connected stream: %+v, %v", got, err)
	}
	// Another account's stream is not this one's.
	other := dwsclient.Identity{AgentID: "agent-1", UID: "43", OrgID: "org-1"}
	if got, err := s.StreamStatus(ctx, other); err != nil || got.Connected || got.Reported {
		t.Fatalf("another account: %+v, %v", got, err)
	}
}

// Each attempt is a new Listener that starts its status afresh but continues
// the outage: the stored status keeps the outage's last error and counts its
// failures until a connection proves healthy, and never carries the secret.
func TestStatusKeepsTheOutageAcrossAttempts(t *testing.T) {
	client := newRedisTestClient(t)
	ctx := context.Background()
	store := &redisstore.Store{Redis: client, Prefix: "multica:dws-events-test:state:"}
	key := "stream-1"
	down := time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)
	stored := func() dwsevents.Status {
		t.Helper()
		st, ok, err := store.Status(ctx, key)
		if err != nil || !ok {
			t.Fatalf("status = %+v %v %v", st, ok, err)
		}
		return st
	}
	attempt := func() *statusStore {
		return &statusStore{Store: store, secret: "app-secret", onState: func(dwsevents.State) {}}
	}

	first := attempt()
	_ = first.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateConnecting, DownSince: down})
	_ = first.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateReconnecting, DownSince: down,
		LastError: "ticket: client_secret=app-secret rejected", Failures: 1})
	if st := stored(); st.Failures != 1 || st.LastError != "ticket: client_secret=[redacted] rejected" {
		t.Fatalf("first attempt = %+v", st)
	}

	second := attempt()
	_ = second.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateConnecting, DownSince: down})
	if st := stored(); st.Failures != 1 || st.LastError == "" || st.State != dwsevents.StateConnecting {
		t.Fatalf("a new attempt dropped the outage: %+v", st)
	}
	_ = second.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateReconnecting, DownSince: down,
		LastError: "dial: refused", Failures: 1})
	if st := stored(); st.Failures != 2 || st.LastError != "dial: refused" {
		t.Fatalf("second attempt = %+v", st)
	}

	third := attempt()
	_ = third.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateConnected, DownSince: down})
	_ = third.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateConnected})
	if st := stored(); st.Failures != 0 || st.LastError != "" {
		t.Fatalf("a healthy connection ends the outage: %+v", st)
	}

	// A later outage starts its own count.
	fourth := attempt()
	_ = fourth.SetStatus(ctx, dwsevents.Status{Identity: key, State: dwsevents.StateConnecting, DownSince: down.Add(time.Hour)})
	if st := stored(); st.Failures != 0 || st.LastError != "" {
		t.Fatalf("a new outage = %+v", st)
	}
}

// A changed credential version restarts the identity's stream, under the
// same key.
func TestCredentialVersionRestartsTheStream(t *testing.T) {
	id := dwsclient.Identity{AgentID: "agent-1", UID: "42", OrgID: "org-1"}
	listed := []dwsclient.Identity{id}
	s := testSource(t, []Consumer{{EventKey: dws.EventIMAt,
		Identities: func(context.Context) ([]dwsclient.Identity, error) { return listed, nil }}}, true)
	before, err := s.targets(context.Background())
	if err != nil || len(before) != 1 {
		t.Fatalf("targets = %v, %v", before, err)
	}
	id.CredentialVersion = "deap:employee:supervisor"
	listed = []dwsclient.Identity{id}
	after, _ := s.targets(context.Background())
	if len(after) != 1 || after[0].Key != before[0].Key || after[0].Fingerprint == before[0].Fingerprint {
		t.Fatalf("before %+v after %+v", before, after)
	}
	if got := s.identity(after[0].Key, dwsclient.Identity{}); got.CredentialVersion != id.CredentialVersion {
		t.Fatalf("stream identity = %+v", got)
	}
}
