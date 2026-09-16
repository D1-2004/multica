package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDispatchOriginOpenMsgIDUsesWindowEvidence(t *testing.T) {
	command := DispatchCommand{
		WindowEvidenceID: "msg-u2",
		Event: DispatchEvent{Data: DispatchEventData{
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-u1", Text: "第一问"},
				{OpenMsgID: "msg-u2", Text: "第二问"},
			},
		}},
	}
	if got := dispatchOriginOpenMsgID(command); got != "msg-u2" {
		t.Fatalf("origin = %q", got)
	}
	command.WindowEvidenceID = ""
	if got := dispatchOriginOpenMsgID(command); got != "msg-u1" {
		t.Fatalf("first remaining origin = %q", got)
	}
	command.Event.Data.Messages = []DispatchMessage{
		{OpenMsgID: "reaction-message", Text: "not an inbound request", Reaction: &DispatchMessageReaction{EmotionName: "like", Action: "add"}},
		{OpenMsgID: "blank-message", Text: "  "},
	}
	if got := dispatchOriginOpenMsgID(command); got != "" {
		t.Fatalf("guessed origin from a non-request message: %q", got)
	}
}

func TestDispatchRuntimeContextFreezesOriginOpenMsgID(t *testing.T) {
	command := windowItemCommand(DispatchCommand{
		SchemaVersion: "2.0",
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-group", Type: "group"},
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-auth", Text: "撤销认证谁能操作"},
				{OpenMsgID: "msg-name", Text: "家长姓名显示错了"},
			},
		}},
	}, inboundcoord.WindowItem{SourceRefs: []string{"u1"}})
	raw := dispatchRuntimeContext(command, "item-key")
	var stored persistedDispatchContext
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ReplyToOpenMsgID != "msg-auth" {
		t.Fatalf("task origin = %q", stored.ReplyToOpenMsgID)
	}
	if len(stored.EventData.Messages) != 1 || stored.EventData.Messages[0].OpenMsgID != "msg-auth" {
		t.Fatalf("scoped messages = %+v", stored.EventData.Messages)
	}
	params := buildAgentDispatchIssueCreateParams(command, DispatchPrompt{DisplayContent: "撤销认证谁能操作"}, agentDispatchContext{}, db.Agent{}, "item-key", agentDispatchIssueCreateOverrides{})
	if dingTalkOriginFromIssueMetadata(params.Metadata) != "msg-auth" {
		t.Fatalf("issue metadata = %s", params.Metadata)
	}
}

func TestMergeDingTalkOriginMetadataDoesNotOverwrite(t *testing.T) {
	existing := dingTalkOriginMetadata("msg-original")
	merged := mergeDingTalkOriginMetadata(existing, "msg-later")
	if dingTalkOriginFromIssueMetadata(merged) != "msg-original" {
		t.Fatalf("overwrote frozen origin: %s", merged)
	}
}

func TestApplyDingTalkOriginReplyCopiesStoredOrigin(t *testing.T) {
	policy := &protocol.DingTalkMessagePolicy{ShowAITag: true}
	applyDingTalkOriginReply(policy, persistedDispatchContext{
		ReplyToOpenMsgID: "msg-origin",
		EventData: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-origin"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-origin", Text: "查一下"}},
		},
	})
	if policy.ReplyToOpenMsgID != "msg-origin" || policy.ReplyConversationID != "cid-origin" {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestDingTalkOriginReplyHint(t *testing.T) {
	hint := dingTalkOriginReplyHint("cid-origin", "msg-origin")
	if !strings.Contains(hint, "--message-id msg-origin") || !strings.Contains(hint, "+messages-reply") {
		t.Fatalf("hint = %q", hint)
	}
	instruction := buildDispatchConversationInstruction(persistedDispatchContext{
		Source:                   DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
		Domain:                   "channel",
		Outbound:                 DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS},
		CoordinatorIssueFollowUp: true,
		ReplyToOpenMsgID:         "msg-origin",
		EventData: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-origin", Type: "group"},
			Messages:     []DispatchMessage{{OpenMsgID: "msg-origin", Text: "查一下"}},
		},
	}, false)
	if !strings.Contains(instruction, "+messages-reply --group cid-origin --message-id msg-origin") {
		t.Fatalf("follow-up instruction missing origin reply: %s", instruction)
	}
}

func TestFillDingTalkOriginReplyPrefersTaskContext(t *testing.T) {
	ctxJSON, err := json.Marshal(persistedDispatchContext{
		ReplyToOpenMsgID: "msg-task",
		EventData:        DispatchEventData{Conversation: DispatchConversation{OpenConversationID: "cid-task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	in := fillDingTalkOriginReply(dingtalkresponse.ActionInput{}, db.AgentTaskQueue{Context: ctxJSON}, db.Issue{Metadata: dingTalkOriginMetadata("msg-issue")})
	if in.ReplyToOpenMsgID != "msg-task" || in.ConversationID != "cid-task" {
		t.Fatalf("%+v", in)
	}
	in = fillDingTalkOriginReply(dingtalkresponse.ActionInput{}, db.AgentTaskQueue{}, db.Issue{Metadata: dingTalkOriginMetadata("msg-issue")})
	if in.ReplyToOpenMsgID != "msg-issue" {
		t.Fatalf("issue fallback = %+v", in)
	}
}

func TestApplyDingTalkOriginReplyFreezesQuotedSender(t *testing.T) {
	stored := persistedDispatchContext{
		ReplyToOpenMsgID: "msg-origin",
		EventData: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-origin"},
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-earlier", Text: "前一句", SenderOpenDingTalkID: "colleague", SenderDisplayName: "同事"},
				{OpenMsgID: "msg-origin", Text: "查一下", SenderOpenDingTalkID: "asker", SenderDisplayName: "冬翔"},
			},
		},
	}
	policy := &protocol.DingTalkMessagePolicy{}
	applyDingTalkOriginReply(policy, stored)
	if policy.ReplyToSenderOpenDingTalkID != "asker" || policy.ReplyToSenderDisplayName != "冬翔" {
		t.Fatalf("quoted sender = %q/%q", policy.ReplyToSenderOpenDingTalkID, policy.ReplyToSenderDisplayName)
	}
	// An origin the stored window cannot explain stays unknown.
	stored.ReplyToOpenMsgID = "msg-elsewhere"
	policy = &protocol.DingTalkMessagePolicy{}
	applyDingTalkOriginReply(policy, stored)
	if policy.ReplyToSenderOpenDingTalkID != "" || policy.ReplyToSenderDisplayName != "" {
		t.Fatalf("guessed quoted sender = %q/%q", policy.ReplyToSenderOpenDingTalkID, policy.ReplyToSenderDisplayName)
	}
}
