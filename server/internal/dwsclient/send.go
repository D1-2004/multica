package dwsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// SendRequest contains only trusted routing fields and a platform idempotency key.
// Markdown is the personal-message endpoint's default content type.
type SendRequest struct {
	SourceOpenMessageID     string
	SourceConversationID    string
	ConversationID          string
	RecipientOpenDingTalkID string
	AtOpenDingTalkID        string
	Content                 string
	Title                   string
	IdempotencyKey          string
	ShowAITag               bool
	ReplyToOpenMsgID        string
}

type SendResult struct {
	OpenTaskID         string
	OpenConversationID string
	OpenMessageID      string
	A2UIReceipt        *A2UIReceipt
}

type SendStatus struct {
	State              string
	OpenConversationID string
	OpenMessageID      string
	ErrorCode          string
}

// SendRejectedError means the provider explicitly rejected the request. Other
// send errors are ambiguous and must never trigger an automatic resend.
type SendRejectedError struct{ Code string }

func (e *SendRejectedError) Error() string { return "DWS message send rejected: " + e.Code }

// MessageOperationError preserves only allowlisted provider identifiers. CLI
// stderr may contain credentials, message bodies, and suggested commands.
type MessageOperationError struct {
	diagnostics      *HistoryError
	DuplicateRequest bool
	providerMessage  string
}

func (e *MessageOperationError) Error() string {
	return diagnosticErrorSummary("DWS message operation failed", e.diagnostics.fields)
}

func (e *MessageOperationError) DiagnosticFields() map[string]any {
	return e.diagnostics.DiagnosticFields()
}

func messageCLIError(raw []byte) *MessageOperationError {
	if len(raw) > MaxResponseBytes {
		return nil
	}
	diagnostics := historyCLIError(raw)
	if diagnostics == nil {
		// The packaged CLI may print a startup notice before its JSON error.
		// Only accept a complete trailing envelope; never log the notice itself.
		if offset := bytes.LastIndex(raw, []byte("\n{")); offset >= 0 {
			diagnostics = historyCLIError(raw[offset+1:])
		}
	}
	if diagnostics == nil {
		return nil
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	candidate := raw
	if offset := bytes.LastIndex(raw, []byte("\n{")); offset >= 0 {
		candidate = raw[offset+1:]
	}
	_ = json.Unmarshal(candidate, &envelope)
	duplicate := diagnostics.fields["server_error_code"] == "1001" && strings.Contains(envelope.Error.Message, "Request is repeated with uuid '")
	return &MessageOperationError{diagnostics: diagnostics, DuplicateRequest: duplicate, providerMessage: SafeMessage(redact.Text(envelope.Error.Message), 1000)}
}

func (c CLI) Send(ctx context.Context, configDir string, req SendRequest) (SendResult, error) {
	if _, err := sendArgs(req); err != nil {
		return SendResult{}, err
	}
	if req.SourceOpenMessageID != "" {
		if req.ConversationID != "" && req.ConversationID != req.SourceConversationID {
			return SendResult{}, errors.New("DWS reply source conversation mismatch")
		}
		sender, err := c.ResolveMessageSender(ctx, configDir, req.SourceConversationID, req.SourceOpenMessageID)
		if err != nil {
			return SendResult{}, err
		}
		if req.AtOpenDingTalkID != "" {
			req.Content = strings.Replace(req.Content, "<@"+req.AtOpenDingTalkID+">", "<@"+sender+">", 1)
			req.AtOpenDingTalkID = sender
		}
		if req.RecipientOpenDingTalkID != "" {
			req.RecipientOpenDingTalkID = sender
		}
	}
	if strings.TrimSpace(req.ReplyToOpenMsgID) != "" && req.AtOpenDingTalkID != "" {
		// A quote reply carries the platform's own @ of the quoted sender and
		// takes no at list. Drop the addressing placeholder so the reply does
		// not mention the same person twice. A message that is nothing but
		// that mention keeps it, because an empty reply cannot be sent.
		if stripped := StripLeadingMention(req.Content, req.AtOpenDingTalkID); strings.TrimSpace(stripped) != "" {
			req.Content = stripped
		}
		req.AtOpenDingTalkID = ""
	}
	args, err := sendArgs(req)
	if err != nil {
		return SendResult{}, err
	}
	raw, err := c.messageOp(ctx, configDir, args, func(client *dws.Client) ([]byte, error) { return sendSDK(ctx, client, req) })
	if err != nil {
		return SendResult{}, err
	}
	return ParseSendResult(raw)
}

func sendArgs(req SendRequest) ([]string, error) {
	group := strings.TrimSpace(req.ConversationID)
	person := strings.TrimSpace(req.RecipientOpenDingTalkID)
	replyTo := strings.TrimSpace(req.ReplyToOpenMsgID)
	if strings.TrimSpace(req.Content) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, errors.New("DWS send requires content and an idempotency key")
	}
	if replyTo != "" {
		if group == "" {
			return nil, errors.New("DWS quote-reply requires a conversation id")
		}
		return []string{"chat", "+messages-reply", "--content", req.Content,
			"--idempotency-key", req.IdempotencyKey, "--ai-tag=" + strconv.FormatBool(req.ShowAITag),
			"--format", "json", "--yes", "--group", group, "--message-id", replyTo}, nil
	}
	if (group == "") == (person == "") {
		return nil, errors.New("DWS send requires one target, content and an idempotency key")
	}
	args := []string{"chat", "message", "send", "--content", req.Content,
		"--idempotency-key", req.IdempotencyKey, "--ai-tag=" + strconv.FormatBool(req.ShowAITag), "--format", "json"}
	if req.Title != "" {
		args = append(args, "--title", req.Title)
	}
	if group != "" {
		args = append(args, "--conversation-id", group)
		if req.AtOpenDingTalkID != "" {
			args = append(args, "--at-open-dingtalk-ids", req.AtOpenDingTalkID)
		}
	} else {
		args = append(args, "--open-dingtalk-id", person)
	}
	return args, nil
}

