package handler

import (
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestUserDecisionUnavailableDoesNotDispatch(t *testing.T) {
	c := DispatchCommand{}
	d := unavailableUserDecision(c)
	if d.Action != inboundcoord.ActionReply || d.UserText == "" || len(d.Items) != 0 {
		t.Fatalf("unsafe unavailable decision: %+v", d)
	}
	c.ProactiveConversation = true
	d = unavailableUserDecision(c)
	if d.Action != inboundcoord.ActionSilence {
		t.Fatal("unrelated proactive message would get an unavailable notice")
	}
}

func TestUserDecisionRejectionPreservesActualReason(t *testing.T) {
	for _, reason := range []string{"user_decision_channel_unsupported", "user_decision_queue_unavailable", "user_decision_sender_unavailable", "user_decision_identity_verification_failed", "user_decision_multiple_initiators"} {
		d := rejectedUserDecision(DispatchCommand{}, reason, "本次未执行。")
		if d.Reason != reason || d.Action != inboundcoord.ActionReply || len(d.Items) != 0 {
			t.Fatalf("misclassified rejection: %+v", d)
		}
		c := DispatchCommand{ProactiveConversation: true}
		if d := rejectedUserDecision(c, reason, "本次未执行。"); d.Action != inboundcoord.ActionSilence {
			t.Fatal("rejection exposed an unrelated group response")
		}
	}
}

func TestUserDecisionIdentityDiagnosticsAreBounded(t *testing.T) {
	for _, code := range []string{"user_decision_sender_profile_lookup_failed", "user_decision_sender_profile_invalid", "user_decision_initiator_lookup_failed"} {
		reason, message := userDecisionIdentityRejection(fmt.Errorf("private transport details: %w", &dwsclient.DecisionIdentityError{Code: code}))
		if reason != code || message == "" || strings.Contains(message, "private") {
			t.Fatalf("unsafe classification: %s %s", reason, message)
		}
	}
	for _, err := range []error{errors.New("secret credential"), &dwsclient.DecisionIdentityError{Code: "secret credential"}} {
		reason, message := userDecisionIdentityRejection(err)
		if reason != "user_decision_identity_verification_failed" || strings.Contains(message, "secret") {
			t.Fatalf("untrusted diagnostic leaked: %s %s", reason, message)
		}
	}
}
func TestRobotDoesNotEnterDigitalEmployeeUserDecisionGuard(t *testing.T) {
	var h Handler
	c := DispatchCommand{}
	c.Event.Domain = "channel"
	c.Event.Type = "message.created"
	c.Source.Type = "robot"
	w := httptest.NewRecorder()
	if h.guardUnavailableUserDecision(w, httptest.NewRequest("POST", "/", nil), c, agentDispatchContext{}) {
		t.Fatal("robot blocked by digital employee decision mode")
	}
	if w.Body.Len() != 0 {
		t.Fatal("robot received decision rejection")
	}
}

func TestUserDecisionNamesUseExactTrustedSender(t *testing.T) {
	policy := db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, InboundCoordinatorUserDecision: true, InboundCoordinatorUserDecisionNames: []string{" 冬翔 ", "Alice"}}
	command := func(name string) DispatchCommand {
		return DispatchCommand{Source: DispatchSource{Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{Sender: DispatchSender{DisplayName: name, UID: "sender"}, Messages: []DispatchMessage{{Text: "冬翔 Alice"}}}}}
	}
	for _, tc := range []struct {
		name string
		want bool
	}{{"冬翔", true}, {" 冬翔 ", true}, {"Alice", true}, {"alice", false}, {"冬", false}, {"冬翔测试", false}, {"其他人", false}, {"", false}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := userDecisionEnabledForCommand(policy, command(tc.name)); got != tc.want {
				t.Fatalf("eligibility=%v want=%v", got, tc.want)
			}
		})
	}
	p := policy
	p.InboundCoordinatorUserDecisionNames = nil
	if userDecisionEnabledForCommand(p, command("冬翔")) {
		t.Fatal("empty names enabled cards")
	}
	p = policy
	p.InboundCoordinatorUserDecision = false
	if userDecisionEnabledForCommand(p, command("冬翔")) {
		t.Fatal("disabled switch enabled cards")
	}
	p = policy
	p.InboundCoordinator = false
	if userDecisionEnabledForCommand(p, command("冬翔")) {
		t.Fatal("disabled coordinator enabled cards")
	}
	c := command("冬翔")
	c.Source.Type = "robot"
	if userDecisionEnabledForCommand(policy, c) {
		t.Fatal("robot enabled cards")
	}
	c = command("冬翔")
	c.Event.Data.Messages[0].SenderDisplayName = "其他人"
	if userDecisionEnabledForCommand(policy, c) {
		t.Fatal("mixed eligibility batch enabled cards")
	}
	c = command("冬翔")
	c.TaskFinishedTaskID = "completed-task"
	if userDecisionEnabledForCommand(policy, c) {
		t.Fatal("completion enabled cards")
	}
}

