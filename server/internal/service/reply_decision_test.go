package service

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestNormalizeReplyDecisionOutputFromFinalFence(t *testing.T) {
	output := "可见回复\n\n```multica-reply-decision\n{\"shouldReply\":false,\"reason\":\"echo\"}\n```"

	visible, decision := NormalizeReplyDecisionOutput(output, nil)

	if visible != "可见回复" {
		t.Fatalf("visible output = %q", visible)
	}
	if decision == nil || decision.ShouldReply || decision.Reason != "echo" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestNormalizeReplyDecisionOutputFromNonTerminalFence(t *testing.T) {
	output := "前文\n\n```multica-reply-decision\n{\"shouldReply\":false,\"reason\":\"echo\"}\n```\n\n后文"

	visible, decision := NormalizeReplyDecisionOutput(output, nil)

	if visible != "前文\n\n后文" {
		t.Fatalf("visible output = %q", visible)
	}
	if decision == nil || decision.ShouldReply || decision.Reason != "echo" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestNormalizeReplyDecisionOutputCleansInvalidFenceWithoutDecision(t *testing.T) {
	contents := []string{
		`{"shouldReply":"false"}`,
		`{"reason":"echo"}`,
		`{"shouldReply":false,"extra":true}`,
		`{"shouldReply":false,"reason":null}`,
		`{not-json`,
	}
	for _, content := range contents {
		output := "前文\n\n```multica-reply-decision\n" + content + "\n```\n\n后文"

		visible, decision := NormalizeReplyDecisionOutput(output, nil)

		if visible != "前文\n\n后文" || decision != nil {
			t.Fatalf("NormalizeReplyDecisionOutput(%q) = %q, %#v", content, visible, decision)
		}
	}
}

func TestNormalizeReplyDecisionOutputUsesLastValidFenceAndPreservesOrdinaryCode(t *testing.T) {
	output := "开始\n\n```multica-reply-decision\n{\"shouldReply\":false,\"reason\":\"first\"}\n```\n\n```json\n{\"visible\":true}\n```\n\n```multica-reply-decision\n{\"reason\":\"invalid\"}\n```\n\n```multica-reply-decision\n{\"shouldReply\":true,\"reason\":\"last\"}\n```\n\n结束"

	visible, decision := NormalizeReplyDecisionOutput(output, nil)

	wantVisible := "开始\n\n```json\n{\"visible\":true}\n```\n\n结束"
	if visible != wantVisible {
		t.Fatalf("visible output = %q, want %q", visible, wantVisible)
	}
	if decision == nil || !decision.ShouldReply || decision.Reason != "last" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestNormalizeReplyDecisionOutputCleansCRLFBlockBoundary(t *testing.T) {
	output := "前文\r\n\r\n```multica-reply-decision\r\n{\"shouldReply\":false}\r\n```\r\n\r\n后文"

	visible, decision := NormalizeReplyDecisionOutput(output, nil)

	if visible != "前文\r\n\r\n后文" {
		t.Fatalf("visible output = %q", visible)
	}
	if decision == nil || decision.ShouldReply {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestNormalizeReplyDecisionOutputValidFenceOverridesStructuredFallback(t *testing.T) {
	fallback := &protocol.ReplyDecision{ShouldReply: true, Reason: "daemon"}
	output := "回复\n\n```multica-reply-decision\n{\"shouldReply\":false,\"reason\":\"output\"}\n```"

	visible, decision := NormalizeReplyDecisionOutput(output, fallback)

	if visible != "回复" || decision == nil || decision.ShouldReply || decision.Reason != "output" {
		t.Fatalf("NormalizeReplyDecisionOutput() = %q, %#v", visible, decision)
	}
}

func TestNormalizeReplyDecisionOutputInvalidFenceRetainsStructuredFallback(t *testing.T) {
	fallback := &protocol.ReplyDecision{ShouldReply: true, Reason: "daemon"}
	output := "回复\n\n```multica-reply-decision\n{\"shouldReply\":\"false\"}\n```"

	visible, decision := NormalizeReplyDecisionOutput(output, fallback)

	if visible != "回复" || decision != fallback {
		t.Fatalf("NormalizeReplyDecisionOutput() = %q, %#v", visible, decision)
	}
}