func (c CLI) QuerySendStatus(ctx context.Context, configDir, openTaskID string) (SendStatus, error) {
	if strings.TrimSpace(openTaskID) == "" {
		return SendStatus{}, errors.New("DWS send task id is required")
	}
	raw, err := c.messageOp(ctx, configDir, []string{"chat", "message", "query-send-status", "--open-task-id", openTaskID, "--format", "json"},
		func(client *dws.Client) ([]byte, error) { return querySendStatusSDK(ctx, client, openTaskID) })
	if err != nil {
		return SendStatus{}, err
	}
	return ParseSendStatus(raw)
}

// messageOp runs a message command: through dir's SDK session when Exchange
// made one, else the dws CLI with args.
func (c CLI) messageOp(ctx context.Context, configDir string, args []string, viaSDK func(*dws.Client) ([]byte, error)) ([]byte, error) {
	client, _, ok, err := sdkClient(configDir)
	if !ok {
		return c.messageCommand(ctx, configDir, args)
	}
	if err != nil {
		return nil, err
	}
	return viaSDK(client)
}

func (c CLI) messageCommand(ctx context.Context, configDir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.path(), args...)
	cmd.Env = c.commandEnv(configDir, nil)
	var stdout limitedOutput
	cmd.Stdout = &stdout
	// Keep both streams bounded; expose only allowlisted structured diagnostics.
	var stderr limitedOutput
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.overflow {
		return nil, errors.New("DWS message response is too large")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if detail := messageCLIError(stdout.Bytes()); detail != nil {
			return nil, detail
		}
		if !stderr.overflow {
			if detail := messageCLIError(stderr.Bytes()); detail != nil {
				return nil, detail
			}
		}
	}
	if err != nil && !looksLikeJSONObject(stdout.Bytes()) {
		return nil, commandFailed(ctx, "DWS message operation failed", err)
	}
	return stdout.Bytes(), nil
}

type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := MaxResponseBytes - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

