package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// A native group event names its conversation only by id; the title the
// server read for it rides on the dispatch like a Router delivery's, so
// scene.Resolve stores it. A single chat keeps its sender-named title.
func TestBuildNativeDispatchCommandCarriesGroupTitle(t *testing.T) {
	in := nativeUnitInput(dws.EventIMAt, nativeUnitMessage())
	in.ConversationTitle = " 项目群 "
	group, err := buildNativeDispatchCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := group.Event.Data.Conversation.Title; got != "项目群" {
		t.Fatalf("group title = %q", got)
	}
	_, obs, err := dispatchSceneLocator(group, nativeUnitOrg)
	if err != nil || obs.Title != "项目群" || !obs.KindStated {
		t.Fatalf("scene observation = %+v err=%v", obs, err)
	}

	in = nativeUnitInput(dws.EventIMAllSingleChats, nativeUnitMessage())
	in.ConversationTitle = "项目群"
	single, err := buildNativeDispatchCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := single.Event.Data.Conversation.Title; got != "" {
		t.Fatalf("single chat title = %q", got)
	}
	if _, obs, _ := dispatchSceneLocator(single, nativeUnitOrg); obs.Title != "测试用户甲" {
		t.Fatalf("single chat scene title = %q, want the sender's name", obs.Title)
	}
}

// The title is read from DingTalk at delivery, so a rename or a failed read
// between two deliveries of one message must replay, not conflict.
func TestNativeFingerprintIgnoresConversationTitle(t *testing.T) {
	untitled, _ := buildNativeDispatchCommand(nativeUnitInput(dws.EventIMAt, nativeUnitMessage()))
	in := nativeUnitInput(dws.EventIMAt, nativeUnitMessage())
	in.ConversationTitle = "项目群"
	titled, _ := buildNativeDispatchCommand(in)
	key := nativeDispatchIdempotencyKey(nativeUnitOrg, "cid-1", "msg-1")
	if dispatchRequestFingerprint(untitled, key) != dispatchRequestFingerprint(titled, key) {
		t.Fatal("the group title changed the native fingerprint")
	}
	if titled.Event.Data.Conversation.Title != "项目群" {
		t.Fatal("fingerprinting mutated the command")
	}
	router, routerTitled := untitled, titled
	router.CompletionCallback = &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/router-task/execution-result"}
	routerTitled.CompletionCallback = router.CompletionCallback
	if dispatchRequestFingerprint(router, key) == dispatchRequestFingerprint(routerTitled, key) {
		t.Fatal("Router fingerprints stopped covering the conversation title")
	}
}

