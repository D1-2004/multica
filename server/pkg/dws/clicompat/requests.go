package clicompat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/pkg/dws/clicompat/chatmsg"
	apperrors "github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/errors"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/shortcut/targetresolver"
)

// Request builders. The dws logic here lives inline in cobra RunE and
// shortcut Validate/Execute functions, which cannot be vendored; their
// statements are copied verbatim with flag reads replaced by the flag
// struct's fields and the final tool call replaced by returning the
// arguments. The helpers they call are dws's (dws_helpers.go, dws_chat.go,
// the vendored targetresolver and chatmsg).

// openClawType is edition.ClawType() of the open-source edition
// (pkg/edition/edition.go:216, default.go:21): "openClaw". DWS_AGENT_PRODUCT
// is not honoured here.
const openClawType = "openClaw"

// ---- chat message list (contracts §1.1) ----

// MessageListArgs returns the chat/list_conversation_message_v2 arguments of
// `chat message list --group CID --time T --direction D --limit N`
// (internal/helpers/chat.go:3844-3904). An empty time is dws's default (now,
// Shanghai) with forward=false. dws rejects a direction other than
// older/newer/""; this function treats it as older. Check the result with
// ValidateStrings before sending.
func MessageListArgs(conversationID, queryTime, direction string, limit int) map[string]any {
	// chatFlagOrAlias trims the group (internal/helpers/chat.go:737).
	groupID := strings.TrimSpace(conversationID)
	// internal/helpers/chat.go:3881-3904.
	timeVal := queryTime
	defaultForward := true
	if timeVal == "" {
		timeVal = defaultChatMessageListTime()
		defaultForward = false
	}
	forward := directionForward(direction, defaultForward)
	toolArgs := map[string]any{
		"openconversation_id": groupID,
		"time":                timeVal,
		"forward":             forward,
	}
	if v := limit; v > 0 {
		toolArgs["limit"] = v
	}
	return toolArgs
}

// directionForward is resolveMessageForward (internal/helpers/chat.go:641)
// for --direction alone (Multica never passes --forward); dws's error for an
// unknown direction becomes forward=false.
func directionForward(direction string, defaultForward bool) bool {
	switch strings.TrimSpace(strings.ToLower(direction)) {
	case "newer":
		return true
	case "older":
		return false
	case "":
		return defaultForward
	default:
		return false
	}
}

// ---- chat data-auth cross-org (contracts §2.1) ----

// CrossOrgArgs returns the im/chat_permission_grant arguments of
// `chat data-auth cross-org --all --agentCode wukong --grant-type timed --ttl 7d`:
// buildChatGrantBaseArgs and buildChatCrossOrgDataAuthArgs
// (internal/helpers/chat.go:2705-2762) with those flag values.
func CrossOrgArgs() map[string]any {
	targetOrgID := "*" // --all
	grantType, ttl, sessionID := "timed", "7d", ""
	toolArgs := map[string]any{
		"agentCode": "wukong",
		"scope":     "chat.data:cross-org",
		"grantType": grantType,
	}
	if grantType == "timed" {
		toolArgs["ttl"] = ttl
	}
	if sessionID != "" {
		toolArgs["sessionId"] = sessionID
	}
	toolArgs["grantCategory"] = "data"
	paramsJSON, _ := marshalJSONRaw(map[string]string{"targetOrgId": targetOrgID})
	toolArgs["grantParams"] = string(paramsJSON)
	return toolArgs
}

// ---- chat message send (contracts §3A) ----

// SendFlags are the `chat message send` flags Multica passes.
type SendFlags struct {
	Content, Title, IdempotencyKey string
	AITag                          bool
	ConversationID                 string // --conversation-id
	AtOpenDingTalkIDs              string // --at-open-dingtalk-ids, raw comma list
	OpenDingTalkID                 string // --open-dingtalk-id
}

// SendArgs returns the chat/send_personal_message arguments of a text
// `chat message send`, or the failure dws reports before sending (render it
// with UnifiedJSON): the command's RunE (sendRunE) followed by the
// transport's argument check (ValidateStrings).
func SendArgs(f SendFlags) (map[string]any, *Failure) {
	params, err := sendRunE(f)
	if err != nil {
		return nil, newFailure(err)
	}
	if fail := ValidateStrings(params); fail != nil {
		return nil, fail
	}
	return params, nil
}