type sendEnvelope struct {
	Success            *bool           `json:"success"`
	ErrorCode          string          `json:"errorCode"`
	Result             json.RawMessage `json:"result"`
	OpenTaskID         string          `json:"openTaskId"`
	OpenMessageID      string          `json:"openMessageId"`
	OpenConversationID string          `json:"openConversationId"`
	SendStatus         json.RawMessage `json:"sendStatus"`
}

func decodeSendEnvelope(raw []byte) (sendEnvelope, error) {
	var wrapper struct {
		OK   *bool           `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &wrapper) == nil && wrapper.OK != nil {
		if !*wrapper.OK {
			return sendEnvelope{}, errors.New("DWS message command rejected")
		}
		if len(wrapper.Data) > 0 {
			return decodeSendEnvelope(wrapper.Data)
		}
	}

	var outer sendEnvelope
	if err := json.Unmarshal(raw, &outer); err != nil {
		return outer, errors.New("decode DWS message response")
	}
	if outer.Success == nil {
		return outer, errors.New("DWS message response omitted success")
	}
	if !*outer.Success {
		return outer, &SendRejectedError{Code: SafeCode(outer.ErrorCode)}
	}
	if len(outer.Result) > 0 && string(outer.Result) != "null" {
		var nested sendEnvelope
		if err := json.Unmarshal(outer.Result, &nested); err != nil {
			return outer, errors.New("decode DWS message result")
		}
		if nested.Success != nil && !*nested.Success {
			return nested, &SendRejectedError{Code: SafeCode(nested.ErrorCode)}
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

func ParseSendResult(raw []byte) (SendResult, error) {
	payload, err := decodeSendEnvelope(raw)
	if err != nil {
		return SendResult{}, err
	}
	if strings.TrimSpace(payload.OpenTaskID) == "" {
		return SendResult{}, errors.New("DWS send response omitted openTaskId")
	}
	return SendResult{OpenTaskID: payload.OpenTaskID}, nil
}

func ParseSendStatus(raw []byte) (SendStatus, error) {
	payload, err := decodeSendEnvelope(raw)
	if err != nil {
		return SendStatus{}, err
	}
	status := SendStatus{State: "provider_accepted"}
	var state string
	// Numeric/unknown status codes must not be guessed. The query's success
	// envelope alone acknowledges the lookup, not the underlying delivery.
	_ = json.Unmarshal(payload.SendStatus, &state)
	switch strings.ToUpper(state) {
	case "FAIL", "FAILED", "FAILURE", "CANCELLED", "CANCELED":
		status.State, status.ErrorCode = "failed", "provider_send_failed"
		return status, nil
	case "PENDING", "PROCESSING", "SENDING", "INIT", "QUEUED":
		return status, nil
	case "SUCCESS", "SUCCEEDED", "DELIVERED", "SEND_SUCCESS":
		if strings.TrimSpace(payload.OpenMessageID) != "" && strings.TrimSpace(payload.OpenConversationID) != "" {
			status.State = "delivered"
			status.OpenMessageID = payload.OpenMessageID
			status.OpenConversationID = payload.OpenConversationID
		}
	}
	return status, nil
}

// CardSendRejection returns only definite pre-delivery failures. Transport and
// downstream delivery failures remain ambiguous and must not authorize a resend.
func (e *MessageOperationError) CardSendRejection() (string, bool) {
	if !e.diagnostics.BusinessError() {
		return "", false
	}
	switch e.diagnostics.ServerErrorCode() {
	case "A2UI_TARGET_INVALID", "INVALID_PARAM", "FORBIDDEN", "PERMISSION_DENIED":
	default:
		return "", false
	}
	text := "选择卡片发送失败，本次未执行。\nDWS 错误：" + e.diagnostics.ServerErrorCode()
	if e.providerMessage != "" {
		text += "\nDWS 提示：" + e.providerMessage
	}
	if trace := e.diagnostics.field("trace_id"); trace != "" {
		text += "\nTrace ID：" + trace
	}
	return text, true
}
