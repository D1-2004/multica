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

func TestContinuationContentPrefersLookIntoBriefOverProcessingPipeline(t *testing.T) {
	item := WindowItem{
		Purpose:  "冬翔委托：为冬翔额外提供 2-3 份校招生日志事实点及关怀文案草稿",
		LookInto: "委托人：冬翔。用户要：多给几份。刚才发生：刚看过一份关怀成稿样本。这不是：不要默认继续拟关怀文案。交付：优先其他人日志原文。",
		Content:  "冬翔 在钉钉会话中的消息：\n\n是的，多给我几份",
	}
	got := ContinuationContent(item)
	if !strings.Contains(got, "本次续接只推进这一个交付物："+item.LookInto) {
		t.Fatalf("look_into brief must be the live deliverable: %q", got)
	}
	if strings.HasPrefix(got, "本次续接只推进这一个交付物：为冬翔额外提供") {
		t.Fatalf("purpose processing pipeline must not lock the continuation: %q", got)
	}
	if !strings.Contains(got, "事项简报：") || !strings.HasSuffix(got, item.Content) {
		t.Fatalf("purpose remains provenance and user words stay exact: %q", got)
	}
}
