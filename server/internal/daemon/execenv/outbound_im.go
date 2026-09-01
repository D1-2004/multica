package execenv

import (
	"strings"
)

// OutboundChat is one parsed DingTalk chat send or reply from a tool/bash event.
type OutboundChat struct {
	Action         string // send | reply
	ConversationID string
	EvidenceID     string
	PersonID       string
	OpenTaskID     string
}

// ToolEvent is one persisted task message used as an outbound event filter input.
type ToolEvent struct {
	Command string
	Output  string
	Input   map[string]any
}

// LooksLikeDWSChatOutbound reports send or reply, not list/search/status/help.
func LooksLikeDWSChatOutbound(command string) bool {
	s := strings.ToLower(command)
	if strings.Contains(s, "message list") || strings.Contains(s, "query-send-status") ||
		strings.Contains(s, "send-status") || strings.Contains(s, "search") ||
		strings.Contains(s, "--help") || strings.Contains(s, "calendar send") {
		return false
	}
	hasOutbound := strings.Contains(s, "message send") || strings.Contains(s, "message_send") ||
		strings.Contains(s, "chat send") || strings.Contains(s, "+dm") ||
		strings.Contains(s, "+send") || strings.Contains(s, "send-to-group") ||
		strings.Contains(s, "send-by-bot") || strings.Contains(s, "message reply") ||
		strings.Contains(s, "messages-reply") || strings.Contains(s, "+messages-reply")
	if !hasOutbound {
		return false
	}
	return strings.Contains(s, "dws") || strings.Contains(s, "chat") || strings.Contains(s, "dingtalk")
}

func looksLikeQuerySendStatus(command string) bool {
	s := strings.ToLower(command)
	return strings.Contains(s, "query-send-status") || strings.Contains(s, "send-status")
}

func outboundAction(command string) string {
	s := strings.ToLower(command)
	if strings.Contains(s, "message reply") || strings.Contains(s, "messages-reply") {
		return "reply"
	}
	return "send"
}

// ParseOutboundChat extracts send/reply, scene, person, and DWS task id from one event.
func ParseOutboundChat(command, output string, input map[string]any) OutboundChat {
	hint := command
	out := OutboundChat{
		PersonID:   firstFlag(hint, "--user", "--open-dingtalk-id"),
		OpenTaskID: extractJSONString(output, "openTaskId"),
	}
	if LooksLikeDWSChatOutbound(hint) {
		out.Action = outboundAction(hint)
	}
	cid, evid := ExtractConversationFromTool(hint, output, input)
	out.ConversationID = cid
	out.EvidenceID = evid
	if out.OpenTaskID == "" {
		out.OpenTaskID = extractJSONString(hint, "openTaskId")
	}
	if out.PersonID == "" && input != nil {
		out.PersonID = firstFlag(flattenInput(input), "--user", "--open-dingtalk-id")
	}
	if !plausibleConversationOrEvidenceID(out.PersonID) {
		out.PersonID = ""
	}
	if !plausibleConversationOrEvidenceID(out.OpenTaskID) {
		out.OpenTaskID = ""
	}
	return out
}

// FilterOutboundChat walks a tool/bash event batch: send/reply are outbound,
// query-send-status receipts fill missing openConversationId for the matching
// openTaskId (dws send --user only returns openTaskId).
func FilterOutboundChat(events []ToolEvent) []OutboundChat {
	receipts := map[string]OutboundChat{}
	var loose []OutboundChat
	var out []OutboundChat
	for _, event := range events {
		hint := event.Command
		if extra := flattenInput(event.Input); extra != "" {
			hint = hint + " " + extra
		}
		parsed := ParseOutboundChat(hint, event.Output, event.Input)
		cid, evid := ExtractDWSReceipt(event.Output)
		if !plausibleConversationOrEvidenceID(cid) {
			cid, evid = ExtractConversationFromTool(hint, event.Output, event.Input)
		}
		if looksLikeQuerySendStatus(hint) {
			got := OutboundChat{ConversationID: cid, EvidenceID: evid, OpenTaskID: firstFlag(hint, "--open-task-id")}
			if got.OpenTaskID == "" {
				got.OpenTaskID = extractJSONString(event.Output, "openTaskId")
			}
			if plausibleConversationOrEvidenceID(got.ConversationID) {
				if got.OpenTaskID != "" {
					receipts[got.OpenTaskID] = got
				}
				loose = append(loose, got)
			}
			continue
		}
		if parsed.Action == "" {
			continue
		}
		if parsed.ConversationID == "" && plausibleConversationOrEvidenceID(cid) {
			parsed.ConversationID = cid
			if parsed.EvidenceID == "" {
				parsed.EvidenceID = evid
			}
		}
		out = append(out, parsed)
	}
	for i := range out {
		if out[i].ConversationID != "" {
			continue
		}
		if rec, ok := receipts[out[i].OpenTaskID]; ok && out[i].OpenTaskID != "" {
			out[i].ConversationID = rec.ConversationID
			if out[i].EvidenceID == "" {
				out[i].EvidenceID = rec.EvidenceID
			}
			continue
		}
		if len(loose) == 1 && out[i].ConversationID == "" {
			out[i].ConversationID = loose[0].ConversationID
			if out[i].EvidenceID == "" {
				out[i].EvidenceID = loose[0].EvidenceID
			}
		}
	}
	return out
}

func flattenInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	var b strings.Builder
	for _, raw := range input {
		if s, ok := raw.(string); ok {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(s)
		}
	}
	return b.String()
}

func firstFlag(text string, names ...string) string {
	tokens := tokenizeCLI(text)
	for i, tok := range tokens {
		for _, name := range names {
			if tok == name && i+1 < len(tokens) {
				return strings.Trim(tokens[i+1], `"'`)
			}
			if prefix := name + "="; strings.HasPrefix(tok, prefix) {
				return strings.Trim(strings.TrimPrefix(tok, prefix), `"'`)
			}
		}
	}
	return ""
}

func tokenizeCLI(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