// sendRunE is the text-message path of `chat message send`'s RunE
// (internal/helpers/chat.go:4097-4323) for --conversation-id or
// --open-dingtalk-id.
func sendRunE(f SendFlags) (map[string]any, error) {
	groupID := strings.TrimSpace(f.ConversationID) // chatFlagOrAlias (chat.go:735)
	userID := ""
	openDingTalkID := f.OpenDingTalkID
	msgUuid := f.IdempotencyKey
	specified := 0
	if groupID != "" {
		specified++
	}
	if userID != "" {
		specified++
	}
	if openDingTalkID != "" {
		specified++
	}
	if specified > 1 {
		return nil, apperrors.NewValidation(
			"--conversation-id, --user and --open-dingtalk-id are mutually exclusive, specify exactly one",
			apperrors.WithReason("mutually_exclusive"),
		)
	}
	if specified == 0 {
		return nil, apperrors.NewValidation(
			"--conversation-id, --user or --open-dingtalk-id is required",
			apperrors.WithReason("require_one_of"),
		)
	}
	if openDingTalkID != "" {
		if err := targetresolver.ValidateExplicitOpenDingTalkID("--open-dingtalk-id", openDingTalkID); err != nil {
			return nil, err
		}
	}
	clawType := ""
	aiTag := f.AITag
	if aiTag {
		clawType = openClawType
	}

	// ── 文本/Markdown 消息 ──
	text := f.Content
	if text == "" {
		return nil, apperrors.NewValidation(
			"message content required (use --content or positional arg, or --media-id for image)",
			apperrors.WithReason("require_one_of"),
		)
	}
	title := f.Title
	if title == "" {
		title = sanitizeTitleFromText(text)
	}
	if groupID != "" {
		atAll := false
		atOpenIdsStr := f.AtOpenDingTalkIDs
		// 群聊统一走 openDingTalkId @ 人接口。
		newParams := map[string]any{
			"openConversationId": groupID,
			"msgType":            "markdown",
			"clawType":           clawType,
		}
		text = applyCurrentUserGroupMentions(newParams, text, atOpenIdsStr, atAll)
		contentJSON, _ := marshalJSONRaw(map[string]string{"title": title, "text": text})
		newParams["content"] = string(contentJSON)
		if msgUuid != "" {
			newParams["uuid"] = msgUuid
		}
		return newParams, nil
	}
	// 单聊：统一走 openDingTalkId
	directContentJSON, _ := marshalJSONRaw(map[string]string{"title": title, "text": text})
	newDirectParams := map[string]any{
		"receiverOpenDingTalkId": openDingTalkID,
		"msgType":                "markdown",
		"content":                string(directContentJSON),
		"clawType":               clawType,
	}
	if msgUuid != "" {
		newDirectParams["uuid"] = msgUuid
	}
	return newDirectParams, nil
}

// ---- chat +messages-reply (contracts §3B) ----

// ReplyFlags are the `chat +messages-reply` flags Multica passes.
type ReplyFlags struct {
	Content, IdempotencyKey string
	AITag                   bool
	ConversationID          string // --group
	MessageID               string // --message-id
}

// ValidateReply is the local validation dws runs before any call: the
// shortcut framework's required-flag and exactly-one checks
// (internal/corecmd/corecmd.go:737-740,1200-1204) and validateReplyExtensions
// (internal/shortcut/chat/reply_extensions.go:17-67). Shortcut flag values are
// trimmed (internal/shortcut/runner.go:53). Failures are validation errors,
// exit 3, rendered with LegacyJSON.
func ValidateReply(f ReplyFlags) *Failure {
	if err := validateReplyFlags(f); err != nil {
		return newFailure(err)
	}
	return nil
}

func validateReplyFlags(f ReplyFlags) error {
	if strings.TrimSpace(f.Content) == "" {
		return apperrors.NewValidation(fmt.Sprintf("必填参数 --%s 不能为空", "content"))
	}
	if strings.TrimSpace(f.MessageID) == "" {
		return apperrors.NewValidation(fmt.Sprintf("请指定 %s 之一", "--ref-msg-id、--message-id"))
	}
	// validateReplyExtensions with --at-open-dingtalk-ids and --at-all unset.
	ids := uniqueShortcutStrings(nil)
	atAll := false
	body := strings.TrimSpace(f.Content)
	if strings.TrimSpace(body) == "" {
		return apperrors.NewValidation("回复正文不能为空")
	}
	declared := map[string]bool{}
	for _, id := range ids {
		declared[id] = true
	}
	for _, id := range currentUserMentionBodyIDs(body) {
		if !declared[id] {
			return apperrors.NewValidation("正文 @成员占位符必须通过 --at-open-dingtalk-ids 声明")
		}
	}
	if containsCurrentUserMentionToken(body, "all") && !atAll {
		return apperrors.NewValidation("正文 <@all> 必须同时指定 --at-all")
	}
	return nil
}

