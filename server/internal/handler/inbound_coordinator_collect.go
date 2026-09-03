package handler

import (
	"encoding/json"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// inboundCoordinatorCollectWindow waits for a short silence so several
// inbound IMs on the same scene become one Decide, like a person reading
// the chat before answering.
const inboundCoordinatorCollectWindow = 3 * time.Second

func dispatchConversationID(command DispatchCommand) string {
	return strings.TrimSpace(command.Event.Data.Conversation.OpenConversationID)
}

func mergeDispatchCommands(base, extra DispatchCommand) DispatchCommand {
	if len(extra.Event.Data.Messages) == 0 {
		return base
	}
	base.Event.Data.Messages = append(append([]DispatchMessage{}, base.Event.Data.Messages...), extra.Event.Data.Messages...)
	return base
}

func restoreJobCommand(job db.InboundCoordinatorJob) (DispatchCommand, error) {
	var command DispatchCommand
	if err := json.Unmarshal(job.Command, &command); err != nil {
		return command, err
	}
	return command, nil
}
