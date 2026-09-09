package handler

import (
	"encoding/json"
	"testing"
)

func TestCollectedMentionsRemainPerMessageAfterDurableRoundTrip(t *testing.T) {
	command := func(id string, mentions []DispatchMention) DispatchCommand {
		return DispatchCommand{ProactiveConversation: true, Event: DispatchEvent{Data: DispatchEventData{
			Sender: DispatchSender{DisplayName: "同事", UID: "sender"}, Mentions: mentions, Messages: []DispatchMessage{{OpenMsgID: id, Text: id}},
		}}}
	}
	base := command("unmentioned", nil)
	merged := mergeDispatchCommands(base, command("for-other", []DispatchMention{{UID: "other"}}))
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	var restored DispatchCommand
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	restored = mergeDispatchCommands(restored, command("for-employee", []DispatchMention{{UID: "employee"}}))
	got := windowUtterancesFromCommand(restored)
	if len(got) != 3 || got[0].Mentions == nil || len(got[0].Mentions) != 0 || len(got[1].Mentions) != 1 || got[1].Mentions[0].UID != "other" || len(got[2].Mentions) != 1 || got[2].Mentions[0].UID != "employee" {
		t.Fatalf("mention union contaminated sources: %+v", got)
	}
	legacy := command("old", []DispatchMention{{UID: "employee"}})
	legacy.Event.Data.Messages = append(legacy.Event.Data.Messages, DispatchMessage{Text: "unrelated"})
	for _, u := range windowUtterancesFromCommand(legacy) {
		if u.Mentions != nil {
			t.Fatalf("legacy union attributed to source: %+v", u)
		}
	}
}
