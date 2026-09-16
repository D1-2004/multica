package dwsclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStripLeadingMentionKeepsDeliberateMentions(t *testing.T) {
	for _, tc := range []struct{ name, content, id, want string }{
		{"addressing prefix", "<@sender> 日报范围补充说明", "sender", "日报范围补充说明"},
		{"repeated prefix", "<@sender> <@sender>  正文", "sender", "正文"},
		{"leading spaces", "  <@sender>正文", "sender", "正文"},
		{"other member", "<@other> 正文", "sender", "<@other> 正文"},
		{"inside sentence", "麻烦 <@sender> 复核", "sender", "麻烦 <@sender> 复核"},
		{"unknown sender", "<@sender> 正文", "", "<@sender> 正文"},
		{"only a mention", "<@sender>", "sender", ""},
	} {
		if got := StripLeadingMention(tc.content, tc.id); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// A quote reply is already addressed to the quoted sender by DingTalk, so the
// placeholder a plain send would need must not reach the reply content.
func TestSendQuoteReplyDropsDuplicateMention(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-dws")
	recorded := filepath.Join(dir, "args.json")
	target, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import json,sys
json.dump(sys.argv[1:], open(` + string(target) + `, "w"))
print(json.dumps({'success':True,'openTaskId':'task'}))
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cli := CLI{Path: exe}
	if _, err := cli.Send(context.Background(), dir, SendRequest{
		ConversationID: "cid", ReplyToOpenMsgID: "msg-origin", AtOpenDingTalkID: "sender",
		Content: "<@sender> 具体日期范围补充说明", IdempotencyKey: "stable",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	content := ""
	for i, arg := range args {
		switch arg {
		case "--content":
			content = args[i+1]
		case "--at-open-dingtalk-ids":
			t.Fatal("quote reply carried an at list")
		}
	}
	if content != "具体日期范围补充说明" {
		t.Fatalf("content = %q", content)
	}
	// A pure @ ping has nothing left after stripping, and an empty reply
	// cannot be sent, so the placeholder stays.
	if _, err := cli.Send(context.Background(), dir, SendRequest{
		ConversationID: "cid", ReplyToOpenMsgID: "msg-origin", AtOpenDingTalkID: "sender",
		Content: "<@sender> ", IdempotencyKey: "stable",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	args = nil
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if arg == "--content" && args[i+1] != "<@sender> " {
			t.Fatalf("mention-only content = %q", args[i+1])
		}
	}
}
