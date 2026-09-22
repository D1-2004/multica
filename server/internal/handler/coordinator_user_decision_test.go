package handler

import (
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
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
