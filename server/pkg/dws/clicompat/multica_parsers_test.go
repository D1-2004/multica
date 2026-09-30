package clicompat

// The renderers exist to feed Multica's parsers the bytes they already
// accept. The functions below are trimmed copies of those parsers
// (dt-fde-multica server/internal/dwsclient/{client,send,a2ui,message_sender}.go,
// server/internal/service/inboundcoord/dws_history.go parseDWSHistory and
// server/internal/service/scenememory/dws.go parseDWSPage) keeping every
// decode step and check that decides accept/reject; logging and redaction
// helpers are dropped.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- server/internal/dwsclient/client.go ----

func multicaSafeCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return "operation_failed"
	}
	for _, r := range raw {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return "operation_failed"
		}
	}
	return raw
}

func multicaHistoryCLIError(raw []byte) map[string]string {
	var envelope struct {
		Error map[string]json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Error == nil {
		return nil
	}
	fields := map[string]string{}
	for _, key := range []string{"category", "reason", "server_error_code", "trace_id"} {
		var value string
		if json.Unmarshal(envelope.Error[key], &value) != nil {
			var number json.Number
			if key != "server_error_code" || json.Unmarshal(envelope.Error[key], &number) != nil {
				continue
			}
			value = number.String()
		}
		value = strings.TrimSpace(value)
		if value != "" && multicaSafeCode(value) == value {
			fields[key] = value
		}
	}
	return fields
}

func multicaConfirmCrossOrgGrant(raw []byte) error {
	var response struct {
		Success bool `json:"success"`
		Result  struct {
			Scope     string `json:"scope"`
			GrantType string `json:"grantType"`
			ExpireAt  int64  `json:"expireAt"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Success || response.Result.Scope != "chat.data:cross-org" ||
		response.Result.GrantType != "timed" || response.Result.ExpireAt <= time.Now().UnixMilli() {
		return errors.New("DWS cross-org chat read renewal was not confirmed")
	}
	return nil
}

// ---- server/internal/dwsclient/send.go ----

type multicaMessageError struct {
	fields           map[string]string
	duplicateRequest bool
	providerMessage  string
}

func multicaMessageCLIError(raw []byte) *multicaMessageError {
	fields := multicaHistoryCLIError(raw)
	if fields == nil {
		return nil
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	return &multicaMessageError{
		fields:           fields,
		duplicateRequest: fields["server_error_code"] == "1001" && strings.Contains(envelope.Error.Message, "Request is repeated with uuid '"),
		providerMessage:  envelope.Error.Message,
	}
}

func (e *multicaMessageError) cardSendRejection() bool {
	if e.fields["category"] != "api" || e.fields["reason"] != "business_error" {
		return false
	}
	switch e.fields["server_error_code"] {
	case "A2UI_TARGET_INVALID", "INVALID_PARAM", "FORBIDDEN", "PERMISSION_DENIED":
		return true
	}
	return false
}

type multicaSendEnvelope struct {
	Success            *bool           `json:"success"`
	ErrorCode          string          `json:"errorCode"`
	Result             json.RawMessage `json:"result"`
	OpenTaskID         string          `json:"openTaskId"`
	OpenMessageID      string          `json:"openMessageId"`
	OpenConversationID string          `json:"openConversationId"`
	SendStatus         json.RawMessage `json:"sendStatus"`
}

func multicaDecodeSendEnvelope(raw []byte) (multicaSendEnvelope, error) {
	var wrapper struct {
		OK   *bool           `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && wrapper.OK != nil {
		if !*wrapper.OK {
			return multicaSendEnvelope{}, errors.New("DWS message command rejected")
		}
		if len(wrapper.Data) > 0 {
			return multicaDecodeSendEnvelope(wrapper.Data)
		}
	}
	var outer multicaSendEnvelope
	if err := json.Unmarshal(raw, &outer); err != nil {
		return outer, errors.New("decode DWS message response")
	}
	if outer.Success == nil {
		return outer, errors.New("DWS message response omitted success")
	}
	if !*outer.Success {
		return outer, errors.New("rejected: " + outer.ErrorCode)
	}
	if len(outer.Result) > 0 && string(outer.Result) != "null" {
		var nested multicaSendEnvelope
		if err := json.Unmarshal(outer.Result, &nested); err != nil {
			return outer, errors.New("decode DWS message result")
		}
		if nested.Success != nil && !*nested.Success {
			return nested, errors.New("rejected: " + nested.ErrorCode)
		}
		if nested.OpenTaskID != "" {
			outer.OpenTaskID = nested.OpenTaskID
		}
		if nested.OpenMessageID != "" {
			outer.OpenMessageID = nested.OpenMessageID
		}
		if nested.OpenConversationID != "" {
			outer.OpenConversationID = nested.OpenConversationID
		}
		if len(nested.SendStatus) > 0 {
			outer.SendStatus = nested.SendStatus
		}
	}
	return outer, nil
}

func multicaParseSendResult(raw []byte) (string, error) {
	payload, err := multicaDecodeSendEnvelope(raw)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.OpenTaskID) == "" {
		return "", errors.New("DWS send response omitted openTaskId")
	}
	return payload.OpenTaskID, nil
}

func multicaParseSendStatus(raw []byte) (string, error) {
	payload, err := multicaDecodeSendEnvelope(raw)
	if err != nil {
		return "", err
	}
	var state string
	_ = json.Unmarshal(payload.SendStatus, &state)
	switch strings.ToUpper(state) {
	case "FAIL", "FAILED", "FAILURE", "CANCELLED", "CANCELED":
		return "failed", nil
	case "SUCCESS", "SUCCEEDED", "DELIVERED", "SEND_SUCCESS":
		if strings.TrimSpace(payload.OpenMessageID) != "" && strings.TrimSpace(payload.OpenConversationID) != "" {
			return "delivered", nil
		}
	}
	return "provider_accepted", nil
}

// ---- server/internal/dwsclient/message_sender.go ----

func multicaParseMessageSender(raw []byte, conversationID, messageID string) (string, error) {
	invalid := errors.New("DWS source message identity could not be verified")
	var envelope struct {
		OK       *bool             `json:"ok"`
		Success  *bool             `json:"success"`
		Data     json.RawMessage   `json:"data"`
		Result   json.RawMessage   `json:"result"`
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", invalid
	}
	if envelope.OK != nil {
		if !*envelope.OK || len(envelope.Data) == 0 {
			return "", invalid
		}
		return multicaParseMessageSender(envelope.Data, conversationID, messageID)
	}
	if envelope.Success == nil || !*envelope.Success {
		return "", invalid
	}
	messages := envelope.Messages
	if len(envelope.Result) > 0 {
		var result struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if json.Unmarshal(envelope.Result, &result) != nil {
			return "", invalid
		}
		if len(result.Messages) > 0 {
			messages = result.Messages
		}
	}
	if len(messages) != 1 {
		return "", invalid
	}
	var message struct {
		MessageID            string `json:"messageId"`
		OpenMessageID        string `json:"openMessageId"`
		ConversationID       string `json:"conversationId"`
		OpenConversationID   string `json:"openConversationId"`
		SenderOpenDingTalkID string `json:"senderOpenDingTalkId"`
		SenderID             string `json:"senderId"`
	}
	if json.Unmarshal(messages[0], &message) != nil {
		return "", invalid
	}
	match := func(a, b, want string) bool {
		return (a != "" || b != "") && (a == "" || a == want) && (b == "" || b == want)
	}
	if !match(message.MessageID, message.OpenMessageID, messageID) || !match(message.ConversationID, message.OpenConversationID, conversationID) {
		return "", invalid
	}
	sender := strings.TrimSpace(message.SenderOpenDingTalkID)
	if sender == "" || strings.ContainsAny(sender, " ,<>\t\n\r") || message.SenderID != "" && message.SenderID != sender {
		return "", invalid
	}
	return sender, nil
}

// ---- server/internal/dwsclient/a2ui.go ----

func multicaParseA2UIReceipt(raw []byte) (string, int64, error) {
	var response struct {
		OK     bool `json:"ok"`
		Result struct {
			Success bool `json:"success"`
			Result  struct {
				BizID          string `json:"bizId"`
				CardInstanceID int64  `json:"cardInstanceId"`
			} `json:"result"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.OK || !response.Result.Success ||
		response.Result.Result.BizID == "" || response.Result.Result.CardInstanceID == 0 {
		return "", 0, errors.New("A2UI send outcome is unconfirmed")
	}
	return response.Result.Result.BizID, response.Result.Result.CardInstanceID, nil
}

func multicaUpdateA2UIConfirmed(raw []byte) bool {
	var result struct {
		Success bool `json:"success"`
	}
	return json.Unmarshal(raw, &result) == nil && result.Success
}

// ---- server/internal/service/inboundcoord/dws_history.go (parseDWSHistory) ----

type multicaHistoryLine struct {
	id, sender, senderID, content, replyTo string
	at                                     time.Time
}

func multicaParseDWSHistory(raw []byte) ([]multicaHistoryLine, error) {
	var payload struct {
		ContractVersion string          `json:"contractVersion"`
		Success         bool            `json:"success"`
		ErrorCode       string          `json:"errorCode"`
		Messages        json.RawMessage `json:"messages"`
		Result          json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("decode DWS conversation history response")
	}
	messagesRaw := payload.Messages
	if len(messagesRaw) == 0 && len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(payload.Result, &nested); err != nil {
			return nil, err
		}
		messagesRaw = nested.Messages
	}
	var messages []struct {
		Content       string          `json:"content"`
		CreateTime    json.RawMessage `json:"createTime"`
		OpenMessageID string          `json:"openMessageId"`
		Sender        string          `json:"sender"`
		SenderUID     string          `json:"senderUid"`
		SenderID      string          `json:"senderId"`
		QuotedMessage *struct {
			Content       string `json:"content"`
			OpenMessageID string `json:"openMessageId"`
		} `json:"quotedMessage"`
	}
	if len(messagesRaw) > 0 && string(messagesRaw) != "null" {
		if err := json.Unmarshal(messagesRaw, &messages); err != nil {
			return nil, errors.New("decode DWS conversation history messages")
		}
	}
	if !payload.Success && len(messages) == 0 {
		return nil, errors.New("rejected " + payload.ErrorCode)
	}
	if len(messagesRaw) == 0 || string(messagesRaw) == "null" {
		return nil, errors.New("DWS conversation history response is missing messages")
	}
	var lines []multicaHistoryLine
	for _, message := range messages {
		var at time.Time
		var display string
		if json.Unmarshal(message.CreateTime, &display) == nil && payload.ContractVersion == "im.message-list.v1" {
			at, _ = time.ParseInLocation("2006-01-02 15:04:05", display, time.FixedZone("Asia/Shanghai", 8*60*60))
		}
		line := multicaHistoryLine{id: message.OpenMessageID, sender: message.Sender, content: message.Content, at: at}
		line.senderID = message.SenderUID
		if line.senderID == "" {
			line.senderID = message.SenderID
		}
		if message.QuotedMessage != nil {
			line.replyTo = message.QuotedMessage.OpenMessageID
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// ---- server/internal/service/scenememory/dws.go (parseDWSPage) ----

type multicaPage struct {
	events          int
	paginationKnown bool
	hasMore         bool
	nextCursor      time.Time
	senderIDs       []string
}

func multicaParseDWSPage(raw []byte) (multicaPage, error) {
	type listMessage struct {
		Content       string `json:"content"`
		Text          string `json:"text"`
		CreateTime    string `json:"createTime"`
		OpenMessageID string `json:"openMessageId"`
		MessageID     string `json:"messageId"`
		Sender        string `json:"sender"`
		SenderID      string `json:"senderId"`
		SenderOpenID  string `json:"senderOpenId"`
		IsSelf        *bool  `json:"isSelf"`
		Self          bool   `json:"self"`
		SenderType    string `json:"senderType"`
	}
	var payload struct {
		Success    bool            `json:"success"`
		ErrorCode  string          `json:"errorCode"`
		Messages   []listMessage   `json:"messages"`
		Result     json.RawMessage `json:"result"`
		HasMore    *bool           `json:"hasMore"`
		NextCursor json.RawMessage `json:"nextCursor"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return multicaPage{}, errors.New("decode DWS conversation history response")
	}
	messages := payload.Messages
	if len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			Messages   []listMessage   `json:"messages"`
			HasMore    *bool           `json:"hasMore"`
			NextCursor json.RawMessage `json:"nextCursor"`
		}
		if json.Unmarshal(payload.Result, &nested) == nil {
			if len(messages) == 0 {
				messages = nested.Messages
			}
			if payload.HasMore == nil {
				payload.HasMore = nested.HasMore
				payload.NextCursor = nested.NextCursor
			}
		}
	}
	if !payload.Success && len(messages) == 0 {
		return multicaPage{}, errors.New("rejected " + payload.ErrorCode)
	}
	page := multicaPage{paginationKnown: payload.HasMore != nil}
	if payload.HasMore != nil {
		page.hasMore = *payload.HasMore
	}
	if len(payload.NextCursor) > 0 {
		var milliseconds json.Number
		if json.Unmarshal(payload.NextCursor, &milliseconds) == nil {
			if value, err := milliseconds.Int64(); err == nil && value > 0 {
				page.nextCursor = time.UnixMilli(value).UTC()
			}
		}
	}
	for _, message := range messages {
		page.senderIDs = append(page.senderIDs, message.SenderID)
		if strings.TrimSpace(message.Content) != "" || strings.TrimSpace(message.Text) != "" {
			page.events++
		}
	}
	return page, nil
}

// ---- expectations ----

func mustCheck(t *testing.T, server, tool, payload string) []byte {
	t.Helper()
	out, fail := CheckPayload(server, tool, []byte(payload))
	if fail != nil {
		t.Fatalf("CheckPayload(%s) = %v", payload, fail)
	}
	return out
}

func TestMessageListOutputFeedsMulticaHistoryParsers(t *testing.T) {
	// Raw shape measured on 预发 (contracts §1.3).
	raw := `{"messages":[{"openMessageId":"msgA","openConversationId":"cidX","senderOpenDingTalkId":"` + testDID + `","sender":"张三",` +
		`"content":"hello","createTime":"2026-09-30 11:58:03","quotedMessage":{"openMessageId":"msgQ","content":"原文"}}],` +
		`"hasMore":true,"nextCursor":1790000000000}`
	out := MessageListOutput(mustCheck(t, "chat", "list_conversation_message_v2", raw), "older")

	var top map[string]any
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if top["contractVersion"] != "im.message-list.v1" || top["success"] != true || top["hasMore"] != true || top["nextCursor"] == nil {
		t.Fatalf("envelope = %s", out)
	}
	lines, err := multicaParseDWSHistory(out)
	if err != nil || len(lines) != 1 {
		t.Fatalf("parseDWSHistory = %v %v", lines, err)
	}
	wantAt := time.Date(2026, 9, 30, 11, 58, 3, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	if !lines[0].at.Equal(wantAt) || lines[0].senderID != testDID || lines[0].replyTo != "msgQ" || lines[0].content != "hello" {
		t.Fatalf("history line = %+v", lines[0])
	}
	page, err := multicaParseDWSPage(out)
	if err != nil || !page.paginationKnown || !page.hasMore || page.nextCursor.UnixMilli() != 1790000000000 || page.events != 1 {
		t.Fatalf("parseDWSPage = %+v %v", page, err)
	}
	if page.senderIDs[0] != testDID {
		t.Fatalf("senderId = %q, want senderOpenDingTalkId", page.senderIDs[0])
	}

	// No pagination facts: hasMore is still present (false), so scene memory
	// sees pagination as known, as with real dws.
	out = MessageListOutput(mustCheck(t, "chat", "list_conversation_message_v2", `{"messages":[]}`), "newer")
	page, err = multicaParseDWSPage(out)
	if err != nil || !page.paginationKnown || page.hasMore {
		t.Fatalf("parseDWSPage(no pagination) = %+v %v", page, err)
	}

	// A plain-text tool answer is projected from null: no success key, so both
	// parsers reject it like dws's output.
	out = MessageListOutput(mustCheck(t, "chat", "list_conversation_message_v2", `"busy"`), "older")
	if _, err := multicaParseDWSHistory(out); err == nil {
		t.Fatal("parseDWSHistory accepted a null projection")
	}
	if _, err := multicaParseDWSPage(out); err == nil {
		t.Fatal("parseDWSPage accepted a null projection")
	}

	// An encrypted message keeps its ciphertext in content.
	const cipher = "SwzNkAraDE6lUHUNlVT3mjFdbxL6dWvmt77XtjACdpJx9VFibzTbW9KtDbkzGOYP||2||1||1"
	out = MessageListOutput(mustCheck(t, "chat", "list_conversation_message_v2",
		`{"messages":[{"openMessageId":"e1","openConversationId":"c","content":"`+cipher+`","createTime":"2026-09-30 11:00:00"}],"hasMore":false}`), "older")
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	messages := top["messages"].([]any)
	first := messages[0].(map[string]any)
	if first["content"] != cipher || first["text"] != "[加密消息，无法解码]" || top["partial"] != true || top["decryptFailedCount"] != float64(1) {
		t.Fatalf("encrypted projection = %s", out)
	}
}

func TestListFailureFeedsMulticaHistoryError(t *testing.T) {
	_, fail := CheckPayload("chat", "list_conversation_message_v2",
		[]byte(`{"success":false,"errorCode":"CrossOrgPermissionDenied","errorMsg":"denied","traceId":"trace-1"}`))
	fields := multicaHistoryCLIError(fail.LegacyJSON())
	want := map[string]string{"category": "api", "reason": "business_error", "server_error_code": "CrossOrgPermissionDenied", "trace_id": "trace-1"}
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("historyCLIError = %v", fields)
		}
	}
	_, fail = CheckPayload("chat", "list_conversation_message_v2", []byte(`{"success":false,"errorCode":"130003","errorMsg":"OpenId is not in conversation"}`))
	if multicaHistoryCLIError(fail.LegacyJSON())["server_error_code"] != "130003" {
		t.Fatal("NotInConversation code lost")
	}
}

