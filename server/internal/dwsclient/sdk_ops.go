package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat"
)

// Each SDK operation builds the arguments dws would send (dwscli), calls the
// tool, applies dws's success rules and renders what dws prints, so the
// parsers below the CLI path read the same bytes either way.

// sdkTool calls one tool as dws would. It returns the payload dws accepts
// or dws's failure; err is a cancellation, kept as such for the callers'
// timeout handling.
func sdkTool(ctx context.Context, client *dws.Client, server dws.Server, tool string, args map[string]any) ([]byte, *clicompat.Failure, error) {
	if failure := clicompat.ValidateStrings(args); failure != nil {
		return nil, failure, nil
	}
	raw, err := client.CallRaw(ctx, server, tool, args)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, clicompat.CallFailure(err), nil
	}
	payload, failure := clicompat.CheckPayload(string(server), tool, raw)
	return payload, failure, nil
}

// sdkMessageError is messageCommand's error for a failed dws command: the
// structured diagnostics of the JSON dws prints (legacy on stderr, unified
// on stdout), never its text.
func sdkMessageError(failure *clicompat.Failure, unified bool) error {
	printed := failure.LegacyJSON()
	if unified {
		printed = failure.UnifiedJSON()
	}
	if detail := messageCLIError(printed); detail != nil {
		return detail
	}
	return errors.New("DWS message operation failed")
}

func sdkBounded(out []byte) ([]byte, error) {
	if len(out) > MaxResponseBytes {
		return nil, errors.New("DWS message response is too large")
	}
	return out, nil
}

// listSDK is List through the SDK (`chat message list`).
func listSDK(ctx context.Context, client *dws.Client, req ListRequest, queryTime string) ([]byte, error) {
	payload, failure, err := sdkTool(ctx, client, dws.ServerChat, "list_conversation_message_v2",
		clicompat.MessageListArgs(req.ConversationID, queryTime, req.Direction, req.Limit))
	if err != nil {
		return nil, commandFailed(ctx, "DWS conversation history query failed", err)
	}
	if failure != nil {
		if detail := historyCLIError(failure.LegacyJSON()); detail != nil {
			return nil, detail
		}
		return nil, errors.New("DWS conversation history query failed")
	}
	out := clicompat.MessageListOutput(payload, req.Direction)
	if ctx.Err() != nil {
		// The CLI would have been killed at the deadline; keep it a timeout.
		return nil, commandFailed(ctx, "DWS conversation history query failed", ctx.Err())
	}
	if len(out) > MaxResponseBytes {
		return nil, errors.New("DWS conversation history response is too large")
	}
	return out, nil
}

// renewCrossOrgReadSDK is RenewCrossOrgRead through the SDK (`chat
// data-auth cross-org`).
func renewCrossOrgReadSDK(ctx context.Context, client *dws.Client) ([]byte, error) {
	payload, failure, err := sdkTool(ctx, client, dws.ServerIM, "chat_permission_grant", clicompat.CrossOrgArgs())
	if err != nil {
		return nil, commandFailed(ctx, "DWS cross-org chat read renewal failed", err)
	}
	if failure != nil {
		return nil, errors.New("DWS cross-org chat read renewal failed")
	}
	return clicompat.LegacyOutput(payload), nil
}

// sendSDK is the send command Send would run: `chat +messages-reply` for a
// quote reply, else `chat message send`.
func sendSDK(ctx context.Context, client *dws.Client, req SendRequest) ([]byte, error) {
	group := strings.TrimSpace(req.ConversationID)
	if replyTo := strings.TrimSpace(req.ReplyToOpenMsgID); replyTo != "" {
		flags := clicompat.ReplyFlags{Content: req.Content, IdempotencyKey: req.IdempotencyKey, AITag: req.ShowAITag,
			ConversationID: group, MessageID: replyTo}
		if failure := clicompat.ValidateReply(flags); failure != nil {
			return nil, sdkMessageError(failure, false)
		}
		lookup, failure, err := sdkTool(ctx, client, dws.ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": []string{flags.MessageID}})
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return nil, sdkMessageError(failure, false)
		}
		conversation, sender, failure := clicompat.ReplySource(lookup, flags)
		if failure != nil {
			return nil, sdkMessageError(failure, false)
		}
		payload, failure, err := sdkTool(ctx, client, dws.ServerChat, "send_personal_message", clicompat.ReplyArgs(flags, conversation, sender))
		if err != nil {
			return nil, err
		}
		if failure != nil {
			return nil, sdkMessageError(failure, false)
		}
		return sdkBounded(clicompat.ReplyOutput(payload, flags, conversation, sender))
	}
	flags := clicompat.SendFlags{Content: req.Content, Title: req.Title, IdempotencyKey: req.IdempotencyKey, AITag: req.ShowAITag}
	if group != "" {
		flags.ConversationID, flags.AtOpenDingTalkIDs = group, req.AtOpenDingTalkID
	} else {
		flags.OpenDingTalkID = strings.TrimSpace(req.RecipientOpenDingTalkID)
	}
	// `chat message send` reports through the unified envelope, success and
	// failure alike.
	args, failure := clicompat.SendArgs(flags)
	if failure != nil {
		return nil, sdkMessageError(failure, true)
	}
	payload, failure, err := sdkTool(ctx, client, dws.ServerChat, "send_personal_message", args)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, sdkMessageError(failure, true)
	}
	return sdkBounded(clicompat.SendOutput(payload))
}

