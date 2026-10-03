package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/dwsclient"
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
	nativeConversationTitles = &nativeTitleCache{entries: map[string]nativeTitleEntry{}}
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	nativeClock = func() time.Time { return now }
	t.Cleanup(func() {
		nativeClock = time.Now
		nativeConversationTitles = &nativeTitleCache{entries: map[string]nativeTitleEntry{}}
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
	nativeConversationTitles = &nativeTitleCache{entries: map[string]nativeTitleEntry{}}
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
			nativeConversationTitles = &nativeTitleCache{entries: map[string]nativeTitleEntry{}}
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
		if scope.PersonKey != "open-user-1" || scope.PersonName != "测试用户甲" || scope.DispatchOrgID != nativeUnitOrg {
			t.Fatalf("%s: scope = %+v", key, scope)
		}
	}
}
