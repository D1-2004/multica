package handler

import (
	"strings"
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

func TestSameCoordinatorCollectKindDoesNotInferIntent(t *testing.T) {
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
	if !sameCoordinatorCollectKind(ask, ack) {
		t.Fatal("ordinary inbound messages must share the same collect kind")
	}
	if !sameCoordinatorCollectKind(ack, ask) {
		t.Fatal("collect kind must not depend on message wording")
	}
	if !sameCoordinatorCollectKind(ask, follow) {
		t.Fatal("follow-up asks on the same scene still collect")
	}
	if !sameCoordinatorCollectKind(ack, thanks) {
		t.Fatal("ordinary inbound messages collect with each other")
	}
}

func TestSameCoordinatorCollectKindSeparatesTaskFinished(t *testing.T) {
	ask := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Sender:   DispatchSender{DisplayName: "冬翔"},
		Messages: []DispatchMessage{{Text: "帮我订下周去上海的高铁", SenderDisplayName: "冬翔"}},
	}}}
	wrapA := DispatchCommand{TaskFinishedTaskID: "task-a"}
	wrapB := DispatchCommand{TaskFinishedTaskID: "task-b"}
	if sameCoordinatorCollectKind(ask, wrapA) {
		t.Fatal("wrap-up must not collect onto inbound work")
	}
	if sameCoordinatorCollectKind(wrapA, ask) {
		t.Fatal("inbound work must not collect onto wrap-up")
	}
	if sameCoordinatorCollectKind(wrapA, wrapB) {
		t.Fatal("wrap-up for different tasks must not merge")
	}
	if !sameCoordinatorCollectKind(wrapA, DispatchCommand{TaskFinishedTaskID: "task-a"}) {
		t.Fatal("the same wrap-up task may coalesce")
	}
}

// A replica still running the old binary only recognises the pre-upgrade
// wording, and the wording only stays true while the limit is two.
func TestSceneCapacityReasonMatchesLimit(t *testing.T) {
	if inboundcoord.SceneDelegatorMaxInFlightMatters != 2 {
		t.Fatal("capacity limit changed: update sceneCapacityRejectReason wording with it")
	}
	if !strings.Contains(sceneCapacityRejectReason(), "scene already has two in-flight matters") {
		t.Fatalf("rolling deploy needs the legacy prefix: %s", sceneCapacityRejectReason())
	}
	if !isSceneCapacityReason("scene already has two in-flight matters") ||
		!isSceneCapacityReason(sceneCapacityRejectReason()) {
		t.Fatal("both the old and the new reason must be recognised as a capacity wait")
	}
	if isSceneCapacityReason("recalled issue already has a pending agent task") {
		t.Fatal("same-issue busy must not be treated as a capacity wait")
	}
}

// Two utterances of one person must land on one budget even when the messages
// present their identifiers in a different order.
func TestSceneDelegatorGroupsOneSpeakerOnce(t *testing.T) {
	first := sceneDelegator{SceneID: "scene-1", Keys: []string{"staff-1", "open-1"}}
	second := sceneDelegator{SceneID: "scene-1", Keys: []string{"open-1", "staff-1"}}
	needs := addSceneDelegatorNeed(addSceneDelegatorNeed(nil, first), second)
	if len(needs) != 1 || needs[0].needed != 2 {
		t.Fatalf("one speaker must spend one budget: %+v", needs)
	}
	other := sceneDelegator{SceneID: "scene-1", Keys: []string{"staff-2"}}
	needs = addSceneDelegatorNeed(needs, other)
	if len(needs) != 2 || needs[1].needed != 1 {
		t.Fatalf("a second speaker keeps their own budget: %+v", needs)
	}
}

func TestSceneDelegatorDoesNotInferIntent(t *testing.T) {
	sender := func(text, staffID string) DispatchCommand {
		return DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-delegator", Type: "group"},
			Sender:       DispatchSender{DisplayName: "测试号", StaffID: staffID},
			Messages:     []DispatchMessage{{Text: text, SenderDisplayName: "测试号", SenderStaffID: staffID}},
		}}}
	}
	ask := dispatchSceneDelegator(sender("问 dxxh 周五三点", "staff-a"))
	ack := dispatchSceneDelegator(sender("谢谢", "staff-a"))
	if ask.key() == "" || ask.key() != ack.key() {
		t.Fatalf("capacity must follow the person, not the wording: %q vs %q", ask.key(), ack.key())
	}
	other := dispatchSceneDelegator(sender("问 dxxh 周五三点", "staff-b"))
	if other.key() == ask.key() {
		t.Fatal("a different delegator must spend a different budget")
	}
	unknown := dispatchSceneDelegator(DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Conversation: DispatchConversation{OpenConversationID: "cid-delegator", Type: "group"},
		Messages:     []DispatchMessage{{Text: "问 dxxh 周五三点"}},
	}}})
	if len(unknown.Keys) != 0 {
		t.Fatal("a missing identity must stay unattributed instead of guessing a person")
	}
}