func TestNativeConversationTitleCachesAndDegrades(t *testing.T) {
	nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	nativeClock = func() time.Time { return now }
	t.Cleanup(func() {
		nativeClock = time.Now
		nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	})
	identity := dwsclient.Identity{AgentID: nativeUnitAgent, UID: nativeUnitUID, OrgID: nativeUnitOrg}

	if got := (&Handler{}).nativeConversationTitle(context.Background(), identity, "cid-1"); got != "" {
		t.Fatalf("no lookup wired: %q", got)
	}

	calls := 0
	var answer string
	var failure error
	h := &Handler{DWSNativeConversationTitle: func(_ context.Context, id dwsclient.Identity, cid string) (string, error) {
		calls++
		if id != identity || cid != "cid-1" {
			t.Fatalf("lookup as %+v for %q", id, cid)
		}
		return answer, failure
	}}
	answer = " 项目群 "
	for i := 0; i < 2; i++ {
		if got := h.nativeConversationTitle(context.Background(), identity, " cid-1 "); got != "项目群" {
			t.Fatalf("title = %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("lookups = %d, want 1 (cached)", calls)
	}
	// A rename shows after the cache expires.
	now = now.Add(nativeTitleTTL)
	answer = "改名后的群"
	if got := h.nativeConversationTitle(context.Background(), identity, "cid-1"); got != "改名后的群" || calls != 2 {
		t.Fatalf("after expiry title = %q calls = %d", got, calls)
	}

	// A failed read dispatches untitled and is retried only after a minute.
	nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	failure = errors.New("FORBIDDEN")
	if got := h.nativeConversationTitle(context.Background(), identity, "cid-1"); got != "" {
		t.Fatalf("failed read title = %q", got)
	}
	h.nativeConversationTitle(context.Background(), identity, "cid-1")
	if calls != 3 {
		t.Fatalf("lookups after a failure = %d, want 3", calls)
	}
	now = now.Add(nativeTitleMissTTL)
	failure, answer = nil, "项目群"
	if got := h.nativeConversationTitle(context.Background(), identity, "cid-1"); got != "项目群" || calls != 4 {
		t.Fatalf("retry after a failure: title = %q calls = %d", got, calls)
	}

	// A read that names no title is kept like a title.
	nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	answer = ""
	h.nativeConversationTitle(context.Background(), identity, "cid-1")
	now = now.Add(nativeTitleMissTTL)
	if got := h.nativeConversationTitle(context.Background(), identity, "cid-1"); got != "" || calls != 5 {
		t.Fatalf("untitled read: title = %q calls = %d, want one lookup", got, calls)
	}

	// The stream's cancellation does not reach the read.
	nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	answer = "项目群"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	h.DWSNativeConversationTitle = func(ctx context.Context, _ dwsclient.Identity, _ string) (string, error) {
		calls++
		return answer, ctx.Err()
	}
	if got := h.nativeConversationTitle(cancelled, identity, "cid-1"); got != "项目群" {
		t.Fatalf("title under a cancelled stream context = %q", got)
	}
}

// Only a group event reads a title; a single chat never costs a lookup.
func TestAcceptNativeMessageReadsTitlesOfGroupsOnly(t *testing.T) {
	for _, tt := range []struct {
		key   string
		calls int
	}{
		{dws.EventIMAt, 1},
		{dws.EventIMAllSingleChats, 0},
	} {
		t.Run(tt.key, func(t *testing.T) {
			nativeConversationTitles = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
			nativeDispatchLoops = newNativeLoopBreaker(nativeLoopLimit, nativeLoopWindow)
			now := time.UnixMilli(nativeUnitMessage().EventTime).Add(time.Minute)
			nativeClock = func() time.Time { return now }
			t.Cleanup(func() { nativeClock = time.Now })
			calls := 0
			store := nativeUnitStore()
			h := &Handler{dwsNativeDispatch: store, DWSNativeConversationTitle: func(context.Context, dwsclient.Identity, string) (string, error) {
				calls++
				return "项目群", nil
			}}
			identity := dwsclient.Identity{AgentID: nativeUnitAgent, UID: nativeUnitUID, OrgID: nativeUnitOrg}
			if err := h.acceptNativeMessage(context.Background(), identity, dwsevents.Event{ID: "ev-1", Key: tt.key}, nativeUnitMessage()); err != nil {
				t.Fatal(err)
			}
			if calls != tt.calls || store.endpointReads != 1 {
				t.Fatalf("title lookups = %d (want %d), reached dispatch = %v", calls, tt.calls, store.endpointReads == 1)
			}
		})
	}
}

// The trigger person of a native dispatch is the sender's openDingTalkId as
// the receiving account sees it, the same in its groups and single chats, so
// one personal configuration follows the person into both.
func TestNativeDispatchTriggerPersonIsTheSameInGroupAndSingleChat(t *testing.T) {
	for _, key := range []string{dws.EventIMAt, dws.EventIMAllSingleChats} {
		command, err := buildNativeDispatchCommand(nativeUnitInput(key, nativeUnitMessage()))
		if err != nil {
			t.Fatal(err)
		}
		scope := contextcap.ScopeFromTaskContext(dispatchRuntimeContext(command, "idem-1"))
		if scope.PersonKey != "odt:open-user-1" || scope.PersonName != "测试用户甲" || scope.DispatchOrgID != nativeUnitOrg {
			t.Fatalf("%s: scope = %+v", key, scope)
		}
	}
}

// The Employee foreground directory keys its person layer by the same rule
// as task claims: staffId, else the native sender's openDingTalkId.
func TestEmployeeCapabilityPersonKeysNativeSenders(t *testing.T) {
	job := employeeentry.Job{Scope: employeeentry.Scope{TenantOrgID: nativeUnitOrg}}
	envelope := func(messages ...DispatchMessage) employeeDispatchEnvelope {
		var env employeeDispatchEnvelope
		env.Command.Event.Data.Messages = messages
		return env
	}
	native := func(openID string) DispatchMessage {
		return DispatchMessage{OpenMsgID: "m-" + openID, SenderOpenDingTalkID: openID}
	}
	for _, tt := range []struct {
		name      string
		envelopes []employeeDispatchEnvelope
		want      string
	}{
		{"one native sender", []employeeDispatchEnvelope{envelope(native("DopenA")), envelope(native("DopenA"))}, "odt:DopenA"},
		{"two native senders", []employeeDispatchEnvelope{envelope(native("DopenA"), native("DopenB"))}, ""},
		{"a message without a sender", []employeeDispatchEnvelope{envelope(native("DopenA"), DispatchMessage{OpenMsgID: "anon"})}, ""},
		{"router staff id", []employeeDispatchEnvelope{envelope(DispatchMessage{SenderUID: "u1", SenderStaffID: "staff-1", SenderOpenDingTalkID: "DopenA"})}, "staff-1"},
		{"no messages", []employeeDispatchEnvelope{envelope()}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := employeeCapabilityPerson(job, tt.envelopes); got != tt.want {
				t.Fatalf("person = %q, want %q", got, tt.want)
			}
		})
	}
}

// A native sender's staffId is looked up once and dispatched like a Router
// delivery's; a miss or a failure dispatches the openDingTalkId only and is
// retried later.
func TestNativeSenderStaffIDLookup(t *testing.T) {
	nativeStaffMisses = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	nativeClock = func() time.Time { return now }
	t.Cleanup(func() {
		nativeClock = time.Now
		nativeStaffMisses = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}
	})
	identity := dwsclient.Identity{AgentID: nativeUnitAgent, UID: nativeUnitUID, OrgID: nativeUnitOrg}
	m := nativeUnitMessage()
	if got := (&Handler{}).nativeSenderStaffID(context.Background(), identity, m, "cid-1"); got != "" {
		t.Fatalf("no lookup wired: %q", got)
	}
	calls := 0
	var answer string
	var failure error
	h := &Handler{DWSNativeStaffID: func(_ context.Context, id dwsclient.Identity, openID string, names []string, cid string) (string, error) {
		calls++
		if id != identity || openID != "open-user-1" || len(names) != 1 || names[0] != "测试用户甲" || cid != "cid-1" {
			t.Fatalf("lookup %+v %q %v %q", id, openID, names, cid)
		}
		return answer, failure
	}}
	answer = "staff-1"
	if got := h.nativeSenderStaffID(context.Background(), identity, m, "cid-1"); got != "staff-1" || calls != 1 {
		t.Fatalf("found: %q calls=%d", got, calls)
	}
	// Not in the address book: the openDingTalkId alone, asked again later.
	answer = ""
	h.nativeSenderStaffID(context.Background(), identity, m, "cid-1")
	h.nativeSenderStaffID(context.Background(), identity, m, "cid-1")
	if calls != 2 {
		t.Fatalf("a miss is cached: calls=%d, want 2", calls)
	}
	now = now.Add(nativeStaffMissTTL)
	answer = "odt:spoof"
	if got := h.nativeSenderStaffID(context.Background(), identity, m, "cid-1"); got != "" || calls != 3 {
		t.Fatalf("a prefixed staff id was accepted: %q calls=%d", got, calls)
	}
	now = now.Add(nativeStaffMissTTL)
	failure = errors.New("DWS down")
	if got := h.nativeSenderStaffID(context.Background(), identity, m, "cid-1"); got != "" || calls != 4 {
		t.Fatalf("failure: %q calls=%d", got, calls)
	}
	now = now.Add(nativeStaffFailureTTL)
	failure, answer = nil, "staff-1"
	if got := h.nativeSenderStaffID(context.Background(), identity, m, "cid-1"); got != "staff-1" || calls != 5 {
		t.Fatalf("retry after a failure: %q calls=%d", got, calls)
	}
	anonymous := nativeUnitMessage()
	anonymous.SenderOpenDingTalkID = "null"
	if got := h.nativeSenderStaffID(context.Background(), identity, anonymous, "cid-1"); got != "" || calls != 5 {
		t.Fatalf("an anonymous sender was looked up: %q calls=%d", got, calls)
	}
}

