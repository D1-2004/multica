package handler

import (
	"testing"
)

func TestMergeDispatchCommandsAppendsBurst(t *testing.T) {
	base := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-a"},
			Messages:     []DispatchMessage{{Text: "第1项通过"}},
		}},
	}
	extra := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-a"},
			Messages:     []DispatchMessage{{Text: "第2项通过"}, {Text: "第3项通过"}},
		}},
	}
	got := mergeDispatchCommands(base, extra)
	if len(got.Event.Data.Messages) != 3 {
		t.Fatalf("got %d messages", len(got.Event.Data.Messages))
	}
	if got.Event.Data.Messages[2].Text != "第3项通过" {
		t.Fatalf("merged=%#v", got.Event.Data.Messages)
	}
}

func TestMergeDispatchCommandsStampsPerMessageDelegator(t *testing.T) {
	base := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Sender:       DispatchSender{DisplayName: "测试号", UID: "uid-test", OpenDingTalkID: "open-test"},
			Conversation: DispatchConversation{OpenConversationID: "cid-a"},
			Messages:     []DispatchMessage{{Text: "问 dxxh 周五三点"}},
		}},
	}
	extra := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Sender:       DispatchSender{DisplayName: "dxxh", UID: "uid-dxxh", OpenDingTalkID: "open-dxxh"},
			Conversation: DispatchConversation{OpenConversationID: "cid-a"},
			Messages:     []DispatchMessage{{Text: "查一下今天 token"}},
		}},
	}
	got := mergeDispatchCommands(base, extra)
	if got.Event.Data.Messages[0].SenderDisplayName != "测试号" {
		t.Fatalf("first sender=%q", got.Event.Data.Messages[0].SenderDisplayName)
	}
	if got.Event.Data.Messages[1].SenderDisplayName != "dxxh" {
		t.Fatalf("second sender=%q", got.Event.Data.Messages[1].SenderDisplayName)
	}
	if got.Event.Data.Messages[1].SenderUID != "uid-dxxh" {
		t.Fatalf("second uid=%q", got.Event.Data.Messages[1].SenderUID)
	}
}

func TestDispatchConversationID(t *testing.T) {
	cmd := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Conversation: DispatchConversation{OpenConversationID: " cid-x "},
	}}}
	if dispatchConversationID(cmd) != "cid-x" {
		t.Fatalf("got %q", dispatchConversationID(cmd))
	}
}

func TestCoordinatorIssueFollowUp(t *testing.T) {
	if coordinatorIssueFollowUp([]byte(`{"coordinator_issue_follow_up":true}`)) != true {
		t.Fatal("boolean true should match")
	}
	if coordinatorIssueFollowUp([]byte(`{"other":true}`)) {
		t.Fatal("missing key must be false")
	}
}

func TestClipTaskCompleteOutput(t *testing.T) {
	got := clipTaskCompleteOutput([]byte(`{"output":"须莫说周五三点可以开会。"}`), 80)
	if got != "须莫说周五三点可以开会。" {
		t.Fatalf("got %q", got)
	}
}

func TestOverlayDispatchSenderCopiesDelegator(t *testing.T) {
	base := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender: DispatchSender{DisplayName: "测试号", UID: "uid-test", OpenDingTalkID: "open-test"},
		Messages: []DispatchMessage{
			{Text: "问 dxxh", SenderDisplayName: "测试号", SenderUID: "uid-test", SenderOpenDingTalkID: "open-test"},
			{Text: "查 token", SenderDisplayName: "dxxh", SenderUID: "uid-dxxh", SenderOpenDingTalkID: "open-dxxh"},
		},
	}}}
	got := overlayDispatchSender(base, "dxxh")
	if got.Event.Data.Sender.DisplayName != "dxxh" {
		t.Fatalf("overlay=%q", got.Event.Data.Sender.DisplayName)
	}
	if got.Event.Data.Sender.UID != "uid-dxxh" || got.Event.Data.Sender.OpenDingTalkID != "open-dxxh" {
		t.Fatalf("item 2 must not keep speaker 1 ids: %#v", got.Event.Data.Sender)
	}
	if base.Event.Data.Sender.DisplayName != "测试号" || base.Event.Data.Sender.UID != "uid-test" {
		t.Fatal("must not mutate the merged command sender")
	}
	if overlayDispatchSender(base, "  ").Event.Data.Sender.DisplayName != "测试号" {
		t.Fatal("empty delegator must keep original sender")
	}
}

func TestShouldParkSceneCapacitySkipsAckWindows(t *testing.T) {
	ask := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "测试号"},
		Messages: []DispatchMessage{{Text: "问 dxxh 周五三点", SenderDisplayName: "测试号"}},
	}}}
	ack := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "测试号"},
		Messages: []DispatchMessage{{Text: "谢谢", SenderDisplayName: "测试号"}},
	}}}
	if !shouldParkSceneCapacity(ask, 2) {
		t.Fatal("two active tasks must park a real ask")
	}
	if shouldParkSceneCapacity(ack, 2) {
		t.Fatal("ACK window must not wait for a sandbox slot")
	}
	if shouldParkSceneCapacity(ask, 1) {
		t.Fatal("one active task is under the cap")
	}
}
