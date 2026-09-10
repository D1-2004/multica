package handler

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

func TestCoordinatorWindowMessageRefsPreserveProactiveAttachmentsAndOrdinaryText(t *testing.T) {
	for _, proactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("proactive=%t", proactive), func(t *testing.T) {
			attachment := DispatchMessage{OpenMsgID: "file-message", SenderUID: "sender-a", SenderDisplayName: "甲", Attachments: []DispatchAttachment{{Type: "file", Name: "requirements.pdf"}}}
			text := DispatchMessage{OpenMsgID: "text-message", SenderUID: "sender-b", SenderDisplayName: "乙", Text: "只分析刚才的附件，不要发送。"}
			command := DispatchCommand{ProactiveConversation: proactive, Event: DispatchEvent{Data: DispatchEventData{
				Sender: DispatchSender{DisplayName: "甲", UID: "sender-a"},
				Messages: []DispatchMessage{
					{OpenMsgID: "reaction", Text: "old quoted work", Reaction: &DispatchMessageReaction{EmotionName: "like", Action: "add"}},
					attachment,
					{OpenMsgID: "blank", Text: "  "},
					text,
				},
			}}}
			want := []DispatchMessage{text}
			if proactive {
				want = []DispatchMessage{attachment, text}
			}
			utterances := windowUtterancesFromCommand(command)
			if len(utterances) != len(want) {
				t.Fatalf("window has %d utterances, want %d", len(utterances), len(want))
			}
			for i, original := range want {
				u := utterances[i]
				if u.EvidenceID != original.OpenMsgID || u.SenderID != original.SenderUID || u.Sender != original.SenderDisplayName {
					t.Fatalf("u%d lost its original identity: %#v", i+1, u)
				}
				selected := windowItemCommand(command, inboundcoord.WindowItem{SourceRefs: []string{fmt.Sprintf("u%d", i+1)}, Delegator: u.Sender})
				if len(selected.Event.Data.Messages) != 1 || !reflect.DeepEqual(selected.Event.Data.Messages[0], original) {
					t.Fatalf("u%d selected a different raw message: %#v", i+1, selected.Event.Data.Messages)
				}
				if selected.Event.Data.Sender.UID != original.SenderUID || selected.Event.Data.Sender.DisplayName != original.SenderDisplayName {
					t.Fatalf("u%d inherited another sender: %#v", i+1, selected.Event.Data.Sender)
				}
			}
		})
	}
}
