package inboundcoord

import (
	"strings"
	"testing"
)

func TestContinuationContentKeepsOneScopeAndExactEvidence(t *testing.T) {
	item := WindowItem{Purpose: "小周委托：把原会议改到三点", Content: "小周 在钉钉会话中的消息：\n\n原会议改三点，另外查明天天气。"}
	got := ContinuationContent(item)
	if !strings.HasPrefix(got, "本次续接只推进这一个交付物：把原会议改到三点\n") || !strings.Contains(got, "其它已分派工作不属于本次执行范围") || !strings.HasSuffix(got, item.Content) {
		t.Fatalf("scope/evidence changed: %q", got)
	}
}
