package dingtalk

import (
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"testing"
)

func TestParseFreshSessionCommand(t *testing.T) {
	cases := []struct {
		body     string
		wantBody string
		wantOK   bool
	}{
		{"/new", "", true},
		{"/new 帮我看看这个报错", "帮我看看这个报错", true},
		{"  /new  hello ", "hello", true},
		{"\n\n/new next line\nmore", "next line\nmore", true},
		{"/new\n后续内容", "后续内容", true},
		{"/newx", "", false},
		{"/New", "", false},
		{"say /new inline", "", false},
		{"hello\n/new", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := parseFreshSessionCommand(c.body)
		if ok != c.wantOK || got != c.wantBody {
			t.Errorf("parseFreshSessionCommand(%q) = (%q, %v), want (%q, %v)", c.body, got, ok, c.wantBody, c.wantOK)
		}
	}
}

func TestInboundNewCommandForcesFresh(t *testing.T) {
	msg, ok := inboundFromBotCallback(botCallbackData{
		ConversationID:   "cid",
		MsgID:            "m1",
		SenderStaffID:    "staff_1",
		ConversationType: "1",
		Msgtype:          "text",
		Text: struct {
			Content string `json:"content"`
		}{Content: "/new 重新开始"},
	}, "client_a")
	if !ok {
		t.Fatal("inbound mapping failed")
	}
	if !msg.ForceFresh {
		t.Fatal("expected ForceFresh=true for /new")
	}
	if msg.Text != "重新开始" {
		t.Fatalf("expected stripped body, got %q", msg.Text)
	}
}

func TestInboundPlainTextDoesNotForceFresh(t *testing.T) {
	msg, ok := inboundFromBotCallback(botCallbackData{
		ConversationID:   "cid",
		MsgID:            "m1",
		SenderStaffID:    "staff_1",
		ConversationType: "1",
		Msgtype:          "text",
		Text: struct {
			Content string `json:"content"`
		}{Content: "你好"},
	}, "client_a")
	if !ok {
		t.Fatal("inbound mapping failed")
	}
	if msg.ForceFresh {
		t.Fatal("plain text must not force fresh")
	}
}

// Exercise the adapter-to-router boundary: an empty stripped body must still
// carry the directive that makes Router persist the next-turn reset.
func TestBareNewSurvivesRouterCommandClassification(t *testing.T) {
	for _, body := range []string{"/new", "@机器人 /new", "/reset", "@机器人 /reset"} {
		for _, kind := range []string{"text", "richText"} {
			t.Run(kind+body, func(t *testing.T) {
				data := botCallbackData{ConversationID: "cid", MsgID: "fresh", SenderStaffID: "sender", ConversationType: "2", Msgtype: kind}
				data.Text.Content = body
				data.Content = richTextContent{RichText: []richTextNode{{Text: body}}}
				msg, ok := inboundFromBotCallback(data, "client")
				if !ok || !msg.ForceFresh || msg.Text != "" {
					t.Fatalf("reset not consumed: %+v", msg)
				}
				rest, fresh := engine.ParseFreshSessionCommand(msg.CommandText)
				if !fresh || rest != "" {
					t.Fatalf("router lost bare reset: %q", msg.CommandText)
				}
			})
		}
	}
}
