package service

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

var replyDecisionBlockPattern = regexp.MustCompile("(?ms)^```multica-reply-decision(?:\\s+|\\r?\\n)(.*?)\\s*```[ \\t]*(?:\\r?\\n|$)")
var optionalJSONFencePattern = regexp.MustCompile("(?ms)^```(?:json)?\\r?\\n(\\s*\\{[\\s\\S]*?\\}\\s*)\\r?\\n```[ \\t]*(?:\\r?\\n|$)")

// NormalizeReplyDecisionOutput removes internal reply-decision control data
// from user-visible output. The last strictly valid decision wins; when no
// valid decision exists, fallback is retained for rolling compatibility
// with daemons that already send reply_decision separately.
//
// Models often omit the protocol fence and dump {"shouldReply":true} as
// trailing JSON. That JSON is Host control data and must never reach IM.
func NormalizeReplyDecisionOutput(output string, fallback *protocol.ReplyDecision) (string, *protocol.ReplyDecision) {
	visible, decision := stripReplyDecisionFences(output, fallback, replyDecisionBlockPattern, true)
	visible, decision = stripReplyDecisionFences(visible, decision, optionalJSONFencePattern, false)
	visible, decision = stripTrailingReplyDecisionJSON(visible, decision)
	return strings.TrimRightFunc(visible, unicode.IsSpace), decision
}

func stripReplyDecisionFences(output string, decision *protocol.ReplyDecision, pattern *regexp.Regexp, removeInvalid bool) (string, *protocol.ReplyDecision) {
	matches := pattern.FindAllStringSubmatchIndex(output, -1)
	if len(matches) == 0 {
		return output, decision
	}
	var visible strings.Builder
	visible.Grow(len(output))
	cursor := 0
	for _, match := range matches {
		body := output[match[2]:match[3]]
		parsed := parseReplyDecisionJSON(strings.TrimSpace(body))
		if parsed == nil && !removeInvalid {
			continue
		}
		visible.WriteString(output[cursor:match[0]])
		if parsed != nil {
			decision = parsed
		}
		cursor = match[1]
		if visible.Len() == 0 || endsWithBlankLine(visible.String()) {
			cursor = skipLeadingBlankLine(output, cursor)
		}
	}
	visible.WriteString(output[cursor:])
	return visible.String(), decision
}

func stripTrailingReplyDecisionJSON(output string, decision *protocol.ReplyDecision) (string, *protocol.ReplyDecision) {
	for {
		start := strings.LastIndexByte(output, '{')
		if start < 0 {
			return output, decision
		}
		end, ok := matchingObjectEnd(output, start)
		if !ok {
			return output, decision
		}
		if strings.TrimSpace(output[end+1:]) != "" {
			return output, decision
		}
		parsed := parseReplyDecisionJSON(output[start : end+1])
		if parsed == nil {
			return output, decision
		}
		decision = parsed
		output = strings.TrimRightFunc(output[:start], unicode.IsSpace)
	}
}

func matchingObjectEnd(s string, start int) (int, bool) {
	if start >= len(s) || s[start] != '{' {
		return 0, false
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func normalizeTaskCompletionResult(result []byte) []byte {
	var payload protocol.TaskCompletedPayload
	if json.Unmarshal(result, &payload) != nil {
		return result
	}
	output, decision := NormalizeReplyDecisionOutput(payload.Output, payload.ReplyDecision)
	if output == payload.Output && decision == payload.ReplyDecision {
		return result
	}

	var object map[string]json.RawMessage
	if json.Unmarshal(result, &object) != nil {
		return result
	}
	encodedOutput, err := json.Marshal(output)
	if err != nil {
		return result
	}
	object["output"] = encodedOutput
	if decision == nil {
		delete(object, "reply_decision")
	} else {
		encodedDecision, marshalErr := json.Marshal(decision)
		if marshalErr != nil {
			return result
		}
		object["reply_decision"] = encodedDecision
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return result
	}
	return normalized
}

func parseReplyDecisionJSON(raw string) *protocol.ReplyDecision {
	var wire struct {
		ShouldReply *bool           `json:"shouldReply"`
		Reason      json.RawMessage `json:"reason,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.ShouldReply == nil {
		return nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil
	}
	reason := ""
	if len(wire.Reason) > 0 {
		if string(wire.Reason) == "null" || json.Unmarshal(wire.Reason, &reason) != nil {
			return nil
		}
	}
	return &protocol.ReplyDecision{ShouldReply: *wire.ShouldReply, Reason: reason}
}

func endsWithBlankLine(output string) bool {
	return strings.HasSuffix(output, "\n\n") || strings.HasSuffix(output, "\r\n\r\n")
}

func skipLeadingBlankLine(output string, cursor int) int {
	if strings.HasPrefix(output[cursor:], "\r\n") {
		return cursor + 2
	}
	if strings.HasPrefix(output[cursor:], "\n") {
		return cursor + 1
	}
	return cursor
}
