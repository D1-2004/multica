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
