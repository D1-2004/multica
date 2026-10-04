package taskinput

import (
	"strconv"
	"strings"
	"testing"
)

func TestReplyChainIsBoundedAndCycleSafe(t *testing.T) {
	parents := map[string]string{"a": "b", "b": "c", "c": "a"}
	chain, complete := ReplyChain("a", parents)
	if !complete || strings.Join(chain, ",") != "a,b,c" {
		t.Fatalf("cycle: %v %v", chain, complete)
	}
	long := map[string]string{}
	for i := range 20 {
		long["m"+strconv.Itoa(i)] = "m" + strconv.Itoa(i+1)
	}
	chain, complete = ReplyChain("m0", long)
	if complete || len(chain) != MaxReplyHops {
		t.Fatalf("hop limit: %v %v", chain, complete)
	}
	chain, complete = ReplyChain("x", nil)
	if !complete || len(chain) != 1 {
		t.Fatalf("single hop: %v %v", chain, complete)
	}
}

func TestBindAnswerRules(t *testing.T) {
	const scene, group, other = "scene-dm", "scene-group", "scene-other"
	inv := func(id, sceneID, msg string, state InvitationState) BindingCandidate {
		return BindingCandidate{InvitationID: id, TargetSceneID: sceneID, ParticipantRef: "p", ProviderMessageID: msg, State: state}
	}
	dm := InboundMessage{SceneID: scene, SceneKind: "dm", SenderRef: "p", SenderKind: SenderPerson, MessageKind: MessageText}
	gm := dm
	gm.SceneID, gm.SceneKind = group, "group"

	cases := []struct {
		name       string
		msg        InboundMessage
		parents    map[string]string
		candidates []BindingCandidate
		outcome    BindOutcome
		id         string
		reason     string
	}{
		{"dm single pending", dm, nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered)}, BindBound, "1", ""},
		// A pending invitation in another scene does not compete in this DM.
		{"pending elsewhere", dm, nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered), inv("2", other, "m2", InvitationDelivered)}, BindBound, "1", ""},
		{"dm two pending", dm, nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered), inv("2", scene, "m2", InvitationDelivered)}, BindAmbiguous, "", ReasonSeveralPending},
		{"dm pending plus answered", dm, nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered), inv("2", scene, "m2", InvitationAnswered)}, BindBound, "1", ""},
		{"other participant", func() InboundMessage { m := dm; m.SenderRef = "q"; return m }(), nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered)}, BindUnbound, "", ReasonNoPendingInvitation},
		{"no identity", func() InboundMessage { m := dm; m.SenderRef = ""; return m }(), nil, nil, BindIgnored, "", ReasonUnidentifiedSender},
		{"unknown sender kind", func() InboundMessage { m := dm; m.SenderKind = ""; return m }(), nil, nil, BindIgnored, "", ReasonUnknownSender},
		{"system notice", func() InboundMessage { m := dm; m.MessageKind = MessageSystem; return m }(), nil, nil, BindIgnored, "", ReasonNotAnswerContent},
		{"unknown message kind", func() InboundMessage { m := dm; m.MessageKind = ""; return m }(), nil, nil, BindIgnored, "", ReasonNotAnswerContent},
		{"enterprise scene", func() InboundMessage { m := dm; m.SceneKind = "enterprise"; return m }(), nil, nil, BindIgnored, "", ReasonUnsupportedScene},
		{"group reply three hops", func() InboundMessage { m := gm; m.ReplyToID = "x2"; return m }(), map[string]string{"x2": "x1", "x1": "m1"}, []BindingCandidate{inv("1", group, "m1", InvitationDelivered)}, BindBound, "1", ""},
		{"group reply nearest invitation wins", func() InboundMessage { m := gm; m.ReplyToID = "m2"; return m }(), map[string]string{"m2": "m1"}, []BindingCandidate{inv("1", group, "m1", InvitationAnswered), inv("2", group, "m2", InvitationDelivered)}, BindBound, "2", ""},
		{"group reply too deep", func() InboundMessage { m := gm; m.ReplyToID = "d0"; return m }(), deepChain("d", 9, "m1"), []BindingCandidate{inv("1", group, "m1", InvitationDelivered)}, BindUnbound, "", ReasonReplyChainTooLong},
		{"group reply cycle", func() InboundMessage { m := gm; m.ReplyToID = "c1"; return m }(), map[string]string{"c1": "c2", "c2": "c1"}, []BindingCandidate{inv("1", group, "m1", InvitationDelivered)}, BindUnbound, "", ReasonReplyNotInvitation},
		{"group invite reference without reply", func() InboundMessage { m := gm; m.InviteRefs = []string{"1"}; return m }(), nil, []BindingCandidate{inv("1", group, "m1", InvitationDelivered)}, BindIgnored, "", ReasonGroupWithoutReply},
		{"dm quote of unrelated message", func() InboundMessage { m := dm; m.ReplyToID = "zzz"; return m }(), nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered)}, BindUnbound, "", ReasonReplyNotInvitation},
		{"dm reference", func() InboundMessage { m := dm; m.InviteRefs = []string{"2"}; return m }(), nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered), inv("2", scene, "m2", InvitationAnswered)}, BindBound, "2", ""},
		{"dm unknown reference", func() InboundMessage { m := dm; m.InviteRefs = []string{"9"}; return m }(), nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered)}, BindUnbound, "", ReasonInviteRefUnknown},
		{"dm two references", func() InboundMessage { m := dm; m.InviteRefs = []string{"1", "2"}; return m }(), nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered), inv("2", scene, "m2", InvitationDelivered)}, BindAmbiguous, "", ReasonSeveralReferences},
		{"duplicate invitation message", func() InboundMessage { m := dm; m.ReplyToID = "m1"; return m }(), nil, []BindingCandidate{inv("1", scene, "m1", InvitationDelivered), inv("2", scene, "m1", InvitationDelivered)}, BindAmbiguous, "", ReasonDuplicateProviderMsg},
		{"revoked candidate ignored", dm, nil, []BindingCandidate{inv("1", scene, "m1", InvitationRevoked)}, BindUnbound, "", ReasonNoPendingInvitation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := BindAnswer(tc.msg, tc.parents, tc.candidates)
			if b.Outcome != tc.outcome || b.InvitationID != tc.id || b.Reason != tc.reason {
				t.Fatalf("got %+v", b)
			}
		})
	}
}

