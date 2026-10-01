package dws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var namedKeys = []string{
	EventIMAt, EventIMSingleChat, EventIMGroup, EventIMFromUser, EventIMAllSingleChats, EventIMAllGroups,
	EventIMReadSingleChat, EventIMReadGroup, EventIMRecallSingle, EventIMRecallGroup, EventIMReactionSingle, EventIMReactionGroup,
	EventGroupUpdated, EventGroupMemberAdded, EventGroupMemberExited, EventGroupDisbanded,
	EventApprovalTaskNew, EventApprovalTaskDone, EventApprovalTaskMoved, EventApprovalStarted, EventApprovalCC,
	EventApprovalStopped, EventApprovalFinished, EventVoIPInvite, EventTodoCreated, EventTodoUpdated, EventTodoDeleted, EventCardAction,
}

// Every named key is in the generated catalog and can be subscribed.
func TestCatalogCoversTheNamedKeys(t *testing.T) {
	for _, key := range namedKeys {
		def, ok := LookupEvent(key)
		if !ok || !def.Available() {
			t.Errorf("%s: in catalog=%v %+v", key, ok, def)
		}
	}
	if got := len(EventKeys()); got != len(namedKeys) {
		t.Errorf("available keys = %d, named = %d", got, len(namedKeys))
	}
}

// Each catalog key builds a rule from the scope its definition asks for.
func TestEveryCatalogKeyBuildsARule(t *testing.T) {
	for _, def := range EventCatalog() {
		spec := SubscriptionSpec{EventKey: def.Key}
		switch def.Scope() {
		case ScopeGroup:
			spec.ConversationID = "cid"
		case ScopeTarget:
			spec.TargetOpenDingTalkID = "oid"
		}
		ruleType, _, err := spec.Rule()
		if err != nil || ruleType != def.RuleType || spec.Fingerprint() == "" {
			t.Errorf("%s (%s): rule %q, %v", def.Key, def.Scope(), ruleType, err)
		}
		if def.Scope() != ScopeNone {
			if _, _, err := (SubscriptionSpec{EventKey: def.Key}).Rule(); err == nil && def.Scope() != ScopeRoles {
				t.Errorf("%s: a missing %s scope was accepted", def.Key, def.Scope())
			}
		}
	}
}

// A pending or private key is refused before anything is sent.
func TestUnavailableKeysAreRefused(t *testing.T) {
	saved := eventCatalog
	defer func() { eventCatalog = saved }()
	eventCatalog = append(append([]EventDefinition(nil), saved...),
		EventDefinition{Key: "user_future_pending", Category: "im", RuleType: "all", Status: EventStatusPending, Public: true},
		EventDefinition{Key: "user_future_private", Category: "im", RuleType: "all", Status: EventStatusEnabled})
	for _, key := range []string{"user_future_pending", "user_future_private"} {
		if _, _, err := (SubscriptionSpec{EventKey: key}).Rule(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", key, err)
		}
	}
	for _, key := range EventKeys() {
		if strings.HasPrefix(key, "user_future_") {
			t.Errorf("EventKeys lists %s", key)
		}
	}
}

// The same todo roles name the same rule whatever their order or repeats.
func TestTodoRolesAreCanonical(t *testing.T) {
	a := SubscriptionSpec{EventKey: EventTodoCreated, RoleTypes: []string{"executor", "creator", "executor"}}
	b := SubscriptionSpec{EventKey: EventTodoCreated, RoleTypes: []string{"creator", "executor"}}
	_, param, err := a.Rule()
	if err != nil || strings.Join(param["roleTypes"].([]string), ",") != "creator,executor" {
		t.Fatalf("param = %v, %v", param, err)
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("reordered roles name a different subscription")
	}
	_, all, _ := SubscriptionSpec{EventKey: EventTodoCreated}.Rule()
	if strings.Join(all["roleTypes"].([]string), ",") != "creator,executor,participant" {
		t.Fatalf("default roles = %v", all)
	}
	if _, _, err := (SubscriptionSpec{EventKey: EventTodoCreated, RoleTypes: []string{"boss"}}).Rule(); err == nil {
		t.Fatal("an unknown role was accepted")
	}
}

// Stored subscriptions keep matching: a spec without a filter fingerprints
// exactly as before filters existed.
func TestFingerprintIsStableWithoutAFilter(t *testing.T) {
	spec := SubscriptionSpec{EventKey: EventIMGroup, ConversationID: "cid-1"}
	sum := sha256.Sum256([]byte(EventIMGroup + "\x00group\x00" + `{"openConversationId":"cid-1"}`))
	if want := hex.EncodeToString(sum[:8]); spec.Fingerprint() != want {
		t.Fatalf("fingerprint = %s, want %s", spec.Fingerprint(), want)
	}
}

