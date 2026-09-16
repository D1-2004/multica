package execenv

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DWSMessagePolicyEnv carries the server-attested snapshot for this task only.
const DWSMessagePolicyEnv = "MULTICA_DINGTALK_MESSAGE_POLICY"

// ApplyDWSMessagePolicyEnv must run after custom_env and identity isolation.
// An explicit empty value masks inherited policy from a previous warm task.
func ApplyDWSMessagePolicyEnv(env map[string]string, policy *protocol.DingTalkMessagePolicy) {
	env[DWSMessagePolicyEnv] = ""
	if policy != nil {
		body, _ := json.Marshal(policy)
		env[DWSMessagePolicyEnv] = string(body)
	}
}

type dwsCommand struct {
	conversationID string
	send           bool
	userSend       bool
	preview        bool
	aiTagIndices   map[int]bool
	separator      int
	identityArgs   []string
	values         map[string]string
}

// parseDWSCommand understands the pinned DWS argv grammar, not shell text.
// String flag values and positional content are never interpreted as commands
// or flags; Cobra boolean flags do not consume the following positional value.
func parseDWSCommand(args []string) dwsCommand {
	parsed := dwsCommand{separator: len(args), aiTagIndices: map[int]bool{}}
	var path []string
	var positional []string
	values := make(map[string]string)
	parsed.values = values
	help := false
	leaf := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if i == 0 && arg == "dws" {
			continue
		}
		if arg == "--" {
			parsed.separator = i
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name, value, assigned := strings.Cut(arg, "=")
			if strings.HasPrefix(name, "--") {
				name = strings.TrimPrefix(name, "--")
			} else {
				// pflag accepts -fjson and combined boolean shorthands (-vy).
				short := strings.TrimPrefix(name, "-")
				if strings.HasPrefix(short, "f") {
					name = "format"
					if len(short) > 1 {
						value, assigned = short[1:], true
					}
				} else {
					valid := true
					for _, ch := range short {
						switch ch {
						case 'h':
							help = !assigned || value != "false"
						case 'v', 'y':
						default:
							valid = false
						}
					}
					if !valid {
						return dwsCommand{}
					}
					continue
				}
			}
			if dwsBooleanFlag(name) {
				active := true
				if assigned {
					var err error
					active, err = strconv.ParseBool(value)
					if err != nil && name != "ai-tag" {
						return dwsCommand{}
					}
				}
				switch name {
				case "ai-tag":
					parsed.aiTagIndices[i] = true
				case "help":
					help = active
				case "dry-run", "mock":
					parsed.preview = parsed.preview || active
				case "at-all":
					values[name] = strconv.FormatBool(active)
				}
				continue
			}
			if !dwsStringFlag(name) {
				// Unknown flags are rejected by this Runtime's DWS version. Do
				// not reinterpret their values as an unrelated send command.
				return dwsCommand{}
			}
			if !assigned {
				if i+1 == len(args) {
					return dwsCommand{}
				}
				i++
				value = args[i]
			}
			values[name] = value
			switch name {
			case "token", "profile", "client-id", "client-secret", "timeout":
				parsed.identityArgs = append(parsed.identityArgs, "--"+name, value)
			}
			continue
		}
		if !leaf {
			path = append(path, arg)
			leaf = len(path) >= 2 && (path[1] != "message" || len(path) == 3)
		} else {
			positional = append(positional, arg)
		}
	}
	if help || len(path) < 2 || path[0] != "chat" {
		return dwsCommand{}
	}
	command := strings.Join(path[1:], " ")
	if len(positional) > 0 && values["content"] == "" && values["text"] == "" && values["markdown"] == "" {
		values["content"] = strings.Join(positional, " ")
	}
	switch command {
	case "message send", "message reply", "send", "reply", "+send", "+dm", "+send-to-group", "+messages-reply":
		parsed.send, parsed.userSend = true, true
	case "+messages-send":
		parsed.userSend = (values["as"] == "" || values["as"] == "user") &&
			(values["identity"] == "" || values["identity"] == "user")
		parsed.send = parsed.userSend
	case "message send-by-bot", "send-by-bot":
		parsed.send = true
	default:
		return dwsCommand{}
	}
	for _, name := range []string{"conversation-id", "conversation", "chat-id", "group"} {
		if values[name] != "" {
			parsed.conversationID = strings.TrimSpace(values[name])
			break
		}
	}
	// This shortcut accepts names too. Only its receipt identifies the scene.
	if command == "+send-to-group" {
		parsed.conversationID = ""
	}
	return parsed
}