// ReplySource resolves the quoted message from the im/list_messages_by_ids
// payload {"openMsgIds":[MID]} (after CheckPayload): the payload decoded like
// RuntimeContext.callMCPData, then exactChatMessage and resolveReplyTarget
// (internal/shortcut/chat/reply_extensions.go:72-121).
func ReplySource(lookupPayload []byte, f ReplyFlags) (conversationID, senderOpenDingTalkID string, fail *Failure) {
	data, err := decodeCallMCPData("list_messages_by_ids", lookupPayload)
	if err != nil {
		return "", "", newFailure(err)
	}
	message, err := exactChatMessageIn(data, strings.TrimSpace(f.ConversationID), strings.TrimSpace(f.MessageID))
	if err != nil {
		return "", "", newFailure(err)
	}
	// resolveReplyTarget (reply_extensions.go:108-121) without --reply-in-thread and --ref-sender.
	sender := findMessageSenderOpenDingTalkID(message)
	if sender == "" {
		return "", "", newFailure(apperrors.NewValidation("源消息缺少发送者 openDingTalkId，未执行回复"))
	}
	return shortcutString(message, "openConversationId", "openconversationId", "conversationId", "openCid"), sender, nil
}

// exactChatMessageIn is exactChatMessage (reply_extensions.go:72-98) after
// its list_messages_by_ids call.
func exactChatMessageIn(data map[string]any, conversationID, messageID string) (map[string]any, error) {
	var found map[string]any
	for _, message := range shortcutMessageMaps(data) {
		if fmt.Sprint(chatmsg.MessageID(message)) != messageID {
			continue
		}
		cid := shortcutString(message, "openConversationId", "openconversationId", "conversationId", "openCid")
		if cid == "" || (conversationID != "" && cid != conversationID) {
			return nil, apperrors.NewValidation("源消息会话与 --group/--conversation-id 不一致或下游未提供会话身份")
		}
		if found != nil {
			return nil, apperrors.NewValidation("消息 ID 返回重复记录，无法确定唯一源消息")
		}
		found = message
	}
	if found == nil {
		return nil, apperrors.NewValidation("未找到精确匹配的源消息 ID；未执行写入")
	}
	return found, nil
}

// ReplyArgs returns the chat/send_personal_message arguments of the quote
// reply: MessagesReply.Execute (internal/shortcut/chat/lark_alignment.go:249-273)
// with replyMentionBody, AddAIMessageTag and addReplyMentionParams inlined
// for no --at-open-dingtalk-ids and no --at-all. conversationID and the
// sender come from ReplySource. dws checks the arguments at the transport
// after the lookup: run ValidateStrings on the result before sending.
func ReplyArgs(f ReplyFlags, conversationID, senderOpenDingTalkID string) map[string]any {
	refSender := senderOpenDingTalkID
	body := PrepareChatReplyMentions(strings.TrimSpace(f.Content), uniqueShortcutStrings(nil), false, true)
	content, _ := json.Marshal(map[string]string{
		"referenceOpenMessageId":   strings.TrimSpace(f.MessageID),
		"srcMsgSendOpenDingTalkId": refSender,
		"replyMsgType":             "text",
		"content":                  body,
	})
	params := map[string]any{
		"openConversationId": conversationID,
		"msgType":            "reply",
		"content":            string(content),
	}
	// RuntimeContext.AddAIMessageTag (internal/shortcut/runner.go:115).
	if f.AITag {
		params["clawType"] = openClawType
	}
	if value := strings.TrimSpace(f.IdempotencyKey); value != "" {
		params["uuid"] = value
	}
	return params
}

// ---- chat +messages-send --msg-type a2ui (contracts §6A) ----

// A2UISendFlags are the `chat +messages-send --as user --msg-type a2ui` flags.
type A2UISendFlags struct {
	ChatID, OpenDingTalkID, BizCardID, RequestID, Summary string
	Messages                                              []string // --a2ui-messages as a JSON array of strings
}

// A2UISendArgs returns the im/create_and_send_a2ui_card arguments, or the
// failure dws reports before sending (render with LegacyJSON):
// validateMessagesSend, validateSendExtensions and
// executeMessagesSendUserShare (internal/shortcut/chat/unified_send.go:139-238,
// send_extensions.go:19-106) for --as user --msg-type a2ui, followed by the
// transport's argument check. Messages is passed to dws as the JSON array
// Multica puts on the command line.
func A2UISendArgs(f A2UISendFlags) (map[string]any, *Failure) {
	args, err := a2uiSendShortcut(f)
	if err != nil {
		return nil, newFailure(err)
	}
	if fail := ValidateStrings(args); fail != nil {
		return nil, fail
	}
	return args, nil
}

