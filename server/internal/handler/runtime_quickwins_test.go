package handler

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"strings"
	"testing"
)

func TestTaskReplyCommandRespectsDeliveryAndQuotesLocators(t *testing.T) {
	stored := persistedDispatchContext{Source: DispatchSource{Platform: "dingtalk"}, Domain: "channel", Type: "message.created", Surface: DispatchSurface{Type: "issue"}, Outbound: DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS, ReplyTo: "latest_message"}, EventData: DispatchEventData{Conversation: DispatchConversation{OpenConversationID: "cid+abc=="}, Messages: []DispatchMessage{{OpenMsgID: "msg+abc==", Text: "hello"}}}}
	raw, _ := json.Marshal(stored)
	command := taskDingTalkReplyCommand(raw, "task-1")
	if !strings.Contains(command, "--group 'cid+abc=='") || !strings.Contains(command, "--message-id 'msg+abc=='") {
		t.Fatalf("missing quoted target: %s", command)
	}
	stored.Outbound.Mode = "callback"
	raw, _ = json.Marshal(stored)
	if taskDingTalkReplyCommand(raw, "task-1") != "" {
		t.Fatal("callback gained DWS command")
	}
	stored.Outbound.Mode = protocol.DispatchOutboundModeDWS
	stored.EventData.Conversation.OpenConversationID = "cid'; touch /tmp/pwn; '"
	raw, _ = json.Marshal(stored)
	if taskDingTalkReplyCommand(raw, "task-1") != "" {
		t.Fatal("unsafe locator admitted")
	}
}
func TestSmallSkillBatchRetainsLargeBundleFallback(t *testing.T) {
	small := []service.AgentSkillData{{Content: "small", Files: []service.AgentSkillFileData{{Content: "support"}}}}
	if !inlineSmallSkillSet(small) {
		t.Fatal("small set not batched")
	}
	small[0].Files[0].Content = strings.Repeat("x", 128*1024)
	if inlineSmallSkillSet(small) {
		t.Fatal("supporting files escaped size bound")
	}
	if inlineSmallSkillSet(make([]service.AgentSkillData, 9)) {
		t.Fatal("count bound missing")
	}
}
