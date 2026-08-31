package service

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

var replyDecisionBlockPattern = regexp.MustCompile("(?ms)^```multica-reply-decision\\r?\\n(.*?)\\r?\\n```[ \\t]*(?:\\r?\\n|$)")

// NormalizeReplyDecisionOutput removes internal reply-decision fences from
// user-visible output. The last strictly valid fenced decision wins; when no
// valid fenced decision exists, fallback is retained for rolling compatibility
// with daemons that already send reply_decision separately.
func NormalizeReplyDecisionOutput(output string, fallback *protocol.ReplyDecision) (string, *protocol.ReplyDecision) {
	matches := replyDecisionBlockPattern.FindAllStringSubmatchIndex(output, -1)
	if len(matches) == 0 {
		return output, fallback
	}

	var visible strings.Builder
	visible.Grow(len(output))
	cursor := 0
	decision := fallback
	for _, match := range matches {
		visible.WriteString(output[cursor:match[0]])
		if parsed := parseReplyDecisionJSON(output[match[2]:match[3]]); parsed != nil {
			decision = parsed
		}
		cursor = match[1]
		if visible.Len() == 0 || endsWithBlankLine(visible.String()) {
			cursor = skipLeadingBlankLine(output, cursor)
		}
	}
	visible.WriteString(output[cursor:])
	return strings.TrimRightFunc(visible.String(), unicode.IsSpace), decision
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