func a2uiSendShortcut(f A2UISendFlags) (map[string]any, error) {
	rawMessages, _ := json.Marshal(f.Messages)
	a2uiMessages := strings.TrimSpace(string(rawMessages))
	// validateMessagesSend (unified_send.go:139-238) for identity user.
	group := strings.TrimSpace(f.ChatID)
	openID := strings.TrimSpace(f.OpenDingTalkID)
	if openID != "" {
		if err := targetresolver.ValidateExplicitOpenDingTalkID("--open-dingtalk-id", openID); err != nil {
			return nil, err
		}
	}
	// validateSendExtensions (send_extensions.go:19-29) for kind a2ui.
	if _, err := ParseChatA2UIMessages(a2uiMessages); err != nil {
		return nil, err
	}
	targetCount := 0
	for _, value := range []string{group, openID} {
		if value != "" {
			targetCount++
		}
	}
	if targetCount != 1 {
		return nil, apperrors.NewValidation("--identity user 时 --group/--chat-id、--chat-query、--user、--user-query、--open-dingtalk-id 必须且只能指定一个")
	}
	// executeMessagesSendUserShare (send_extensions.go:87-106).
	messages, err := ParseChatA2UIMessages(a2uiMessages)
	if err != nil {
		return nil, err
	}
	requestID, cardID := strings.TrimSpace(f.RequestID), strings.TrimSpace(f.BizCardID)
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if cardID == "" {
		cardID = uuid.NewString()
	}
	summary := strings.TrimSpace(f.Summary)
	if summary == "" {
		summary = strings.Join(messages, "\n")
	}
	args := map[string]any{"requestId": requestID, "bizCardId": cardID, "a2uiMessages": messages, "summary": summary, "protocolVersion": "1.0", "flowStatus": "PROCESSING"}
	addMessagesSendUserTarget(args, group, openID)
	return args, nil
}

// ---- chat message update-a2ui-card (contracts §6B) ----

// updateA2UICardUseLine and updateA2UICardExample are the cobra usage line
// and Example of `chat message update-a2ui-card` (internal/helpers/chat.go:7530),
// which dws prints in its missing-flag error.
const (
	updateA2UICardUseLine = "dws chat message update-a2ui-card [flags]"
	updateA2UICardExample = `  dws chat message update-a2ui-card --biz-id <bizId> --content '["{\"version\":\"v1.0\",\"updateDataModel\":{\"surfaceId\":\"surface\",\"path\":\"/status\",\"value\":\"finished\"}}"]' --flow-status FINISH`
)

// UpdateA2UIArgs returns the im/update_a2ui_card arguments of
// `chat message update-a2ui-card`, or the failure dws reports before sending
// (render with LegacyJSON): the command's RunE (internal/helpers/chat.go:7536-7571)
// followed by the transport's argument check. messages is passed to dws as
// the JSON array Multica puts on the command line; annotations empty means
// the flag is absent.
func UpdateA2UIArgs(bizID, flowStatus string, messages []string, annotations json.RawMessage) (map[string]any, *Failure) {
	params, err := updateA2UIRunE(bizID, flowStatus, messages, annotations)
	if err != nil {
		return nil, newFailure(err)
	}
	if fail := ValidateStrings(params); fail != nil {
		return nil, fail
	}
	return params, nil
}

func updateA2UIRunE(bizIDFlag, flowStatusFlag string, messageList []string, annotationsFlag json.RawMessage) (map[string]any, error) {
	contentJSON, _ := json.Marshal(messageList)
	content := string(contentJSON)
	annotationsChanged := len(annotationsFlag) > 0
	// validateRequiredFlags (internal/helpers/helpers.go:40,
	// pkg/cmdutil/flags.go:82-107).
	var flags []string
	for _, flag := range []struct{ name, value string }{
		{"biz-id", bizIDFlag}, {"content", content}, {"flow-status", flowStatusFlag},
	} {
		if flag.value == "" {
			flags = append(flags, "--"+flag.name)
		}
	}
	if len(flags) > 0 {
		return nil, apperrors.NewValidation(
			fmt.Sprintf("missing required flag(s): %s\n  usage: %s\n  example:\n%s",
				strings.Join(flags, ", "), updateA2UICardUseLine, updateA2UICardExample),
			apperrors.WithReason("missing_required_flags"),
		)
	}
	// internal/helpers/chat.go:7540-7571.
	bizID, err := chatmsg.NormalizeCardBizID(bizIDFlag)
	if err != nil {
		return nil, err
	}
	flowStatus, err := normalizeA2UIUpdateFlowStatus(flowStatusFlag)
	if err != nil {
		return nil, err
	}
	var annotations []map[string]json.RawMessage
	if annotationsChanged {
		var err error
		annotations, err = parseA2UIAnnotations(string(annotationsFlag))
		if err != nil {
			return nil, err
		}
	}
	messages, err := parseA2UIMessages(content)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"requestId":       uuid.NewString(),
		"bizId":           bizID,
		"flowStatus":      flowStatus,
		"a2uiMessages":    messages,
		"a2uiAnnotations": []any{},
	}
	if annotationsChanged {
		params["a2uiAnnotations"] = annotations
	}
	return params, nil
}
