package daemon

import (
	"encoding/json"
	"io"
	"strings"
	"unicode"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const replyDecisionFence = "```multica-reply-decision"

func parseReplyDecision(output string) (string, *protocol.ReplyDecision) {
	trimmed := strings.TrimRightFunc(output, unicode.IsSpace)
	fenceStart := strings.LastIndex(trimmed, replyDecisionFence)
	if fenceStart < 0 || fenceStart > 0 && trimmed[fenceStart-1] != '\n' {
		return output, nil
	}

	fenced := trimmed[fenceStart:]
	lineBreak := "\n"
	if strings.HasPrefix(fenced, replyDecisionFence+"\r\n") {
		lineBreak = "\r\n"
	} else if !strings.HasPrefix(fenced, replyDecisionFence+"\n") {
		return output, nil
	}
	closing := lineBreak + "```"
	if !strings.HasSuffix(fenced, closing) {
		return output, nil
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(fenced, replyDecisionFence+lineBreak), closing)

	var wire struct {
		ShouldReply *bool           `json:"shouldReply"`
		Reason      json.RawMessage `json:"reason,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.ShouldReply == nil {
		return output, nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return output, nil
	}
	reason := ""
	if len(wire.Reason) > 0 {
		if string(wire.Reason) == "null" || json.Unmarshal(wire.Reason, &reason) != nil {
			return output, nil
		}
	}

	visible := strings.TrimRightFunc(trimmed[:fenceStart], unicode.IsSpace)
	return visible, &protocol.ReplyDecision{ShouldReply: *wire.ShouldReply, Reason: reason}
}
