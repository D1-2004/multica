package clicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dws/clicompat/chatmsg"
	apperrors "github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/errors"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/output"
)

// Output renderers: the stdout bytes dws prints on success. Each takes the
// payload CheckPayload returned (one JSON object, or "null") and runs the
// statements of the dws command's output path around dws's own projection
// and rendering functions.

// MessageListOutput is the stdout of `chat message list` (contracts §1.4):
// callProjectedMessagesOnServer (internal/helpers/chat.go:176-190) with
// projectChatMessagesPayloadForCommand (chat.go:312-319) and
// applyProjectedChatPagination, printed by writeProjectedChatPayload's legacy
// branch. The SafeChat decrypt ledger is the one release builds print (see
// decryptLedger). direction is "older" or "newer" as passed to
// MessageListArgs ("" means newer, as with a time).
func MessageListOutput(payload []byte, direction string) []byte {
	paginationDirection := "older"
	if directionForward(direction, true) {
		paginationDirection = "newer"
	}
	return renderProjectedChatPayload(payload, func(data map[string]any) map[string]any {
		// projectChatMessagesPayloadForCommand with search=false.
		items := chatmsg.ListMessageItems(data)
		ledger := decryptLedger(items)
		payload := projectChatMessagesPayloadWithLedger(data, false, ledger)
		applyProjectedChatPagination(payload, data, paginationDirection)
		return payload
	})
}

// MessagesByIDsOutput is the stdout of `chat message list-by-ids`
// (contracts §5.3): callProjectedAtomicIMMessages (internal/helpers/chat.go:233)
// with projectExistingChatMessageCollectionsForCommand (chat.go:321-331),
// search=true.
func MessagesByIDsOutput(payload []byte) []byte {
	return renderProjectedChatPayload(payload, func(data map[string]any) map[string]any {
		items := chatmsg.SearchItems(data)
		ledger := decryptLedger(items)
		payload := projectExistingChatMessageCollections(data)
		for key, value := range ledger {
			payload[key] = value
		}
		return payload
	})
}

// SendStatusOutput is the stdout of `chat message query-send-status`
// (contracts §4.3): callProjectedIMMessageSendStatus
// (internal/helpers/chat.go:246-259) with chatmsg.ProjectMessageSendStatus.
func SendStatusOutput(payload []byte, openTaskID string) []byte {
	return renderProjectedChatPayload(payload, func(data map[string]any) map[string]any {
		return chatmsg.ProjectMessageSendStatus(data, openTaskID)
	})
}

// renderProjectedChatPayload is the legacy branch of writeProjectedChatPayload
// (internal/helpers/chat.go:271-305) with writeCommandPayload's JSON output
// (output.WriteJSON). dws fails on an empty text; the payloads CheckPayload
// returns are never empty, and an empty one is read here as "null".
func renderProjectedChatPayload(payload []byte, project func(map[string]any) map[string]any) []byte {
	text := string(payload)
	if strings.TrimSpace(text) == "" {
		text = "null"
	}
	data := map[string]any{}
	if err := unmarshalJSONUseNumber(text, &data); err != nil {
		return printRaw(text)
	}
	projected := project(data)
	return writeCommandJSON(projected)
}

// decryptCandidate holds the fields of messagecrypto.BatchDecryptItem that
// decryptLedger uses.
type decryptCandidate struct {
	MessageID      string
	ConversationID string
	Ciphertext     string
}

// decryptLedger is decryptProjectedChatMessagesByPolicy
// (internal/helpers/chat.go:362-396) as release builds run it (SafeChat
// backend present) when no message can be decrypted: clicompat has no SafeChat
// session, so every candidate fails at the policy step, the way a policy
// lookup error fails it (filterProjectedDecryptItemsByPolicy, chat.go:444),
// with reason "decrypt_unavailable". The ciphertext stays in content and
// chatmsg renders text as "[加密消息，无法解码]".
func decryptLedger(items []map[string]any) map[string]any {
	batchItems := make([]decryptCandidate, 0)
	for _, item := range items {
		messageID := strings.TrimSpace(fmt.Sprint(chatmsg.MessageID(item)))
		if messageID == "" || messageID == "<nil>" {
			continue
		}
		content := strings.TrimSpace(fmt.Sprint(firstChatMapValue(item, "content", "text")))
		if chatmsg.IsEncrypted(content) {
			conversationID := strings.TrimSpace(fmt.Sprint(chatmsg.ConversationID(item)))
			if conversationID == "<nil>" {
				conversationID = ""
			}
			batchItems = append(batchItems, decryptCandidate{
				MessageID:      messageID,
				ConversationID: conversationID,
				Ciphertext:     content,
			})
		}
	}
	decryptCandidateCount := len(batchItems)
	policyFailures := make([]map[string]any, 0)
	for _, item := range batchItems {
		policyFailures = append(policyFailures, chatDecryptFailure(item.MessageID, item.ConversationID, "decrypt_unavailable"))
	}
	batchItems = batchItems[:0]
	ledger := map[string]any{
		"decryptCandidateCount": decryptCandidateCount,
		"decryptAllowedCount":   len(batchItems),
		"decryptedCount":        0,
		"decryptFailedCount":    len(policyFailures),
	}
	if len(policyFailures) > 0 {
		ledger["decryptFailures"] = policyFailures
		ledger["partial"] = true
	}
	return ledger
}

