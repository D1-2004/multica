package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

type coordinatorDispatchSender struct {
	DisplayName          string `json:"displayName"`
	UID                  string `json:"uid"`
	OpenDingTalkID       string `json:"openDingTalkId"`
	SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
	StaffID              string `json:"staffId"`
}

type coordinatorDispatchMessage struct {
	OpenMsgID            string          `json:"openMsgId"`
	OccurredAt           int64           `json:"occurredAt"`
	Text                 string          `json:"text"`
	SenderDisplayName    string          `json:"senderDisplayName"`
	SenderUID            string          `json:"senderUid"`
	SenderOpenDingTalkID string          `json:"senderOpenDingTalkId"`
	SenderStaffID        string          `json:"senderStaffId"`
	Reaction             json.RawMessage `json:"reaction"`
	ReferencedMessage    *struct {
		OpenMsgID string `json:"openMsgId"`
		MessageID string `json:"messageId"`
		OpenID    string `json:"openId"`
		MsgID     string `json:"msgId"`
		Text      string `json:"text"`
		Content   string `json:"content"`
		SenderUID string `json:"senderUid"`
		SenderID  string `json:"senderId"`
	} `json:"referencedMessage"`
}

// restoreCoordinatorWindow accepts only the current ingress batch from the
// trusted dispatch projection. A stale context or another scene cannot replace
// the native message. Display names never establish a sender identity.
func restoreCoordinatorWindow(turn *inboundcoord.Turn, taskContext []byte, msg channel.InboundMessage) {
	utterance := inboundcoord.WindowUtterance{Sender: turn.SenderName, SenderID: turn.PersonID, Text: msg.Text, EvidenceID: turn.EvidenceID}
	if msg.ReplyTo != nil {
		utterance.ReplyToEvidenceID = msg.ReplyTo.MessageID
	}
	turn.Utterances = []inboundcoord.WindowUtterance{utterance}
	turn.MessageTimestamp = time.Time{}
	batch, ok := currentCoordinatorDispatchBatch(taskContext, msg)
	if !ok {
		return
	}
	turn.Utterances = batch
	for _, line := range batch {
		if line.Timestamp.After(turn.MessageTimestamp) {
			turn.MessageTimestamp = line.Timestamp
		}
	}
	if !turn.MessageTimestamp.IsZero() {
		turn.HistoryBefore = turn.MessageTimestamp
	}
}

