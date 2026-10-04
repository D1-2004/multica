package employeetask

import (
	"strings"
	"testing"
)

func TestWorkPacketCompletionNoticePolicy(t *testing.T) {
	input := compilerFixture()
	packet, err := Compile(input)
	if err != nil || packet.CompletionNotice.Mode != CompletionNoticeAlways {
		t.Fatal("default completion behavior changed", err)
	}
	if strings.Contains(packet.Text, "COMPLETION NOTICE POLICY") {
		t.Fatal("default policy changed the legacy packet bytes")
	}
	input.CompletionNotice = CompletionNoticePolicy{Mode: CompletionNoticeIfNotDelivered, RequireDelivery: "file", SourceRef: input.Source.Ref, InstructionQuote: "文件发出后不用再发总结"}
	packet, err = Compile(input)
	if err != nil || packet.CompletionNotice != input.CompletionNotice || !strings.Contains(packet.Text, "if_not_delivered") || !strings.Contains(packet.Text, input.CompletionNotice.InstructionQuote) {
		t.Fatal("policy not carried into execution", err)
	}
	input.CompletionNotice.SourceRef = "another-message"
	if _, err = Compile(input); err == nil {
		t.Fatal("foreign policy evidence accepted")
	}
}
