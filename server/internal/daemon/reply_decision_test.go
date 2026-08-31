package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestParseReplyDecisionFromFinalFence(t *testing.T) {
	output := "可见回复\n\n```multica-reply-decision\n{\"shouldReply\":false,\"reason\":\"echo\"}\n```"

	visible, decision := parseReplyDecision(output)

	if visible != "可见回复" {
		t.Fatalf("visible output = %q", visible)
	}
	if decision == nil || decision.ShouldReply || decision.Reason != "echo" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestParseReplyDecisionRejectsInvalidOrNonTerminalFence(t *testing.T) {
	tests := []string{
		"正文\n\n```multica-reply-decision\n{\"shouldReply\":false}\n```\n尾部",
		"正文\n\n```multica-reply-decision\n{\"reason\":\"echo\"}\n```",
		"正文\n\n```multica-reply-decision\n{\"shouldReply\":false,\"extra\":true}\n```",
		"正文\n\n```multica-reply-decision\n{\"shouldReply\":\"false\"}\n```",
		"正文\n\n```multica-reply-decision\n{\"shouldReply\":false,\"reason\":null}\n```",
	}
	for _, output := range tests {
		visible, decision := parseReplyDecision(output)
		if visible != output || decision != nil {
			t.Fatalf("parseReplyDecision(%q) = %q, %#v", output, visible, decision)
		}
	}
}

func TestTaskResultReplyDecisionUsesSharedProtocolShape(t *testing.T) {
	result := TaskResult{ReplyDecision: &protocol.ReplyDecision{ShouldReply: true}}
	if result.ReplyDecision == nil || !result.ReplyDecision.ShouldReply {
		t.Fatalf("reply decision = %#v", result.ReplyDecision)
	}
}
