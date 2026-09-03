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
