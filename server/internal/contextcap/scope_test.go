package contextcap

import (
	"strings"
	"testing"
)

func TestScopeFromTaskContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want Scope
	}{
		{
			name: "group with single sender",
			raw: `{"dispatch_source":{"platform":"dingtalk"},"dispatch_event_data":{
				"conversation":{"openConversationId":" cidGroupA== ","type":"group","title":"Ops room"},
				"sender":{"staffId":"staff-1","displayName":"Alice"},
				"messages":[{"openMsgId":"m1","senderStaffId":"staff-1"},{"openMsgId":"m2","senderStaffId":"staff-1"}]}}`,
			want: Scope{SceneKey: "cidGroupA==", SceneTitle: "Ops room", PersonKey: "staff-1", PersonName: "Alice", ConversationType: "group"},
		},
		{
			name: "one unstamped message belongs to the data-level sender",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-1","displayName":"Alice","uid":"uid-1"},
				"messages":[{"openMsgId":"msg","text":"hello"}]}}`,
			want: Scope{SceneKey: "cidGroupA", PersonKey: "staff-1", PersonName: "Alice", ConversationType: "group"},
		},
		{
			name: "recorded agent org is kept for the binding-drift check",
			raw: `{"external_identity":{"dws":{"uid":"agent-uid","orgId":" 5001 "}},"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},"sender":{"staffId":"staff-1"}}}`,
			want: Scope{SceneKey: "cidGroupA", PersonKey: "staff-1", ConversationType: "group", DispatchOrgID: "5001"},
		},
		{
			name: "event dispatch without messages keeps its actor",
			raw:  `{"dispatch_event_data":{"conversation":{"type":"single"},"sender":{"staffId":"staff-1"}}}`,
			want: Scope{PersonKey: "staff-1", ConversationType: "single"},
		},
		{
			name: "several messages without staff ids prove nothing",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-2","displayName":"Yan"},
				"messages":[{"openMsgId":"x","text":"from X"},{"openMsgId":"y","text":"from Y"}]}}`,
			want: Scope{SceneKey: "cidGroupA", ConversationType: "group"},
		},
		{
			name: "partial identities in a merged window",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-a","uid":"uid-a"},
				"messages":[{"openMsgId":"m-a","senderStaffId":"staff-a","senderUid":"uid-a"},{"openMsgId":"m-b","senderOpenDingTalkId":"open-b"}]}}`,
			want: Scope{SceneKey: "cidGroupA", ConversationType: "group"},
		},
		{
			name: "single message naming another uid",
			raw: `{"dispatch_event_data":{"conversation":{"type":"single"},"sender":{"staffId":"staff-1","uid":"uid-1"},
				"messages":[{"openMsgId":"m","senderUid":"uid-2"}]}}`,
			want: Scope{ConversationType: "single"},
		},
		{
			name: "single message naming another openDingTalkId",
			raw: `{"dispatch_event_data":{"conversation":{"type":"single"},"sender":{"staffId":"staff-1","openDingTalkId":"open-1"},
				"messages":[{"openMsgId":"m","senderOpenDingTalkId":"open-2"}]}}`,
			want: Scope{ConversationType: "single"},
		},
		{
			name: "coalesced follow-ups need stamped messages",
			raw: `{"coordinator_follow_up_comment_ids":["c1","c2"],"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-y"},"messages":[{"openMsgId":"m-x","text":"from X"}]}}`,
			want: Scope{SceneKey: "cidGroupA", ConversationType: "group"},
		},
		{
			name: "coalesced follow-ups of one stamped person",
			raw: `{"coordinator_follow_up_comment_ids":["c1","c2"],"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-y"},"messages":[{"senderStaffId":"staff-y"},{"senderStaffId":"staff-y"}]}}`,
			want: Scope{SceneKey: "cidGroupA", PersonKey: "staff-y", ConversationType: "group"},
		},
		{
			name: "replayed context has no layers",
			raw: `{"replayed_dispatch_context":true,"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-1"},"messages":[{"senderStaffId":"staff-1"}]}}`,
			want: Scope{},
		},
		{
			name: "group title alias",
			raw:  `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupB","type":"group","conversationTitle":"Alias title"},"sender":{}}}`,
			want: Scope{SceneKey: "cidGroupB", SceneTitle: "Alias title", ConversationType: "group"},
		},
		{
			name: "snake title alias",
			raw:  `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupC","type":"group","conversation_title":"Snake"}}}`,
			want: Scope{SceneKey: "cidGroupC", SceneTitle: "Snake", ConversationType: "group"},
		},
		{
			// The DM is a configuration scene (DirectSceneKey) but carries no
			// runtime scene layer this round.
			name: "single chat has no runtime scene",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidDirect","type":"single","title":"DM"},
				"sender":{"staffId":"staff-2","displayName":"Bob"},"messages":[{"senderStaffId":"staff-2"}]}}`,
			want: Scope{PersonKey: "staff-2", PersonName: "Bob", ConversationType: "single", DirectSceneKey: "cidDirect"},
		},
		{
			name: "unknown conversation type has no DM scene",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidMaybeShared","type":"channel"},
				"sender":{"staffId":"staff-2"}}}`,
			want: Scope{PersonKey: "staff-2", ConversationType: "channel"},
		},
		{
			name: "multi sender merged run has no person",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-1","displayName":"Alice"},
				"messages":[{"senderStaffId":"staff-1"},{"senderStaffId":"staff-9"}]}}`,
			want: Scope{SceneKey: "cidGroupA", ConversationType: "group"},
		},
		{
			name: "invalid cid prefix",
			raw:  `{"dispatch_event_data":{"conversation":{"openConversationId":"xidGroup","type":"group"},"sender":{"staffId":"s"}}}`,
			want: Scope{PersonKey: "s", ConversationType: "group"},
		},
		{
			name: "cid with inner whitespace",
			raw:  `{"dispatch_event_data":{"conversation":{"openConversationId":"cid Group","type":"group"}}}`,
			want: Scope{ConversationType: "group"},
		},
		{
			name: "sender staff id with control character",
			raw:  `{"dispatch_event_data":{"conversation":{"type":"single"},"sender":{"staffId":"a\u0007b"}}}`,
			want: Scope{ConversationType: "single"},
		},
		{name: "no dispatch data", raw: `{"issue_id":"x"}`, want: Scope{}},
		{name: "malformed json", raw: `{"dispatch_event_data":`, want: Scope{}},
		{name: "wrong field type", raw: `{"dispatch_event_data":{"conversation":{"type":7}}}`, want: Scope{}},
		{name: "empty", raw: ``, want: Scope{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeFromTaskContext([]byte(tc.raw))
			if got != tc.want {
				t.Fatalf("ScopeFromTaskContext() = %#v, want %#v", got, tc.want)
			}
			if got.OrgID != "" {
				t.Fatal("org id must be filled by the caller, never from the task context")
			}
		})
	}
}

func TestValidOpenConversationID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"cidVaO557dsSgYcgnvRNbwY4g==", true},
		{"cid", true},
		{"", false},
		{"CIDupper", false},
		{"cid\tx", false},
		{"cid\u0000x", false},
		{"cid" + strings.Repeat("a", 253), true},
		{"cid" + strings.Repeat("a", 254), false},
	} {
		if got := ValidOpenConversationID(tc.id); got != tc.want {
			t.Errorf("ValidOpenConversationID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
	if ValidScopeKey(ScopeOffer, "") || !ValidScopeKey(ScopePerson, "staff") || ValidScopeKey(ScopeScene, "staff") {
		t.Fatal("scope key validation does not follow the scope type")
	}
}

func TestTTLs(t *testing.T) {
	if LinkTTL(ScopeScene) != LinkTTLScene || LinkTTL(ScopePerson) != LinkTTLPerson || LinkTTL(ScopeOffer) != 0 {
		t.Fatal("link TTL mismatch")
	}
	if GrantTTL(ScopeScene) != GrantTTLScene || GrantTTL(ScopePerson) != GrantTTLPerson || GrantTTL("") != 0 {
		t.Fatal("grant TTL mismatch")
	}
}

func TestIsDirectConversationType(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want bool
	}{
		{"single", true},
		{" P2P ", true},
		{"private", true},
		{"Direct", true},
		{"group", false},
		{"", false},
		{"channel", false},
	} {
		if got := IsDirectConversationType(tc.kind); got != tc.want {
			t.Errorf("IsDirectConversationType(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}