// LegacyOutput is the helper print path used by `chat data-auth cross-org`
// and `chat message update-a2ui-card`: renderLegacyMCPText
// (internal/helpers/helpers.go:624-650) with --format json and
// Formatter.PrintJSON (internal/helpers/output.go:73-80) — plain
// encoding/json values (numbers become float64), HTML-escaped, indented.
func LegacyOutput(payload []byte) []byte {
	text := string(payload)
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		_ = enc.Encode(parsed)
		return buf.Bytes()
	}
	return printRaw(text)
}

// SendOutput is the unified stdout of `chat message send` (contracts §3A.3):
// sendPersonalMessageForCommand (internal/helpers/chat.go:617-636) after
// CallMCPToolDataOnServer, emitted by emitJSON. A null payload yields a
// success envelope without data, as in dws.
func SendOutput(payload []byte) []byte {
	data, err := decodeToolData("send_personal_message", payload)
	if err != nil {
		return newFailure(err).UnifiedJSON()
	}
	if response, ok := data.(map[string]any); ok {
		receipt := chatmsg.ProjectMessageSendReceipt(response)
		if taskID, _ := receipt["openTaskId"].(string); taskID != "" {
			return emitJSON(output.Pending(data, &output.OperationInfo{
				ID:          taskID,
				State:       "processing",
				NextCommand: "dws chat message query-send-status --open-task-id " + taskID,
			}))
		}
	}
	return emitJSON(output.Success(data))
}

// ReplyOutput is the stdout of `chat +messages-reply` (contracts §3B.3):
// MessagesReply.Execute after its send (internal/shortcut/chat/lark_alignment.go:274-279)
// with enrichReplyResult (lark_alignment.go:285-318) inlined for no
// --ref-sender, printed by WriteCommandPayload (output.WriteJSON). For a null
// payload dws panics on the nil map (exit 5); the enrichment is printed alone.
func ReplyOutput(payload []byte, f ReplyFlags, conversationID, senderOpenDingTalkID string) []byte {
	data, err := decodeCallMCPData("send_personal_message", payload)
	if err != nil || data == nil {
		data = map[string]any{}
	}
	refSender := senderOpenDingTalkID
	data["contractVersion"] = "im.message-reply.v1"
	data["conversationId"] = strings.TrimSpace(f.ConversationID)
	data["referencedMessage"] = map[string]any{
		"messageId":            strings.TrimSpace(f.MessageID),
		"senderOpenDingTalkId": refSender,
		"resolutionSource":     "message_lookup",
	}
	if key := strings.TrimSpace(f.IdempotencyKey); key != "" {
		data["idempotencyKey"] = key
	}
	if value := replyResponseValue(data, "openMessageId", "openMsgId", "messageId", "msgId"); value != nil {
		data["messageId"] = value
	}
	if value := replyResponseValue(data, "openConvThreadId", "threadId", "topicId"); value != nil {
		data["threadId"] = value
	}
	if value := replyResponseValue(data, "deliveryStatus", "sendStatus", "status"); value != nil {
		data["deliveryStatus"] = value
		data["deliveryStatusKnown"] = true
	} else {
		data["deliveryStatus"] = "unknown"
		data["deliveryStatusKnown"] = false
	}
	data["conversationId"] = conversationID
	return writeCommandJSON(data)
}

// A2UISendOutput is the stdout of `chat +messages-send --msg-type a2ui`
// (contracts §6A): executeUnifiedMessageWrite
// (internal/shortcut/chat/unified_send.go:510-523) for tool
// create_and_send_a2ui_card, printed by WriteCommandPayload
// (output.WriteJSON). A null payload prints "result": null.
func A2UISendOutput(payload []byte) []byte {
	data, err := decodeCallMCPData("create_and_send_a2ui_card", payload)
	if err != nil {
		data = nil
	}
	return writeCommandJSON(map[string]any{
		"ok":       true,
		"identity": "user",
		"tool":     "create_and_send_a2ui_card",
		"result":   data,
	})
}

// decodeCallMCPData is RuntimeContext.callMCPData's decoding
// (internal/shortcut/runner.go:260-270).
func decodeCallMCPData(tool string, payload []byte) (map[string]any, error) {
	text := string(payload)
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, apperrors.NewInternal(fmt.Sprintf("解析 %s 返回失败: %v", tool, err))
	}
	return out, nil
}

// decodeToolData is CallMCPToolDataOnServer's decoding
// (internal/helpers/helpers.go:395-411).
func decodeToolData(toolName string, payload []byte) (any, error) {
	text := string(payload)
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	var data any
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&data); err != nil {
		return nil, apperrors.NewInternal(fmt.Sprintf("解析 %s 返回失败: %v", toolName, err))
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("存在多个 JSON 值")
		}
		return nil, apperrors.NewInternal(fmt.Sprintf("解析 %s 返回失败: %v", toolName, err))
	}
	return data, nil
}

// writeCommandJSON is dws's output.WriteJSON (internal/output/formatter.go:385),
// the renderer of writeCommandPayload and WriteCommandPayload for
// --format json.
func writeCommandJSON(payload any) []byte {
	var buf bytes.Buffer
	if err := output.WriteJSON(&buf, payload); err != nil {
		return (&Failure{err: err}).LegacyJSON()
	}
	return buf.Bytes()
}

// printRaw is Formatter.PrintRaw (internal/helpers/output.go:101-106).
func printRaw(text string) []byte {
	var buf bytes.Buffer
	fmt.Fprint(&buf, text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(&buf)
	}
	return buf.Bytes()
}
