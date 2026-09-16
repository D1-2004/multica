package handler

import (
	"encoding/json"
	"strings"

	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func dispatchOriginOpenMsgID(c DispatchCommand) string {
	if id := strings.TrimSpace(c.WindowEvidenceID); id != "" {
		return id
	}
	for _, message := range c.Event.Data.Messages {
		if message.Reaction != nil || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if id := strings.TrimSpace(message.OpenMsgID); id != "" {
			return id
		}
	}
	return ""
}

func dingTalkOriginMetadata(openMsgID string) []byte {
	openMsgID = strings.TrimSpace(openMsgID)
	if openMsgID == "" {
		return nil
	}
	raw, err := json.Marshal(map[string]string{protocol.DingTalkOriginOpenMsgIDMetadataKey: openMsgID})
	if err != nil {
		return nil
	}
	return raw
}

func mergeDingTalkOriginMetadata(existing []byte, openMsgID string) []byte {
	origin := dingTalkOriginMetadata(openMsgID)
	if len(origin) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return origin
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(existing, &meta) != nil {
		return origin
	}
	if raw, ok := meta[protocol.DingTalkOriginOpenMsgIDMetadataKey]; ok && strings.TrimSpace(string(raw)) != "" && string(raw) != "null" && string(raw) != `""` {
		return existing
	}
	encoded, _ := json.Marshal(openMsgID)
	meta[protocol.DingTalkOriginOpenMsgIDMetadataKey] = encoded
	merged, err := json.Marshal(meta)
	if err != nil {
		return existing
	}
	return merged
}

func dingTalkOriginFromIssueMetadata(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var meta map[string]any
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	value, _ := meta[protocol.DingTalkOriginOpenMsgIDMetadataKey].(string)
	return strings.TrimSpace(value)
}

func firstDispatchMessageOpenMsgID(data DispatchEventData) string {
	for _, message := range data.Messages {
		if message.Reaction != nil || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if id := strings.TrimSpace(message.OpenMsgID); id != "" {
			return id
		}
	}
	return ""
}

func dingTalkOriginFromStored(stored persistedDispatchContext) (openMsgID, conversationID string) {
	openMsgID = strings.TrimSpace(stored.ReplyToOpenMsgID)
	if openMsgID == "" {
		openMsgID = firstDispatchMessageOpenMsgID(stored.EventData)
	}
	conversationID = strings.TrimSpace(stored.EventData.Conversation.OpenConversationID)
	return openMsgID, conversationID
}

// dingTalkOriginSenderOpenDingTalkID is who wrote openMsgID, taken only from
// the stored event. An unmatched id keeps the sender unknown rather than
// guessing the window's first speaker.
func dingTalkOriginSenderOpenDingTalkID(data DispatchEventData, openMsgID string) string {
	openMsgID = strings.TrimSpace(openMsgID)
	if openMsgID == "" {
		return ""
	}
	for _, message := range data.Messages {
		if strings.TrimSpace(message.OpenMsgID) != openMsgID {
			continue
		}
		if id := strings.TrimSpace(message.SenderOpenDingTalkID); id != "" {
			return id
		}
		break
	}
	return ""
}

func applyDingTalkOriginReply(policy *protocol.DingTalkMessagePolicy, stored persistedDispatchContext) {
	if policy == nil {
		return
	}
	openMsgID, conversationID := dingTalkOriginFromStored(stored)
	policy.ReplyToOpenMsgID = openMsgID
	policy.ReplyConversationID = conversationID
	policy.ReplyToSenderOpenDingTalkID = dingTalkOriginSenderOpenDingTalkID(stored.EventData, openMsgID)
}

func fillDingTalkOriginReply(in dingtalkresponse.ActionInput, task db.AgentTaskQueue, issue db.Issue) dingtalkresponse.ActionInput {
	var stored persistedDispatchContext
	if len(task.Context) > 0 {
		_ = json.Unmarshal(task.Context, &stored)
	}
	openMsgID, conversationID := dingTalkOriginFromStored(stored)
	if openMsgID == "" {
		openMsgID = dingTalkOriginFromIssueMetadata(issue.Metadata)
	}
	if in.ReplyToOpenMsgID == "" {
		in.ReplyToOpenMsgID = openMsgID
	}
	if in.ConversationID == "" {
		in.ConversationID = conversationID
	}
	if in.IssueID == "" && issue.ID.Valid {
		in.IssueID = uuidToString(issue.ID)
	}
	if in.TaskID == "" && task.ID.Valid {
		in.TaskID = uuidToString(task.ID)
	}
	return in
}

func dingTalkOriginReplyHint(cid, openMsgID string) string {
	cid = strings.TrimSpace(cid)
	openMsgID = strings.TrimSpace(openMsgID)
	if cid == "" || openMsgID == "" {
		return ""
	}
	return "- origin reply: `dws chat +messages-reply --group " + cid + " --message-id " + openMsgID + " --content <text> --yes`"
}