// dwsMentionsOnly reports whether the send addresses nobody but sender. Mobile
// and userId at lists are opaque here, so any of them keeps the plain send.
func dwsMentionsOnly(parsed dwsCommand, sender string) bool {
	if parsed.values["at-all"] == "true" ||
		strings.TrimSpace(parsed.values["at-mobiles"]) != "" ||
		strings.TrimSpace(parsed.values["at-user-ids"]) != "" {
		return false
	}
	for _, id := range strings.Split(parsed.values["at-open-dingtalk-ids"], ",") {
		if id = strings.TrimSpace(id); id != "" && id != sender {
			return false
		}
	}
	return true
}

func dwsBooleanFlag(name string) bool {
	switch name {
	case "ai-tag", "at-all", "help", "debug", "dry-run", "mock", "verbose", "yes":
		return true
	}
	return false
}

func dwsStringFlag(name string) bool {
	switch name {
	case "client-id", "client-secret", "fields", "format", "jq", "profile", "timeout", "token",
		"at-open-dingtalk-ids", "at-mobiles", "at-user-ids", "contact-id", "content", "text", "markdown",
		"conversation-id", "conversation", "file", "file-path", "group", "chat-id", "groups", "groups-file",
		"idempotency-key", "uuid", "latitude", "location-name", "longitude", "map-thumbnail-url", "media-id",
		"msg-type", "open-dingtalk-id", "open-dingtalk-ids", "title", "user", "users", "to",
		"as", "identity", "chat-query", "user-query", "robot-code", "webhook-token",
		"message-id", "ref-msg-id", "ref-sender":
		return true
	}
	return false
}

// RewriteDWSSendAITag replaces only actual --ai-tag flags on current-user
// sends. In particular, `--content --ai-tag=false` and everything after `--`
// are message content and remain byte-for-byte identical.
// RewriteDWSOriginReply turns a current-user send into the origin conversation
// into a quote-reply. Outreach to a different conversation is unchanged.
func RewriteDWSOriginReply(args []string, policy *protocol.DingTalkMessagePolicy) []string {
	if policy == nil {
		return args
	}
	origin := strings.TrimSpace(policy.ReplyToOpenMsgID)
	cid := strings.TrimSpace(policy.ReplyConversationID)
	if origin == "" || cid == "" {
		return args
	}
	parsed := parseDWSCommand(args)
	if !parsed.userSend || parsed.preview {
		return args
	}
	if parsed.values["file"] != "" || parsed.values["file-path"] != "" || parsed.values["msg-type"] != "" {
		return args
	}
	if parsed.conversationID == "" || parsed.conversationID != cid {
		return args
	}
	if parsed.values["message-id"] != "" || parsed.values["ref-msg-id"] != "" {
		return args
	}
	sender := strings.TrimSpace(policy.ReplyToSenderOpenDingTalkID)
	// A quote reply takes no at list; DingTalk addresses the quoted sender by
	// itself. Mentions of anyone else would be silently dropped, so that send
	// stays a plain one.
	if !dwsMentionsOnly(parsed, sender) {
		return args
	}
	content := parsed.values["content"]
	if content == "" {
		content = parsed.values["text"]
	}
	if content == "" {
		content = parsed.values["markdown"]
	}
	if content == "" {
		return args
	}
	content = dwsclient.StripLeadingMention(content, sender)
	if strings.TrimSpace(content) == "" {
		return args
	}
	out := []string{"chat", "+messages-reply", "--group", cid, "--message-id", origin, "--content", content}
	if key := parsed.values["idempotency-key"]; key == "" {
		key = parsed.values["uuid"]
		if key != "" {
			out = append(out, "--idempotency-key", key)
		}
	} else {
		out = append(out, "--idempotency-key", key)
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--ai-tag") {
			out = append(out, arg)
		}
	}
	out = append(out, parsed.identityArgs...)
	out = append(out, "--yes", "--format", "json")
	return out
}

func RewriteDWSSendAITag(args []string, policy *protocol.DingTalkMessagePolicy) []string {
	parsed := parseDWSCommand(args)
	if policy == nil || !parsed.userSend {
		return args
	}
	result := make([]string, 0, len(args)+1)
	flag := "--ai-tag=" + strconv.FormatBool(policy.ShowAITag)
	for i, arg := range args {
		if i == parsed.separator {
			result = append(result, flag)
		}
		if !parsed.aiTagIndices[i] {
			result = append(result, arg)
		}
	}
	if parsed.separator == len(args) {
		result = append(result, flag)
	}
	return result
}

func dwsMessagePolicyFromEnv(getenv func(string) string) (*protocol.DingTalkMessagePolicy, error) {
	if getenv == nil || getenv(DWSMessagePolicyEnv) == "" {
		return nil, nil
	}
	var policy protocol.DingTalkMessagePolicy
	if err := json.Unmarshal([]byte(getenv(DWSMessagePolicyEnv)), &policy); err != nil {
		return nil, fmt.Errorf("invalid task DingTalk message policy: %w", err)
	}
	return &policy, nil
}