func TestCrossOrgOutputFeedsMultica(t *testing.T) {
	out := LegacyOutput(mustCheck(t, "im", "chat_permission_grant", `{"result":{"scope":"chat.data:cross-org","grantType":"timed","expireAt":4102444800000}}`))
	if err := multicaConfirmCrossOrgGrant(out); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !bytes.HasSuffix(out, []byte("}\n")) {
		t.Fatalf("output must end with a newline: %q", out)
	}
}

func TestSendOutputsFeedMultica(t *testing.T) {
	out := SendOutput(mustCheck(t, "chat", "send_personal_message", `{"success":true,"result":{"openTaskId":"T1"}}`))
	if task, err := multicaParseSendResult(out); err != nil || task != "T1" {
		t.Fatalf("ParseSendResult = %q %v\n%s", task, err, out)
	}
	if !strings.Contains(string(out), `"outcome": "pending"`) || !strings.Contains(string(out), `"next_command": "dws chat message query-send-status --open-task-id T1"`) {
		t.Fatalf("pending envelope = %s", out)
	}
	out = SendOutput(mustCheck(t, "chat", "send_personal_message", `{"result":{"accepted":true}}`))
	if _, err := multicaParseSendResult(out); err == nil || !strings.Contains(string(out), `"outcome": "success"`) {
		t.Fatalf("success without task = %s", out)
	}

	// Plain-send failures are unified: Multica finds only trace_id, so it
	// cannot recognise a duplicate (the dws asymmetry, contracts §3A.4).
	_, fail := CheckPayload("chat", "send_personal_message",
		[]byte(`{"success":false,"errorCode":"1001","errorMsg":"x error: Request is repeated with uuid 'K'.","traceId":"t-dup"}`))
	unified := multicaMessageCLIError(fail.UnifiedJSON())
	if unified == nil || unified.duplicateRequest || unified.fields["trace_id"] != "t-dup" || unified.fields["category"] != "" {
		t.Fatalf("unified send failure = %+v", unified)
	}
	if _, err := multicaDecodeSendEnvelope(fail.UnifiedJSON()); err == nil {
		t.Fatal("ok:false must be rejected")
	}
	// The same failure from a quote reply is legacy: the duplicate is visible.
	legacy := multicaMessageCLIError(fail.LegacyJSON())
	if legacy == nil || !legacy.duplicateRequest {
		t.Fatalf("legacy reply failure = %+v", legacy)
	}
}

