package handler

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A current authored contract is a complete, bounded instruction projection.
// Otherwise retain all configured instructions; never truncate a restriction.
func employeeRoleInstructions(agent db.Agent) (string, error) {
	contract, state := coordinatorcontract.Resolve(agent.CoordinatorContract, agent.Instructions)
	if state == coordinatorcontract.StateLoaded {
		return string(coordinatorcontract.Marshal(contract)), nil
	}
	return agent.Instructions, nil
}

// Per-message identity wins as a unit. Filling one missing identifier from a
// different envelope sender would combine two people into one execution actor.
func stampEmployeeMessages(command *DispatchCommand) {
	if command == nil {
		return
	}
	sender := command.Event.Data.Sender
	for i := range command.Event.Data.Messages {
		msg := &command.Event.Data.Messages[i]
		if msg.Mentions == nil && len(command.Event.Data.Messages) == 1 && command.Event.Data.Mentions != nil {
			msg.Mentions = append([]DispatchMention{}, command.Event.Data.Mentions...)
		}
		if len(command.Event.Data.Messages) != 1 || msg.SenderUID != "" || msg.SenderOpenDingTalkID != "" || msg.SenderStaffID != "" {
			continue
		}
		msg.SenderUID = sender.UID
		msg.SenderOpenDingTalkID = firstNonEmpty(sender.OpenDingTalkID, sender.SenderOpenDingTalkID)
		msg.SenderStaffID = sender.StaffID
		if msg.SenderDisplayName == "" {
			msg.SenderDisplayName = sender.DisplayName
		}
	}
}

func employeeCatalogLabel(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return string(runes)
}
