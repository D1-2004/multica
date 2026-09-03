package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRestoreInboundCoordinatorCommandRestoresPrivateExecutionFields(t *testing.T) {
	t.Parallel()
	endpointID := parseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	targetIdentity := "router-target:v1:sha256:" + strings.Repeat("b", 64)
	original := DispatchCommand{
		DispatchEndpointID: "must-not-survive-json",
		CompletionCallback: &DispatchCompletionCallback{
			URL:    "/api/v1/dispatch-tasks/test/execution-result",
			Target: targetIdentity,
		},
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := restoreInboundCoordinatorCommand(raw, endpointID, targetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DispatchEndpointID != uuidToString(endpointID) {
		t.Fatalf("dispatch endpoint = %q", restored.DispatchEndpointID)
	}
	if restored.CompletionCallback == nil || restored.CompletionCallback.Target != targetIdentity {
		t.Fatalf("completion target = %#v", restored.CompletionCallback)
	}
}

func TestDispatchConversationTitleAliases(t *testing.T) {
	t.Parallel()
	var conv DispatchConversation
	if err := json.Unmarshal([]byte(`{"openConversationId":"cid-1","type":"group","conversationTitle":"场域回归-R7A"}`), &conv); err != nil {
		t.Fatal(err)
	}
	if conv.Title != "场域回归-R7A" || conv.Type != "group" {
		t.Fatalf("got %+v", conv)
	}
}

func TestCoordinatorJobTitleDistinguishesDMAndGroup(t *testing.T) {
	t.Parallel()
	dm := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{Type: "single"},
			Sender:       DispatchSender{DisplayName: "冬翔"},
			Messages:     []DispatchMessage{{Text: "hello"}},
		}},
	}
	if got := coordinatorJobTitle(dm); got != "单聊 · 冬翔 · hello" {
		t.Fatalf("dm title=%q", got)
	}
	group := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{Type: "group", Title: "场域回归-R7A"},
			Sender:       DispatchSender{DisplayName: "冬翔"},
			Messages:     []DispatchMessage{{Text: "what is GoalMate"}},
		}},
	}
	if got := coordinatorJobTitle(group); got != "群聊 · 场域回归-R7A · what is GoalMate" {
		t.Fatalf("group title=%q", got)
	}
	untitledGroup := DispatchCommand{
		Event: DispatchEvent{Data: DispatchEventData{
			Conversation: DispatchConversation{Type: "group"},
			Messages:     []DispatchMessage{{Text: "ping"}},
		}},
	}
	if got := coordinatorJobTitle(untitledGroup); got != "群聊 · ping" {
		t.Fatalf("untitled group title=%q", got)
	}
}
