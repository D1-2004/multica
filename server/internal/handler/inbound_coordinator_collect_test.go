package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

func TestMergeDispatchCommandsAppendsBurst(t *testing.T) {
	base := DispatchCommand{
		CompletionCallback: &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/base/execution-result"},
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-a"},
			Messages:     []DispatchMessage{{Text: "第1项通过"}},
		}},
	}
	extra := DispatchCommand{
		CompletionCallback: &DispatchCompletionCallback{URL: "/api/v1/dispatch-tasks/extra/execution-result"},
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
	if got.CompletionCallback == nil || got.CompletionCallback.URL != base.CompletionCallback.URL {
		t.Fatalf("collect must keep the pending job callback, not the extra: %#v", got.CompletionCallback)
	}
	if len(got.ExtraCompletionCallbacks) != 1 || got.ExtraCompletionCallbacks[0].URL != extra.CompletionCallback.URL {
		t.Fatalf("collect must persist extra callbacks: %#v", got.ExtraCompletionCallbacks)
	}
}

func TestBindWindowItemEvidenceUsesMatchingMessage(t *testing.T) {
	cmd := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "冬翔"},
			Messages: []DispatchMessage{
				{OpenMsgID: "msg-a", Text: "问 dxxh", SenderDisplayName: "冬翔", SenderUID: "uid-a"},
				{OpenMsgID: "msg-b", Text: "查配额", SenderDisplayName: "SixSix", SenderUID: "uid-b", SenderOpenDingTalkID: "open-b"},
			},
		}},
	}
	used := map[string]struct{}{}
	first := cmd
	bindWindowItemEvidence(&first, inboundcoord.WindowItem{Delegator: "冬翔"}, used)
	if first.Event.Data.Sender.DisplayName != "冬翔" || first.Event.Data.Sender.UID != "uid-a" {
		t.Fatalf("first sender=%#v", first.Event.Data.Sender)
	}
	second := cmd
	bindWindowItemEvidence(&second, inboundcoord.WindowItem{Delegator: "SixSix"}, used)
	if second.Event.Data.Sender.DisplayName != "SixSix" || second.Event.Data.Sender.OpenDingTalkID != "open-b" {
		t.Fatalf("second sender=%#v", second.Event.Data.Sender)
	}
	ids := dispatchAssocIDs(second)
	if ids.EvidenceID != "msg-b" {
		t.Fatalf("second evidence=%q", ids.EvidenceID)
	}
	firstIDs := dispatchAssocIDs(first)
	if firstIDs.EvidenceID != "msg-a" {
		t.Fatalf("first evidence=%q", firstIDs.EvidenceID)
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

func TestSameCoordinatorCollectKindSeparatesAckFromWork(t *testing.T) {
	ask := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "冬翔"},
		Messages: []DispatchMessage{{Text: "帮我订下周去上海的高铁", SenderDisplayName: "冬翔"}},
	}}}
	follow := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "冬翔"},
		Messages: []DispatchMessage{{Text: "改成明天的票", SenderDisplayName: "冬翔"}},
	}}}
	ack := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "冬翔"},
		Messages: []DispatchMessage{{Text: "谢谢", SenderDisplayName: "冬翔"}},
	}}}
	thanks := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "dxxh"},
		Messages: []DispatchMessage{{Text: "好的", SenderDisplayName: "dxxh"}},
	}}}
	if sameCoordinatorCollectKind(ask, ack) {
		t.Fatal("ACK must not collect onto a parked ask")
	}
	if sameCoordinatorCollectKind(ack, ask) {
		t.Fatal("ask must not collect onto an ACK window")
	}
	if !sameCoordinatorCollectKind(ask, follow) {
		t.Fatal("follow-up asks on the same scene still collect")
	}
	if !sameCoordinatorCollectKind(ack, thanks) {
		t.Fatal("ACK lines still collect with each other")
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