func TestReplyOutputFeedsMultica(t *testing.T) {
	f := ReplyFlags{Content: "收到", IdempotencyKey: "K", ConversationID: "cid", MessageID: "mid"}
	out := ReplyOutput(mustCheck(t, "chat", "send_personal_message", `{"success":true,"result":{"openTaskId":"RT"}}`), f, "cid", testDID)
	if task, err := multicaParseSendResult(out); err != nil || task != "RT" {
		t.Fatalf("ParseSendResult = %q %v\n%s", task, err, out)
	}
	var enriched map[string]any
	_ = json.Unmarshal(out, &enriched)
	if enriched["contractVersion"] != "im.message-reply.v1" || enriched["deliveryStatusKnown"] != false || enriched["idempotencyKey"] != "K" {
		t.Fatalf("enrichment = %s", out)
	}
}

func TestSendStatusOutputFeedsMultica(t *testing.T) {
	out := SendStatusOutput(mustCheck(t, "im", "query_message_send_status",
		`{"success":true,"result":{"sendStatus":"SUCCESS","openConversationId":"cid","openMessageId":"mid"}}`), "T1")
	if state, err := multicaParseSendStatus(out); err != nil || state != "delivered" {
		t.Fatalf("ParseSendStatus = %q %v\n%s", state, err, out)
	}
	// dws does not rename status to sendStatus: Multica keeps provider_accepted.
	out = SendStatusOutput(mustCheck(t, "im", "query_message_send_status",
		`{"result":{"taskId":"task-1","openMessageId":"msg-1","openConversationId":"cid-1","status":"SUCCESS"}}`), "task-1")
	if state, err := multicaParseSendStatus(out); err != nil || state != "provider_accepted" {
		t.Fatalf("ParseSendStatus(status) = %q %v", state, err)
	}
	var top map[string]any
	_ = json.Unmarshal(out, &top)
	if _, lifted := top["sendStatus"]; lifted || top["openTaskId"] != "task-1" || top["readyForMessageActions"] != true {
		t.Fatalf("projection = %s", out)
	}
	out = SendStatusOutput(mustCheck(t, "im", "query_message_send_status", `{"success":true,"result":{"sendStatus":"FAILED"}}`), "T2")
	if state, _ := multicaParseSendStatus(out); state != "failed" {
		t.Fatalf("failed status = %q", state)
	}
}

