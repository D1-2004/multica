package execenv

import "testing"

func TestLooksLikeDWSChatOutboundIncludesReply(t *testing.T) {
	t.Parallel()
	if !LooksLikeDWSChatOutbound(`dws chat message reply --conversation-id cid-a --content ok`) {
		t.Fatal("reply")
	}
	if !LooksLikeDWSChatOutbound(`dws chat +messages-reply --msg-id m1 --content ok`) {
		t.Fatal("shortcut reply")
	}
	if LooksLikeDWSChatOutbound(`dws chat message query-send-status --open-task-id t1`) {
		t.Fatal("status is not outbound")
	}
}

func TestParseOutboundChatUserSendHasPersonNoCID(t *testing.T) {
	t.Parallel()
	got := ParseOutboundChat(
		`Bash $ dws chat message send --user 0104644667680872 --content "冬翔，今天下午想喝茶还是咖啡？" --format json --yes`,
		`{"arguments":[],"result":{"openTaskId":"SjpQ4pj0hHkduwP0AfOGL34VK6cjr4bpRUsQxL+fOFs="},"success":true}`,
		nil,
	)
	if got.Action != "send" {
		t.Fatalf("action=%q", got.Action)
	}
	if got.PersonID != "0104644667680872" {
		t.Fatalf("person=%q", got.PersonID)
	}
	if got.ConversationID != "" {
		t.Fatalf("send --user must not invent cid, got %q", got.ConversationID)
	}
	if got.OpenTaskID == "" {
		t.Fatal("expected openTaskId")
	}
}

func TestFilterOutboundChatPairsUserSendWithQuerySendStatus(t *testing.T) {
	t.Parallel()
	got := FilterOutboundChat([]ToolEvent{
		{
			Command: `dws chat message send --user 0104644667680872 --content hi --format json --yes`,
			Output:  `{"result":{"openTaskId":"task-1"},"success":true}`,
		},
		{
			Command: `dws chat message query-send-status --open-task-id task-1 --format json`,
			Output:  `{"openConversationId":"cid+bEFv7ngm9n79Q1vL9HYJw==","openMessageId":"msgMG+endABtu5Z/o/XSPTDDA=="}`,
		},
	})
	if len(got) != 1 {
		t.Fatalf("n=%d", len(got))
	}
	if got[0].ConversationID != "cid+bEFv7ngm9n79Q1vL9HYJw==" {
		t.Fatalf("cid=%q", got[0].ConversationID)
	}
	if got[0].EvidenceID != "msgMG+endABtu5Z/o/XSPTDDA==" {
		t.Fatalf("evidence=%q", got[0].EvidenceID)
	}
	if got[0].PersonID != "0104644667680872" {
		t.Fatalf("person=%q", got[0].PersonID)
	}
}

func TestFilterOutboundChatPairsAdjacentHermesToolResult(t *testing.T) {
	t.Parallel()
	got := FilterOutboundChat([]ToolEvent{
		{
			Command: `terminal {"text":"$ dws chat message send --open-dingtalk-id \"uid-v6\" --text \"今晚几点打球？\" -y -f json"}`,
			Input:   map[string]any{"text": `$ dws chat message send --open-dingtalk-id "uid-v6" --text "今晚几点打球？" -y -f json`},
		},
		{
			Command: "terminal",
			Output: `terminal result
- **output:** {"result":{"openTaskId":"task-1","openConversationId":"cid-v6","openMessageId":"msg-v6"},"success":true}
- **exit_code:** 0`,
		},
	})
	if len(got) != 1 {
		t.Fatalf("n=%d", len(got))
	}
	if got[0].ConversationID != "cid-v6" || got[0].EvidenceID != "msg-v6" {
		t.Fatalf("outbound=%+v", got[0])
	}
	if got[0].PersonID != "uid-v6" {
		t.Fatalf("person=%q", got[0].PersonID)
	}
}

func TestFilterOutboundChatDoesNotBindListAlone(t *testing.T) {
	t.Parallel()
	got := FilterOutboundChat([]ToolEvent{
		{
			Command: `dws chat message list --conversation-id cid+bEFv7ngm9n79Q1vL9HYJw==`,
			Output:  `{"openConversationId":"cid+bEFv7ngm9n79Q1vL9HYJw==","openMsgId":"msg-list"}`,
		},
	})
	if len(got) != 0 {
		t.Fatalf("list must not be outbound: %+v", got)
	}
}