// querySendStatusSDK is `chat message query-send-status`.
func querySendStatusSDK(ctx context.Context, client *dws.Client, openTaskID string) ([]byte, error) {
	payload, failure, err := sdkTool(ctx, client, dws.ServerIM, "query_message_send_status", map[string]any{"openTaskId": openTaskID})
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, sdkMessageError(failure, false)
	}
	return sdkBounded(clicompat.SendStatusOutput(payload, openTaskID))
}

// messagesByIDsSDK is `chat message list-by-ids`.
func messagesByIDsSDK(ctx context.Context, client *dws.Client, messageID string) ([]byte, error) {
	payload, failure, err := sdkTool(ctx, client, dws.ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": []string{messageID}})
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, sdkMessageError(failure, false)
	}
	return sdkBounded(clicompat.MessagesByIDsOutput(payload))
}

// sendA2UISDK is `chat +messages-send --msg-type a2ui`.
func sendA2UISDK(ctx context.Context, client *dws.Client, in A2UISendRequest) ([]byte, error) {
	args, failure := clicompat.A2UISendArgs(clicompat.A2UISendFlags{ChatID: in.ConversationID, OpenDingTalkID: in.ReceiverOpenDingTalkID,
		BizCardID: in.BizID, RequestID: in.RequestID, Summary: in.Summary, Messages: in.Messages})
	if failure != nil {
		return nil, sdkMessageError(failure, false)
	}
	payload, failure, err := sdkTool(ctx, client, dws.ServerIM, "create_and_send_a2ui_card", args)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, sdkMessageError(failure, false)
	}
	raw, err := sdkBounded(clicompat.A2UISendOutput(payload))
	if err != nil {
		return nil, err
	}
	receipt, receiptErr := parseA2UIReceipt(raw)
	if receiptErr != nil || receipt.MessageID != "" || receipt.TaskID == "" {
		return raw, nil
	}
	// Send-status tasks belong to the token that created them. Query while
	// this client's original credential is alive, never through another
	// exchanged directory. Failure here cannot revoke a confirmed card send.
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	statusRaw, statusErr := querySendStatusSDK(statusCtx, client, receipt.TaskID)
	if statusErr != nil {
		return raw, nil
	}
	status, statusErr := ParseSendStatus(statusRaw)
	if statusErr != nil || status.State != "delivered" ||
		(in.ConversationID != "" && status.OpenConversationID != in.ConversationID) ||
		(receipt.ConversationID != "" && status.OpenConversationID != receipt.ConversationID) {
		return raw, nil
	}
	receipt.MessageID = status.OpenMessageID
	receipt.ConversationID = status.OpenConversationID
	enriched, marshalErr := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"success": true, "result": receipt,
	}})
	if marshalErr != nil {
		return raw, nil
	}
	return sdkBounded(enriched)
}

// updateA2UISDK is `chat message update-a2ui-card`.
func updateA2UISDK(ctx context.Context, client *dws.Client, bizID, status string, messages []string, annotations []A2UIAnnotation) ([]byte, error) {
	annotationJSON, _ := json.Marshal(annotations)
	args, failure := clicompat.UpdateA2UIArgs(bizID, status, messages, annotationJSON)
	if failure != nil {
		return nil, sdkMessageError(failure, false)
	}
	payload, failure, err := sdkTool(ctx, client, dws.ServerIM, "update_a2ui_card", args)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, sdkMessageError(failure, false)
	}
	return sdkBounded(clicompat.LegacyOutput(payload))
}
