package handler

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// inboundCoordinatorCollectWindow waits for a short silence so several
// inbound IMs on the same scene become one Decide, like a person reading
// the chat before answering. Each new line on a pending job re-arms this
// window. Parallel creates serialize on an advisory lock so they merge.
const (
	inboundCoordinatorCollectWindow  = 4 * time.Second
	inboundCoordinatorCollectMaxWait = 12 * time.Second
)

// coordinatorCollectQuiet is the silence that ends an agent's collect
// window: the performance switch may shorten it per agent, otherwise 4 s.
func (h *Handler) coordinatorCollectQuiet(agentID pgtype.UUID) time.Duration {
	if h != nil && h.CoordinatorCollectQuiet != nil {
		if quiet := h.CoordinatorCollectQuiet(agentID); quiet > 0 {
			return quiet
		}
	}
	return inboundCoordinatorCollectWindow
}

// coordinatorCollectDeadline bounds typing debounce independently of task
// capacity, retries, and worker scheduling. A sealed window never reopens.
func coordinatorCollectDeadline(createdAt, now time.Time, window time.Duration) time.Time {
	quiet := now.Add(window)
	maximum := createdAt.Add(inboundCoordinatorCollectMaxWait)
	if quiet.After(maximum) {
		return maximum
	}
	return quiet
}

func dispatchConversationID(command DispatchCommand) string {
	return strings.TrimSpace(command.Event.Data.Conversation.OpenConversationID)
}

// coordinatorCollectKind is the merge class for one inbound_coordinator_job.
// Wrap-up must not share a window with a live @; two wrap-ups for different
// tasks must not share a window either.
func coordinatorCollectKind(command DispatchCommand) string {
	if id := strings.TrimSpace(command.TaskFinishedTaskID); id != "" {
		return "task_finished:" + id
	}
	if command.ProactiveConversation {
		return "proactive"
	}
	return "work"
}

// Collection classes depend only on protocol and lifecycle facts. Message
// meaning, including output preferences, is decided from the complete window.
func sameCoordinatorCollectKind(base, extra DispatchCommand) bool {
	if base.ProactiveConversation != extra.ProactiveConversation || (base.ProactiveConversation && len(base.Event.Data.Messages)+len(extra.Event.Data.Messages) > 100) {
		return false
	}
	if !sameDingTalkResponsePolicy(base.ResponsePolicy, extra.ResponsePolicy) {
		return false
	}
	// Native subscription and Router deliveries complete to different
	// targets; one window never mixes them.
	if isNativeDispatchCommand(base) != isNativeDispatchCommand(extra) {
		return false
	}
	return coordinatorCollectKind(base) == coordinatorCollectKind(extra)
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
	base.Event.Data.Mentions = append(base.Event.Data.Mentions, extra.Event.Data.Mentions...)
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
		if msg.Mentions == nil && len(command.Event.Data.Messages) == 1 {
			msg.Mentions = append([]DispatchMention{}, command.Event.Data.Mentions...)
		}
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
	sender := DispatchSender{DisplayName: name}
	if strings.TrimSpace(command.Event.Data.Sender.DisplayName) == name {
		sender = command.Event.Data.Sender
	}
	for _, msg := range command.Event.Data.Messages {
		if strings.TrimSpace(msg.SenderDisplayName) != name {
			continue
		}
		sender = DispatchSender{DisplayName: name}
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