// A proved staffId rides on the native dispatch like a Router delivery's:
// the same person key in groups and single chats, and no fingerprint change
// when one delivery could not prove it.
func TestNativeDispatchCarriesSenderStaffID(t *testing.T) {
	for _, key := range []string{dws.EventIMAt, dws.EventIMAllSingleChats} {
		in := nativeUnitInput(key, nativeUnitMessage())
		in.SenderStaffID = " staff-1 "
		command, err := buildNativeDispatchCommand(in)
		if err != nil {
			t.Fatal(err)
		}
		data := command.Event.Data
		if data.Sender.StaffID != "staff-1" || data.Messages[0].SenderStaffID != "staff-1" || data.Sender.OpenDingTalkID != "open-user-1" {
			t.Fatalf("%s: sender %+v message staff %q", key, data.Sender, data.Messages[0].SenderStaffID)
		}
		scope := contextcap.ScopeFromTaskContext(dispatchRuntimeContext(command, "idem-1"))
		if scope.PersonKey != "staff-1" || scope.PersonName != "测试用户甲" {
			t.Fatalf("%s: scope = %+v", key, scope)
		}
		unresolved, _ := buildNativeDispatchCommand(nativeUnitInput(key, nativeUnitMessage()))
		idem := nativeDispatchIdempotencyKey(nativeUnitOrg, "cid-1", "msg-1")
		if dispatchRequestFingerprint(command, idem) != dispatchRequestFingerprint(unresolved, idem) {
			t.Fatalf("%s: the staffId changed the native fingerprint", key)
		}
	}
	// No openDingTalkId, no staffId: nothing is attributed to an unknown sender.
	m := nativeUnitMessage()
	m.SenderOpenDingTalkID = ""
	in := nativeUnitInput(dws.EventIMAt, m)
	in.SenderStaffID = "staff-1"
	command, err := buildNativeDispatchCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	if command.Event.Data.Sender.StaffID != "" || command.Event.Data.Messages[0].SenderStaffID != "" {
		t.Fatal("a staffId was attached to an anonymous sender")
	}
}
