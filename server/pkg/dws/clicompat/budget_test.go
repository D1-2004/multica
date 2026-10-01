package clicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func listOf(messages ...map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"messages": messages, "hasMore": false})
	return raw
}

func renderedMessages(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	return payload.Messages
}

// Message text a group member controls cannot make the projection slow:
// the message is kept, without chatmsg's projection.
func TestResourceHeavyTextIsNotProjected(t *testing.T) {
	var ids []string
	for i := 0; i < 1000; i++ {
		ids = append(ids, fmt.Sprintf(`{"mediaId":"%d"}`, i))
	}
	content := `{"quoted":{"x":[` + strings.Join(ids, ",") + `]}}`
	start := time.Now()
	out := MessageListOutput(listOf(map[string]any{"openMessageId": "m1", "openConversationId": "cid",
		"senderOpenDingTalkId": "D1", "sender": "张三", "content": content, "createTime": "2026-09-30 11:58:03"}), "older")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("hostile message took %v", elapsed)
	}
	m := renderedMessages(t, out)
	if len(m) != 1 || m[0]["projectionSkipped"] != "complexity_limit" || m[0]["content"] != content || m[0]["senderId"] != "D1" ||
		m[0]["messageId"] != "m1" || m[0]["sender"] != "张三" || m[0]["createTime"] != "2026-09-30 11:58:03" {
		t.Fatalf("bounded message = %v", m)
	}
}

// A deep forward chain cannot blow memory up.
func TestDeepForwardChainIsNotProjected(t *testing.T) {
	var msg any = map[string]any{"openMessageId": "leaf", "content": "x"}
	for i := 0; i < 1000; i++ {
		msg = map[string]any{"openMessageId": fmt.Sprint("m", i), "content": "fwd", "forwardMessages": []any{msg}}
	}
	start := time.Now()
	out := MessageListOutput(listOf(msg.(map[string]any)), "older")
	if elapsed := time.Since(start); elapsed > time.Second || len(out) > 64<<10 {
		t.Fatalf("deep chain took %v and %d bytes", elapsed, len(out))
	}
	if m := renderedMessages(t, out); len(m) != 1 || m[0]["projectionSkipped"] != "complexity_limit" {
		t.Fatalf("deep chain = %v", m)
	}
}

// Ordinary messages, forwards and quotes included, are projected as dws does.
func TestOrdinaryMessagesStayProjected(t *testing.T) {
	forward := map[string]any{"openMessageId": "f1", "content": "[图片] mediaId: @lAD1", "sender": "李四"}
	out := MessageListOutput(listOf(
		map[string]any{"openMessageId": "m1", "senderOpenDingTalkId": "D1", "sender": "张三", "content": "hello",
			"createTime": "2026-09-30 11:58:03", "quotedMessage": map[string]any{"openMessageId": "q1", "content": "earlier"}},
		map[string]any{"openMessageId": "m2", "senderOpenDingTalkId": "D2", "content": "看这些", "forwardMessages": []any{forward, forward}},
	), "older")
	for _, m := range renderedMessages(t, out) {
		if _, skipped := m["projectionSkipped"]; skipped {
			t.Fatalf("ordinary message skipped: %v", m)
		}
		if m["senderId"] == nil || m["messageId"] == nil {
			t.Fatalf("ordinary message not projected: %v", m)
		}
	}
}
