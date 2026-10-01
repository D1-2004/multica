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
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_source":{"platform":"dingtalk"},"dispatch_event_data":{
				"conversation":{"openConversationId":" cidGroupA== ","type":"group","title":"Ops room"},
				"sender":{"staffId":"staff-1","displayName":"Alice"},
				"messages":[{"openMsgId":"m1","senderStaffId":"staff-1"},{"openMsgId":"m2","senderStaffId":"staff-1"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", SceneTitle: "Ops room", PersonKey: "staff-1", PersonName: "Alice", ConversationType: "group"},
		},
		{
			name: "one unstamped message belongs to the data-level sender",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-1","displayName":"Alice","uid":"uid-1"},
				"messages":[{"openMsgId":"msg","text":"hello"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", PersonKey: "staff-1", PersonName: "Alice", ConversationType: "group"},
		},
		{
			name: "recorded agent org is kept for the binding-drift check",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"external_identity":{"dws":{"uid":"agent-uid","orgId":" 5001 "}},"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},"sender":{"staffId":"staff-1"}}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", PersonKey: "staff-1", ConversationType: "group", DispatchOrgID: "5001"},
		},
		{
			name: "event dispatch without messages keeps its actor",
			raw:  `{"dispatch_event_data":{"conversation":{"type":"single"},"sender":{"staffId":"staff-1"}}}`,
			want: Scope{PersonKey: "staff-1", ConversationType: "single"},
		},
		{
			name: "several messages without staff ids prove nothing",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-2","displayName":"Yan"},
				"messages":[{"openMsgId":"x","text":"from X"},{"openMsgId":"y","text":"from Y"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", ConversationType: "group"},
		},
		{
			name: "partial identities in a merged window",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-a","uid":"uid-a"},
				"messages":[{"openMsgId":"m-a","senderStaffId":"staff-a","senderUid":"uid-a"},{"openMsgId":"m-b","senderOpenDingTalkId":"open-b"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", ConversationType: "group"},
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
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"coordinator_follow_up_comment_ids":["c1","c2"],"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-y"},"messages":[{"openMsgId":"m-x","text":"from X"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", ConversationType: "group"},
		},
		{
			name: "coalesced follow-ups of one stamped person",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"coordinator_follow_up_comment_ids":["c1","c2"],"dispatch_event_data":{
				"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-y"},"messages":[{"senderStaffId":"staff-y"},{"senderStaffId":"staff-y"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", PersonKey: "staff-y", ConversationType: "group"},
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
			raw:  `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupB","type":"group","conversationTitle":"Alias title"},"sender":{}}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", SceneTitle: "Alias title", ConversationType: "group"},
		},
		{
			name: "snake title alias",
			raw:  `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupC","type":"group","conversation_title":"Snake"}}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", SceneTitle: "Snake", ConversationType: "group"},
		},
		{
			// A 1:1 chat is a scene like a group: its SceneRef is its layer.
			name: "single chat carries its scene",
			raw: `{"agent_scene":{"scene_id":"22222222-2222-4222-8222-222222222222"},"dispatch_event_data":{"conversation":{"openConversationId":"cidDirect","type":"single","title":"DM"},
				"sender":{"staffId":"staff-2","displayName":"Bob"},"messages":[{"senderStaffId":"staff-2"}]}}`,
			want: Scope{SceneID: "22222222-2222-4222-8222-222222222222", SceneTitle: "DM", PersonKey: "staff-2", PersonName: "Bob", ConversationType: "single"},
		},
		{
			// The conversation id alone is never a scene: only the resolved
			// SceneRef is.
			name: "conversation without a scene ref has no scene",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group","title":"Ops room"},
				"sender":{"staffId":"staff-1"}}}`,
			want: Scope{PersonKey: "staff-1", ConversationType: "group"},
		},
		{
			name: "malformed scene ref is ignored",
			raw: `{"agent_scene":{"scene_id":"cidGroupA"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-1"}}}`,
			want: Scope{PersonKey: "staff-1", ConversationType: "group"},
		},
		{
			name: "unknown conversation type has no DM scene",
			raw: `{"dispatch_event_data":{"conversation":{"openConversationId":"cidMaybeShared","type":"channel"},
				"sender":{"staffId":"staff-2"}}}`,
			want: Scope{PersonKey: "staff-2", ConversationType: "channel"},
		},
		{
			name: "multi sender merged run has no person",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},"dispatch_event_data":{"conversation":{"openConversationId":"cidGroupA","type":"group"},
				"sender":{"staffId":"staff-1","displayName":"Alice"},
				"messages":[{"senderStaffId":"staff-1"},{"senderStaffId":"staff-9"}]}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", ConversationType: "group"},
		},
		{
			name: "sender staff id with control character",
			raw:  `{"dispatch_event_data":{"conversation":{"type":"single"},"sender":{"staffId":"a\u0007b"}}}`,
			want: Scope{ConversationType: "single"},
		},
		{
			// Still a dispatch: the org layer may apply without scene or person.
			name: "dispatch without conversation or sender",
			raw:  `{"dispatch_event_data":{}}`,
			want: Scope{Dispatched: true},
		},
		{
			name: "group scene routine carries the scene and its org, never a person",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},
				"scene_routine":{"routine_id":"r1","tenant_org_id":" 5001 ","kind":"group","person_staff_id":"staff-1"}}`,
			want: Scope{SceneID: "11111111-1111-4111-8111-111111111111", ConversationType: "group", DispatchOrgID: "5001"},
		},
		{
			name: "dm scene routine carries its proven counterpart",
			raw: `{"agent_scene":{"scene_id":"22222222-2222-4222-8222-222222222222"},
				"scene_routine":{"routine_id":"r2","tenant_org_id":"5001","kind":"dm","person_staff_id":"staff-7"}}`,
			want: Scope{SceneID: "22222222-2222-4222-8222-222222222222", ConversationType: "single", DispatchOrgID: "5001", PersonKey: "staff-7"},
		},
		{
			name: "dm scene routine without a proven counterpart has no person",
			raw: `{"agent_scene":{"scene_id":"22222222-2222-4222-8222-222222222222"},
				"scene_routine":{"routine_id":"r2","tenant_org_id":"5001","kind":"dm"}}`,
			want: Scope{SceneID: "22222222-2222-4222-8222-222222222222", ConversationType: "single", DispatchOrgID: "5001"},
		},
		{
			name: "replayed scene routine has no layers",
			raw: `{"replayed_dispatch_context":true,"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},
				"scene_routine":{"routine_id":"r1","tenant_org_id":"5001","kind":"group"}}`,
			want: Scope{},
		},
		{
			name: "scene routine of an unknown kind has no layers",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},
				"scene_routine":{"routine_id":"r1","tenant_org_id":"5001","kind":"enterprise"}}`,
			want: Scope{},
		},
		{
			name: "scene routine without an org has no layers",
			raw: `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"},
				"scene_routine":{"routine_id":"r1","kind":"group"}}`,
			want: Scope{},
		},
		{
			name: "a scene ref alone is not a routine",
			raw:  `{"agent_scene":{"scene_id":"11111111-1111-4111-8111-111111111111"}}`,
			want: Scope{},
		},
		{name: "no dispatch data", raw: `{"issue_id":"x"}`, want: Scope{}},
		{name: "malformed json", raw: `{"dispatch_event_data":`, want: Scope{}},
		{name: "wrong field type", raw: `{"dispatch_event_data":{"conversation":{"type":7}}}`, want: Scope{}},
		{name: "empty", raw: ``, want: Scope{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every parsed dispatch is Dispatched; the zero Scope never is.
			want := tc.want
			if want != (Scope{}) {
				want.Dispatched = true
			}
			got := ScopeFromTaskContext([]byte(tc.raw))
			if got != want {
				t.Fatalf("ScopeFromTaskContext() = %#v, want %#v", got, want)
			}
			if got.OrgID != "" || got.HasOrg() {
				t.Fatal("org id must be filled by the caller, never from the task context")
			}
		})
	}
}

func TestScopeLayerSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scope  Scope
		layers bool
		want   LayerSelection
	}{
		{name: "zero", scope: Scope{}, want: LayerSelection{}},
		{name: "org only", scope: Scope{Dispatched: true, OrgID: "org-1"}, layers: true, want: LayerSelection{OrgID: "org-1", Org: true}},
		{
			name:   "org, scene and person",
			scope:  Scope{Dispatched: true, OrgID: "org-1", SceneID: "11111111-1111-4111-8111-111111111111", PersonKey: "staff-1"},
			layers: true,
			want:   LayerSelection{OrgID: "org-1", Org: true, SceneID: "11111111-1111-4111-8111-111111111111", PersonKey: "staff-1"},
		},
		{
			// An org id without a dispatch is not an org layer.
			name:  "org id without dispatch",
			scope: Scope{OrgID: "org-1"},
			want:  LayerSelection{OrgID: "org-1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.scope.HasLayers(); got != tc.layers {
				t.Fatalf("HasLayers() = %v, want %v", got, tc.layers)
			}
			if got := tc.scope.Selection(); got != tc.want {
				t.Fatalf("Selection() = %#v, want %#v", got, tc.want)
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
