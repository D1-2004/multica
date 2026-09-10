package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func currentSpeakerRelayContext() persistedDispatchContext {
	return persistedDispatchContext{
		Source:                   DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Domain:                   "channel",
		Type:                     "message.created",
		Surface:                  DispatchSurface{Type: protocol.DispatchSurfaceTypeIssue},
		Outbound:                 DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS, ReplyTo: "latest_message"},
		CoordinatorIssueFollowUp: true,
		CoordinatorIssueTrigger:  inboundcoord.CoordinatorIssueTriggerComment,
		ReplyToOpenMsgID:         "msg-current",
		EventData: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-current", Type: "single"},
			Sender:       DispatchSender{DisplayName: "当前发信人", OpenDingTalkID: "open-current"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-current", Text: "继续当前工作并返回结果"}},
		},
	}
}

func TestCoordinatorIssueRelayUsesTrustedCurrentTargetWithoutMandatoryRoleLookup(t *testing.T) {
	for _, text := range []string{"打印 dws auth status", "读取 git status", "把刚才查到的结果告诉我"} {
		t.Run(text, func(t *testing.T) {
			stored := currentSpeakerRelayContext()
			stored.EventData.Messages[0].Text = text
			instruction := buildDispatchConversationInstruction(stored, false)
			for _, want := range []string{
				"To answer this current request, use that target directly",
				"an Issue-comment trigger alone does not require an association lookup",
				"+messages-reply --group cid-current --message-id msg-current",
				"Still read the current Issue and relevant latest comments",
				"For an actual delegated question, relay to another person, or conflicting or missing roles",
				"does not authorize unrelated outreach or access to another scene",
				"Multica comment author is only the Issue-tool executor",
				"remove processing/complete emotions",
				"Do not write or claim ‘task complete’ until any required send returns a successful receipt",
			} {
				if !strings.Contains(instruction, want) {
					t.Fatalf("current target/retained boundary missing %q: %s", want, instruction)
				}
			}
			for _, forbidden := range []string{"Before acting, explicitly map", ". Find the original delegator", "will automatically reach any DingTalk participant"} {
				if strings.Contains(instruction, forbidden) {
					t.Fatalf("unconditional role lookup or invented delivery %q: %s", forbidden, instruction)
				}
			}
		})
	}
}

func TestCoordinatorIssueRelayRequiresConsistentIdentityAndReplyLocator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*persistedDispatchContext)
		ready  bool
	}{
		{"explicit-current", func(*persistedDispatchContext) {}, true},
		{"event-locator-fallback", func(s *persistedDispatchContext) { s.ReplyToOpenMsgID = "" }, true},
		{"uid-without-open-id", func(s *persistedDispatchContext) {
			s.EventData.Sender.OpenDingTalkID = ""
			s.EventData.Sender.UID = "trusted-uid"
		}, true},
		{"missing-cid", func(s *persistedDispatchContext) { s.EventData.Conversation.OpenConversationID = "" }, false},
		{"missing-message", func(s *persistedDispatchContext) { s.ReplyToOpenMsgID = ""; s.EventData.Messages = nil }, false},
		{"different-origin", func(s *persistedDispatchContext) { s.ReplyToOpenMsgID = "msg-another" }, false},
		{"origin-without-current-event", func(s *persistedDispatchContext) { s.EventData.Messages = nil }, false},
		{"robot-name-only", func(s *persistedDispatchContext) { s.Source.Type = "robot"; s.EventData.Sender.OpenDingTalkID = "" }, false},
		{"no-dws-capability", func(s *persistedDispatchContext) { s.Outbound.Mode = protocol.DispatchOutboundModeRobotSDK }, false},
		{"no-origin-hint-emitted", func(s *persistedDispatchContext) { s.CoordinatorIssueFollowUp = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := currentSpeakerRelayContext()
			tc.change(&stored)
			instruction := buildDispatchIssueRelayInstruction(stored)
			if got := strings.Contains(instruction, "To answer this current request, use that target directly"); got != tc.ready {
				t.Fatalf("ready target=%v, want %v: %s", got, tc.ready, instruction)
			}
			if !tc.ready && !strings.Contains(instruction, "Resolve the missing or conflicting delivery facts from the original task context before sending") {
				t.Fatalf("missing target was silently guessed: %s", instruction)
			}
			if !strings.Contains(instruction, "Still read the current Issue and relevant latest comments") || !strings.Contains(instruction, "association graph as needed") {
				t.Fatalf("target optimization dropped task/relay constraints: %s", instruction)
			}
			if tc.name == "robot-name-only" && !strings.Contains(instruction, "never invent an identity or borrow the Multica Issue author") {
				t.Fatal("robot identity uncertainty was removed")
			}
		})
	}
}

func TestCoordinatorIssueRelayRetainsActualRelayResolution(t *testing.T) {
	stored := currentSpeakerRelayContext()
	stored.EventData.Messages[0].Text = "她已经答复了，把她的答复转告给最初委托的同事；不要发到其他群。"
	instruction := buildDispatchConversationInstruction(stored, false)
	for _, want := range []string{"For an actual delegated question, relay to another person", "find the original delegator", "If the triggering Issue comment contains a contacted person's answer, find the requester", "<recipient> replied: <answer>", "does not authorize unrelated outreach or access to another scene"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("actual relay resolution missing %q: %s", want, instruction)
		}
	}
	if stored.EventData.Messages[0].Text != "她已经答复了，把她的答复转告给最初委托的同事；不要发到其他群。" {
		t.Fatal("relay optimization mutated the original request")
	}
}
