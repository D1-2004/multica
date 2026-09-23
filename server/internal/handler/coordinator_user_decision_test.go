package handler

import (
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"net/http/httptest"
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