func TestMessagesByIDsOutputFeedsMultica(t *testing.T) {
	raw := `{"success":true,"result":{"messages":[{"openMessageId":"mid","openConversationId":"cid","senderOpenDingTalkId":"` + testDID + `"}]}}`
	out := MessagesByIDsOutput(mustCheck(t, "im", "list_messages_by_ids", raw))
	if sender, err := multicaParseMessageSender(out, "cid", "mid"); err != nil || sender != testDID {
		t.Fatalf("parseMessageSender = %q %v\n%s", sender, err, out)
	}
	// A raw staff senderId survives the projection and fails verification, as with dws.
	raw = `{"success":true,"result":{"messages":[{"openMessageId":"mid","openConversationId":"cid","senderOpenDingTalkId":"` + testDID + `","senderId":"staff-9"}]}}`
	if _, err := multicaParseMessageSender(MessagesByIDsOutput(mustCheck(t, "im", "list_messages_by_ids", raw)), "cid", "mid"); err == nil {
		t.Fatal("raw senderId accepted")
	}
}

func TestA2UIOutputsFeedMultica(t *testing.T) {
	out := A2UISendOutput(mustCheck(t, "im", "create_and_send_a2ui_card", `{"success":true,"result":{"bizId":"B1","cardInstanceId":42}}`))
	if biz, card, err := multicaParseA2UIReceipt(out); err != nil || biz != "B1" || card != 42 {
		t.Fatalf("parseA2UIReceipt = %q %d %v\n%s", biz, card, err, out)
	}
	if !strings.Contains(string(out), `"cardInstanceId": 42`) {
		t.Fatalf("cardInstanceId must stay a JSON number: %s", out)
	}
	// Above 2^63 the float64 re-encoding (as in dws) no longer fits int64.
	out = A2UISendOutput(mustCheck(t, "im", "create_and_send_a2ui_card", `{"result":{"bizId":"B1","cardInstanceId":12345678901234567891}}`))
	if _, _, err := multicaParseA2UIReceipt(out); err == nil {
		t.Fatalf("oversized cardInstanceId accepted: %s", out)
	}
	_, fail := CheckPayload("im", "create_and_send_a2ui_card",
		[]byte(`{"success":false,"errorCode":"A2UI_TARGET_INVALID","errorMsg":"target invalid","traceId":"trace123"}`))
	if rejection := multicaMessageCLIError(fail.LegacyJSON()); rejection == nil || !rejection.cardSendRejection() || rejection.fields["trace_id"] != "trace123" {
		t.Fatalf("card rejection = %+v", rejection)
	}
	if !multicaUpdateA2UIConfirmed(LegacyOutput(mustCheck(t, "im", "update_a2ui_card", `{"result":{"updated":true}}`))) {
		t.Fatal("update success not confirmed")
	}
	if multicaUpdateA2UIConfirmed(LegacyOutput(mustCheck(t, "im", "update_a2ui_card", `"ok"`))) {
		t.Fatal("null update output confirmed")
	}
}