func deepChain(prefix string, n int, end string) map[string]string {
	out := map[string]string{}
	for i := range n {
		out[prefix+strconv.Itoa(i)] = prefix + strconv.Itoa(i+1)
	}
	out[prefix+strconv.Itoa(n)] = end
	return out
}

func TestCheckEgressHoldsByLeastTrustedReader(t *testing.T) {
	const privateOrigin = "SENTINEL-origin requester private note"
	const ownAnswer = "SENTINEL-own earlier answer 7"
	private := []PrivateFragment{{Text: privateOrigin}, {Text: ownAnswer, MaxAudience: AudienceDirect}, {Text: "7"}}

	clean := CheckEgress(EgressInput{Rendered: "Hi, what was your number this week?", Audience: AudienceGroup, TargetSceneKind: "group", Private: private})
	if clean.Held || len(clean.RenderedHash) != 64 {
		t.Fatalf("clean group question: %+v", clean)
	}
	if again := CheckEgress(EgressInput{Rendered: "Hi, what was your number this week?", Audience: AudienceGroup, TargetSceneKind: "group"}); again.RenderedHash != clean.RenderedHash {
		t.Fatal("rendered hash must depend on the bytes only")
	}
	direct := CheckEgress(EgressInput{Rendered: "You said " + ownAnswer + " — still right?", Audience: AudienceDirect, TargetSceneKind: "dm", Private: private})
	if direct.Held {
		t.Fatalf("own answer may reach the participant directly: %+v", direct)
	}
	cases := map[string]EgressInput{
		HoldPrivateFragment:  {Rendered: "You said " + ownAnswer, Audience: AudienceGroup, TargetSceneKind: "group", Private: private},
		HoldAudienceMismatch: {Rendered: "Number?", Audience: AudienceDirect, TargetSceneKind: "group"},
		HoldSecret:           {Rendered: "use key sk-abcdefghijklmnopqrstuvwxyz123456", Audience: AudienceDirect, TargetSceneKind: "dm"},
		HoldConfigLink:       {Rendered: "configure here https://x/dingtalk/configure?link=abc", Audience: AudienceDirect, TargetSceneKind: "dm"},
		HoldEmpty:            {Rendered: "  ", Audience: AudienceDirect, TargetSceneKind: "dm"},
		HoldTooLong:          {Rendered: strings.Repeat("a", maxRenderedBytes+1), Audience: AudienceDirect, TargetSceneKind: "dm"},
	}
	for reason, in := range cases {
		got := CheckEgress(in)
		if !got.Held || !contains(got.Reasons, reason) {
			t.Fatalf("%s: %+v", reason, got)
		}
	}
	origin := CheckEgress(EgressInput{Rendered: "Context: " + privateOrigin, Audience: AudienceDirect, TargetSceneKind: "dm", Private: private})
	if !origin.Held || !contains(origin.Reasons, HoldPrivateFragment) {
		t.Fatalf("origin-private text never leaves, even directly: %+v", origin)
	}
	encoded := CheckEgress(EgressInput{Rendered: "dingtalk://dingtalkclient/page/link?url=https%3A%2F%2Fx%2Fdingtalk%2Fconfigure%3Flink%3Dabc", Audience: AudienceDirect, TargetSceneKind: "dm"})
	if !encoded.Held {
		t.Fatalf("encoded config link: %+v", encoded)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