func TestFilterRule(t *testing.T) {
	spec := SubscriptionSpec{EventKey: EventIMGroup, ConversationID: "cid-1",
		Filter:   json.RawMessage(`{"or":[{"field":"sender_open_dingtalk_id","op":"eq","value":"oid-9"},{"field":"content","op":"contains","value":"发布"}]}`),
		Keywords: []string{" 上线 ", "", "回滚"}}
	_, canon, err := spec.FilterRule()
	want := `{"and":[{"or":[{"field":"payload.body.senderOpenDingTalkId","op":"eq","value":"oid-9"},{"field":"payload.body.content","op":"contains","value":"发布"}]},{"field":"payload.body.content","op":"contains_any","value":["上线","回滚"]}]}`
	if err != nil || canon != want {
		t.Fatalf("filter = %s, %v", canon, err)
	}
	plain := SubscriptionSpec{EventKey: EventIMGroup, ConversationID: "cid-1"}
	if spec.Fingerprint() == plain.Fingerprint() || spec.Fingerprint() == "" {
		t.Fatal("a filtered spec must be its own subscription")
	}
	keywordsOnly := SubscriptionSpec{EventKey: EventIMAt, Keywords: []string{"上线"}}
	if _, canon, _ := keywordsOnly.FilterRule(); canon != `{"field":"payload.body.content","op":"contains_any","value":["上线"]}` {
		t.Fatalf("keywords only = %s", canon)
	}
	for name, bad := range map[string]SubscriptionSpec{
		"not JSON":        {EventKey: EventIMAt, Filter: json.RawMessage(`{`)},
		"not an object":   {EventKey: EventIMAt, Filter: json.RawMessage(`[1]`)},
		"no message body": {EventKey: EventGroupUpdated, ConversationID: "cid", Keywords: []string{"x"}},
		"negative TTL":    {EventKey: EventIMAt, TTLSeconds: -1},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrInvalidRequest) || bad.Fingerprint() != "" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Subscribe sends the filter and the expiry the way dws does.
func TestSubscribeSendsFilterAndExpiry(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"success":true,"result":["sub-7"]}`))
	}))
	defer srv.Close()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	c, err := NewWithToken(context.Background(), Config{AuthURL: srv.URL, GatewayURL: srv.URL, SkipVerify: true, Now: func() time.Time { return now }},
		Token{AccessToken: "uat", ClientID: "client-1"})
	if err != nil {
		t.Fatal(err)
	}
	spec := SubscriptionSpec{EventKey: EventIMAt, Keywords: []string{"上线"}, TTLSeconds: 3600}
	sub, err := c.Events.Subscribe(context.Background(), spec)
	if err != nil || sub.ID != "sub-7" || sub.Fingerprint != spec.Fingerprint() {
		t.Fatalf("sub = %+v, %v", sub, err)
	}
	ext := body["ext"].(map[string]any)
	filter, _ := json.Marshal(ext["filter"])
	// The subscription names the token's app, and the app scopes the
	// idempotency key so an app-less subscription of the spec is not reused.
	appKey := sha256.Sum256([]byte("client-1"))
	if string(filter) != `{"field":"payload.body.content","op":"contains_any","value":["上线"]}` ||
		body["clientId"] != "client-1" ||
		ext["idempotencyKey"] != "dws-for-tag-"+spec.Fingerprint()+"-"+hex.EncodeToString(appKey[:4]) ||
		body["expiresAt"] != "2026-09-30T13:00:00Z" {
		t.Fatalf("body = %v", body)
	}
	if _, err := c.Events.Subscribe(context.Background(), SubscriptionSpec{EventKey: EventIMGroup}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("an invalid spec reached DingTalk: %v", err)
	}
}

// Without a verified identity the control plane still learns the
// organization, from the exchange.
func TestEventsSendTheTokenCorpWithoutAVerifiedIdentity(t *testing.T) {
	var corp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corp = r.Header.Get("X-DWS-Corp-Id")
		_, _ = w.Write([]byte(`{"success":true,"result":["sub-1"]}`))
	}))
	defer srv.Close()
	c, err := NewWithToken(context.Background(), Config{AuthURL: srv.URL, GatewayURL: srv.URL, SkipVerify: true},
		Token{AccessToken: "uat", ClientID: "client-1", CorpID: "corp-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Events.Subscribe(context.Background(), SubscriptionSpec{EventKey: EventIMAt}); err != nil || corp != "corp-1" {
		t.Fatalf("corp header = %q, %v", corp, err)
	}
}

// A rule type the SDK does not know is refused instead of being subscribed
// without its parameters.
func TestUnknownRuleTypesAreRefused(t *testing.T) {
	def := EventDefinition{Key: "user_future_event", Category: "im", RuleType: "keyword", Status: EventStatusEnabled, Public: true}
	if def.Scope() != ScopeUnknown {
		t.Fatalf("scope = %s", def.Scope())
	}
	saved := eventCatalog
	eventCatalog = append(append([]EventDefinition(nil), saved...), def)
	defer func() { eventCatalog = saved }()
	if _, _, err := (SubscriptionSpec{EventKey: def.Key}).Rule(); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown rule type accepted: %v", err)
	}
	for _, d := range saved {
		if d.Scope() == ScopeUnknown {
			t.Fatalf("catalog key %s has an unknown rule type %s", d.Key, d.RuleType)
		}
	}
}