func TestUserDecisionCollectSeparatesAudienceAndAuthors(t *testing.T) {
	policy := db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, InboundCoordinatorUserDecision: true, InboundCoordinatorUserDecisionNames: []string{"冬翔"}}
	command := func(name, id string) DispatchCommand {
		return DispatchCommand{Source: DispatchSource{Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{Sender: DispatchSender{DisplayName: name, UID: id}}}}
	}
	for _, tc := range []struct {
		name string
		a, b DispatchCommand
		want bool
	}{
		{"allowed and excluded", command("冬翔", "a"), command("其他人", "b"), false},
		{"excluded and allowed", command("其他人", "b"), command("冬翔", "a"), false},
		{"renamed same ID", command("冬翔", "a"), command("其他人", "a"), false},
		{"same name different ID", command("冬翔", "a"), command("冬翔", "b"), false},
		{"same allowed author", command("冬翔", "a"), command("冬翔", "a"), true},
		{"automatic different authors", command("甲", "a"), command("乙", "b"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameUserDecisionCollectAudience(policy, tc.a, tc.b); got != tc.want {
				t.Fatalf("collect=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestNormalizeUserDecisionNames(t *testing.T) {
	if got := normalizeUserDecisionNames([]string{" 冬翔 ", "", "冬翔", "Alice", "alice"}); !reflect.DeepEqual(got, []string{"冬翔", "Alice", "alice"}) {
		t.Fatalf("names=%v", got)
	}
	if normalizeUserDecisionNames(nil) == nil {
		t.Fatal("explicit empty update must not become SQL NULL")
	}
}

func TestUserDecisionMissingMessageNameStillChecksAuthor(t *testing.T) {
	policy := db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, InboundCoordinatorUserDecision: true, InboundCoordinatorUserDecisionNames: []string{"冬翔"}}
	c := DispatchCommand{Source: DispatchSource{Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{Sender: DispatchSender{DisplayName: "冬翔", UID: "a"}, Messages: []DispatchMessage{{SenderUID: "b", Text: "冬翔"}}}}}
	if !userDecisionEnabledForCommand(policy, c) {
		t.Fatal("missing message name bypassed decision author verification")
	}
	if singleDecisionAuthor(c) {
		t.Fatal("missing message name bypassed distinct author rejection")
	}
}

func TestUserDecisionAllKeepsChannelAndAuthorGuards(t *testing.T) {
	policy := db.GetAgentDingTalkResponsePolicyRow{InboundCoordinator: true, InboundCoordinatorUserDecision: true, InboundCoordinatorUserDecisionAudience: "all"}
	command := func(name, id string) DispatchCommand {
		return DispatchCommand{Source: DispatchSource{Type: "digital_employee"}, Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{Sender: DispatchSender{DisplayName: name, UID: id}}}}
	}
	for _, name := range []string{"冬翔", "任何人", ""} {
		if !userDecisionEnabledForCommand(policy, command(name, "a")) {
			t.Fatalf("all excluded sender %q", name)
		}
	}
	if sameUserDecisionCollectAudience(policy, command("同名", "a"), command("同名", "b")) {
		t.Fatal("all merged different authors")
	}
	if !sameUserDecisionCollectAudience(policy, command("旧名", "a"), command("新名", "a")) {
		t.Fatal("all split same author after rename")
	}
	mixed := command("", "a")
	mixed.Event.Data.Messages = []DispatchMessage{{SenderUID: "b"}}
	if singleDecisionAuthor(mixed) {
		t.Fatal("all bypassed original-author guard")
	}
	for _, change := range []func(*DispatchCommand){
		func(c *DispatchCommand) { c.Source.Type = "robot" },
		func(c *DispatchCommand) { c.Source.Type = "web" },
		func(c *DispatchCommand) { c.Event.Domain = "task" },
		func(c *DispatchCommand) { c.Event.Type = "task.finished" },
		func(c *DispatchCommand) { c.TaskFinishedTaskID = "task" },
	} {
		c := command("冬翔", "a")
		change(&c)
		if userDecisionEnabledForCommand(policy, c) {
			t.Fatal("all bypassed channel guard")
		}
	}
	policy.InboundCoordinatorUserDecision = false
	if userDecisionEnabledForCommand(policy, command("冬翔", "a")) {
		t.Fatal("off enabled cards")
	}
}

func TestUserDecisionModeValidationAndLegacyMapping(t *testing.T) {
	str := func(s string) *string { return &s }
	boolean := func(b bool) *bool { return &b }
	for _, tc := range []struct {
		name     string
		req      UpdateAgentRequest
		invalid  bool
		enabled  *bool
		audience string
	}{
		{"off", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("off")}, false, boolean(false), ""},
		{"all", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("all")}, false, boolean(true), "all"},
		{"named", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("named")}, false, boolean(true), "named"},
		{"unknown", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("everyone")}, true, nil, ""},
		{"empty", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("")}, true, nil, ""},
		{"conflict off", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("off"), InboundCoordinatorUserDecision: boolean(true)}, true, nil, ""},
		{"conflict all", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("all"), InboundCoordinatorUserDecision: boolean(false)}, true, nil, ""},
		{"consistent", UpdateAgentRequest{InboundCoordinatorUserDecisionMode: str("all"), InboundCoordinatorUserDecision: boolean(true)}, false, boolean(true), "all"},
		{"legacy enable", UpdateAgentRequest{InboundCoordinatorUserDecision: boolean(true)}, false, nil, "named"},
		{"legacy names", UpdateAgentRequest{InboundCoordinatorUserDecisionNames: &[]string{"冬翔"}}, false, nil, "named"},
		{"unrelated", UpdateAgentRequest{}, false, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUserDecisionMode(tc.req)
			if (err != nil) != tc.invalid {
				t.Fatalf("validation=%v", err)
			}
			if tc.invalid {
				return
			}
			var params db.UpdateAgentDingTalkResponsePolicyParams
			applyUserDecisionMode(tc.req, &params)
			if params.UserDecisionAudience.Valid != (tc.audience != "") || params.UserDecisionAudience.String != tc.audience {
				t.Fatalf("audience=%+v", params.UserDecisionAudience)
			}
			if params.UserDecision.Valid != (tc.enabled != nil) || (tc.enabled != nil && params.UserDecision.Bool != *tc.enabled) {
				t.Fatalf("enabled=%+v", params.UserDecision)
			}
		})
	}
	for _, tc := range []struct {
		coordinator, enabled bool
		audience, mode       string
	}{
		{true, true, "all", "all"}, {true, true, "named", "named"}, {true, true, "", "named"}, {true, false, "all", "off"}, {false, true, "all", "off"},
	} {
		if got := userDecisionMode(tc.coordinator, tc.enabled, tc.audience); got != tc.mode {
			t.Fatalf("mode=%q want=%q", got, tc.mode)
		}
	}
}