func currentCoordinatorDispatchBatch(raw []byte, msg channel.InboundMessage) ([]inboundcoord.WindowUtterance, bool) {
	var envelope struct {
		EventData struct {
			Conversation struct {
				OpenConversationID string `json:"openConversationId"`
			} `json:"conversation"`
			Sender   coordinatorDispatchSender    `json:"sender"`
			Messages []coordinatorDispatchMessage `json:"messages"`
		} `json:"dispatch_event_data"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil || strings.TrimSpace(msg.MessageID) == "" {
		return nil, false
	}
	scene := assoc.NormalizeConversationID(envelope.EventData.Conversation.OpenConversationID)
	if scene == "" || scene != assoc.NormalizeConversationID(msg.Source.ChatID) {
		return nil, false
	}
	currentFound := false
	sharedSender := true
	for _, line := range envelope.EventData.Messages {
		if line.SenderDisplayName != "" || line.SenderUID != "" || line.SenderOpenDingTalkID != "" || line.SenderStaffID != "" {
			sharedSender = false
		}
		if line.OpenMsgID == msg.MessageID && !coordinatorMessageIsReaction(line) && strings.TrimSpace(line.Text) != "" {
			currentFound = true
		}
	}
	if !currentFound {
		return nil, false
	}
	batch := make([]inboundcoord.WindowUtterance, 0, len(envelope.EventData.Messages))
	for _, line := range envelope.EventData.Messages {
		if coordinatorMessageIsReaction(line) || strings.TrimSpace(line.Text) == "" {
			continue
		}
		sender := line.SenderDisplayName
		senderID := coordinatorSourceValue(line.SenderUID, line.SenderOpenDingTalkID, line.SenderStaffID)
		// The original single-sender Dispatch envelope applies to every line
		// only when no per-message author metadata exists at all. A partially
		// projected mixed batch retains unknown identities instead of borrowing
		// the top-level sender based on a matching display name.
		if sharedSender {
			sender = envelope.EventData.Sender.DisplayName
			senderID = coordinatorSourceValue(envelope.EventData.Sender.UID, envelope.EventData.Sender.OpenDingTalkID, envelope.EventData.Sender.SenderOpenDingTalkID, envelope.EventData.Sender.StaffID)
		}
		u := inboundcoord.WindowUtterance{Sender: sender, SenderID: senderID, Text: line.Text, EvidenceID: line.OpenMsgID}
		if line.OccurredAt > 0 {
			u.Timestamp = time.UnixMilli(line.OccurredAt).UTC()
		}
		if line.ReferencedMessage != nil {
			u.ReplyToEvidenceID = coordinatorSourceValue(line.ReferencedMessage.OpenMsgID, line.ReferencedMessage.OpenID, line.ReferencedMessage.MsgID, line.ReferencedMessage.MessageID)
			u.ReplyToContent = coordinatorSourceValue(line.ReferencedMessage.Text, line.ReferencedMessage.Content)
			u.ReplyToSenderID = coordinatorSourceValue(line.ReferencedMessage.SenderUID, line.ReferencedMessage.SenderID)
		}
		batch = append(batch, u)
	}
	return batch, len(batch) > 0
}

func coordinatorMessageIsReaction(line coordinatorDispatchMessage) bool {
	return len(line.Reaction) > 0 && strings.TrimSpace(string(line.Reaction)) != "null"
}

func coordinatorSourceValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// coordinatorItemTaskContext narrows a verified batch to the sources assigned
// to one work item. This keeps the sandbox's trusted actor projection aligned
// with the Coordinator plan without changing its authenticated outbound route.
func coordinatorItemTaskContext(raw []byte, msg channel.InboundMessage, item inboundcoord.WindowItem) ([]byte, error) {
	if _, ok := currentCoordinatorDispatchBatch(raw, msg); !ok {
		return raw, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope["dispatch_event_data"], &data); err != nil {
		return nil, err
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(data["messages"], &messages); err != nil {
		return nil, err
	}
	selectedRefs := make(map[string]bool, len(item.SourceRefs))
	for _, ref := range item.SourceRefs {
		selectedRefs[ref] = false
	}
	var selected []json.RawMessage
	var first coordinatorDispatchMessage
	sharedSender := true
	index := 0
	for _, message := range messages {
		var line coordinatorDispatchMessage
		if err := json.Unmarshal(message, &line); err != nil {
			return nil, err
		}
		if line.SenderDisplayName != "" || line.SenderUID != "" || line.SenderOpenDingTalkID != "" || line.SenderStaffID != "" {
			sharedSender = false
		}
		if coordinatorMessageIsReaction(line) || strings.TrimSpace(line.Text) == "" {
			continue
		}
		index++
		ref := fmt.Sprintf("u%d", index)
		if _, requested := selectedRefs[ref]; !requested {
			continue
		}
		selectedRefs[ref] = true
		if len(item.SourceRefs) > 0 && ref == item.SourceRefs[0] {
			first = line
		}
		selected = append(selected, message)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("coordinator work item has no current batch sources")
	}
	for ref, found := range selectedRefs {
		if !found {
			return nil, fmt.Errorf("coordinator work item has unknown batch source %q", ref)
		}
	}
	if !sharedSender {
		// Replace rather than overlay: retaining another speaker's staffId
		// when this source only supplies openDingTalkId creates a mixed identity.
		sender := coordinatorDispatchSender{
			DisplayName: first.SenderDisplayName, UID: first.SenderUID,
			OpenDingTalkID: first.SenderOpenDingTalkID, SenderOpenDingTalkID: first.SenderOpenDingTalkID,
			StaffID: first.SenderStaffID,
		}
		data["sender"], _ = json.Marshal(sender)
	}
	data["messages"], _ = json.Marshal(selected)
	envelope["dispatch_event_data"], _ = json.Marshal(data)
	return json.Marshal(envelope)
}
