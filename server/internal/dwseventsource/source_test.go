package dwseventsource

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
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
