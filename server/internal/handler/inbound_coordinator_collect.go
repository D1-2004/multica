package handler

import (
	"encoding/json"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// inboundCoordinatorCollectWindow waits for a short silence so several
// inbound IMs on the same scene become one Decide, like a person reading
// the chat before answering. Each new line on a pending job re-arms this
// window. Parallel creates serialize on an advisory lock so they merge.
const inboundCoordinatorCollectWindow = 4 * time.Second

func dispatchConversationID(command DispatchCommand) string {
	return strings.TrimSpace(command.Event.Data.Conversation.OpenConversationID)
}

// sameCoordinatorCollectKind is false when one side is thanks/OK and the
// other is a real ask. ACK windows skip the 2-task cap; mixing them onto a
// parked ask lets the model silence the ask.
func sameCoordinatorCollectKind(base, extra DispatchCommand) bool {
	return commandIsWindowAck(base) == commandIsWindowAck(extra)
}

func mergeDispatchCommands(base, extra DispatchCommand) DispatchCommand {
	stampDispatchMessageSenders(&base)
	stampDispatchMessageSenders(&extra)
	if extra.CompletionCallback != nil {
		base.ExtraCompletionCallbacks = append(append([]DispatchCompletionCallback{}, base.ExtraCompletionCallbacks...), *extra.CompletionCallback)
	}
	if len(extra.ExtraCompletionCallbacks) > 0 {
		base.ExtraCompletionCallbacks = append(base.ExtraCompletionCallbacks, extra.ExtraCompletionCallbacks...)
	}
	if len(extra.Event.Data.Messages) == 0 {
		return base
	}
	base.Event.Data.Messages = append(append([]DispatchMessage{}, base.Event.Data.Messages...), extra.Event.Data.Messages...)
	return base
}

func stampDispatchMessageSenders(command *DispatchCommand) {
	if command == nil {
		return
	}
	sender := command.Event.Data.Sender
	for i := range command.Event.Data.Messages {
		msg := &command.Event.Data.Messages[i]
		if strings.TrimSpace(msg.SenderDisplayName) == "" {
			msg.SenderDisplayName = strings.TrimSpace(sender.DisplayName)
		}
		if strings.TrimSpace(msg.SenderUID) == "" {
			msg.SenderUID = strings.TrimSpace(sender.UID)
		}
		if strings.TrimSpace(msg.SenderOpenDingTalkID) == "" {
			msg.SenderOpenDingTalkID = firstNonEmpty(sender.OpenDingTalkID, sender.SenderOpenDingTalkID)
		}
		if strings.TrimSpace(msg.SenderStaffID) == "" {
			msg.SenderStaffID = strings.TrimSpace(sender.StaffID)
		}
	}
}

func overlayDispatchSender(command DispatchCommand, delegator string) DispatchCommand {
	name := strings.TrimSpace(delegator)
	if name == "" {
		return command
	}
	if strings.TrimSpace(command.Event.Data.Sender.DisplayName) == name {
		return command
	}
	sender := DispatchSender{DisplayName: name}
	for _, msg := range command.Event.Data.Messages {
		if strings.TrimSpace(msg.SenderDisplayName) != name {
			continue
		}
		sender.UID = strings.TrimSpace(msg.SenderUID)
		if openID := strings.TrimSpace(msg.SenderOpenDingTalkID); openID != "" {
			sender.OpenDingTalkID = openID
			sender.SenderOpenDingTalkID = openID
		}
		sender.StaffID = strings.TrimSpace(msg.SenderStaffID)
		break
	}
	command.Event.Data.Sender = sender
	return command
}

func restoreJobCommand(job db.InboundCoordinatorJob) (DispatchCommand, error) {
	var command DispatchCommand
	if err := json.Unmarshal(job.Command, &command); err != nil {
		return command, err
	}
	return command, nil
}
