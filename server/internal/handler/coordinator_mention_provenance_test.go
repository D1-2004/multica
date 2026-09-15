package handler

import (
	"encoding/json"
	"testing"
)

func TestDispatchMentionsEmployeeAcceptsDecimalUIDInOpenID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mention DispatchMention
		want    bool
	}{
		{"numeric receiving account", DispatchMention{OpenDingTalkID: "6899376218"}, true},
		{"explicit different UID wins", DispatchMention{UID: "other", OpenDingTalkID: "6899376218"}, false},
		{"explicit receiving UID", DispatchMention{UID: "6899376218", OpenDingTalkID: "opaque"}, true},
		{"different numeric account", DispatchMention{OpenDingTalkID: "6899376219"}, false},
		{"opaque unresolved account", DispatchMention{OpenDingTalkID: "opaque"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := DispatchCommand{
				ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "6899376218", OrgID: "org"}},
				Event:            DispatchEvent{Data: DispatchEventData{Mentions: []DispatchMention{tc.mention}}},
			}
			if got := dispatchMentionsEmployee(command); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			command.ExternalIdentity.DWS = nil
			if dispatchMentionsEmployee(command) {
				t.Fatal("matched without receiving identity")
			}
		})
	}
}

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
