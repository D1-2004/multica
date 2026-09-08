package inboundcoord

import "strings"

// TaskDeliveryContext contains positive delivery evidence from one execution's
// persisted tool transcript. Missing receipts never prove that nothing was sent.
type TaskDeliveryContext struct {
	Status          string                 `json:"status"`
	TaskID          string                 `json:"task_id"`
	Scope           string                 `json:"scope"`
	Complete        bool                   `json:"complete"`
	Truncated       bool                   `json:"truncated"`
	Coverage        string                 `json:"coverage"`
	Deliveries      []TaskDeliveryEvidence `json:"deliveries"`
	UnverifiedSends int                    `json:"unverified_sends"`
}

type TaskDeliveryEvidence struct {
	ConversationID string `json:"conversation_id"`
	MessageID      string `json:"message_id"`
	SourceSeq      int32  `json:"source_seq"`
	SentText       string `json:"sent_text,omitempty"`
	TextComplete   bool   `json:"text_complete"`
}

// TaskFinishedResultAlreadyDelivered only short-circuits a verbatim result
// backed by a successful send receipt from this execution to this scene.
// Semantic coverage is left to the completion loop with the same evidence;
// an earlier question, a purpose card, or another run cannot prove delivery.
func TaskFinishedResultAlreadyDelivered(result, sceneCID string, delivery TaskDeliveryContext) bool {
	result, sceneCID = strings.TrimSpace(result), strings.TrimSpace(sceneCID)
	if result == "" || sceneCID == "" || delivery.Status != "loaded" || delivery.Scope != "current_task" || delivery.TaskID == "" {
		return false
	}
	for _, sent := range delivery.Deliveries {
		if sent.ConversationID == sceneCID && sent.MessageID != "" && sent.TextComplete && strings.TrimSpace(sent.SentText) == result {
			return true
		}
	}
	return false
}

// FilterTaskFinishedWrapup only removes empty speech. Delivery evidence, rather
// than phrases such as "已确认" or "等他回", decides whether useful speech is due.
func FilterTaskFinishedWrapup(decision Decision) Decision {
	if decision.Action != ActionReply || strings.TrimSpace(decision.UserText) != "" {
		return decision
	}
	decision.Action = ActionSilence
	decision.UserText = ""
	if strings.TrimSpace(decision.Reason) == "" {
		decision.Reason = "empty_wrapup"
	}
	return decision
}
