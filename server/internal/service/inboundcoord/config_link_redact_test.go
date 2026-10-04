package inboundcoord

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRedactConfigLinksPlainAndEncoded(t *testing.T) {
	deepLink := "dingtalk://dingtalkclient/page/link?url=" + url.QueryEscape(testConfigLinkURL) + "&pc_slide=true"
	cases := map[string]struct {
		in, want string
	}{
		"plain URL": {in: "配置链接：" + testConfigLinkURL + " 请尽快打开", want: "配置链接：[configuration link] 请尽快打开"},
		"path only": {in: "open /dingtalk/configure?link=AbC-_9%3D", want: "open [configuration link]"},
		// The whole deep link goes, so a redacted Markdown link has no
		// live-looking target.
		"DingTalk deep link": {in: deepLink, want: "[configuration link]"},
		"Markdown link":      {in: "[本群能力配置](" + deepLink + ")（30 分钟内有效）", want: "[本群能力配置]([configuration link])（30 分钟内有效）"},
		"deep link with a tab": {
			in:   ConfigLinkDeepLink(testConfigLinkURL+"&tab=routines") + " ok",
			want: "[configuration link] ok",
		},
		"JSON-escaped ampersand": {
			in:   strings.ReplaceAll(deepLink, "&", `\u0026`),
			want: "[configuration link]",
		},
		"double-encoded": {
			in:   "see " + url.QueryEscape(url.QueryEscape(testConfigLinkURL)),
			want: "see [configuration link]",
		},
		"partly encoded": {
			in:   "see https%3A%2F%2Fapp.multica.example%2Fdingtalk%2Fconfigure%3Flink=" + testConfigLinkToken,
			want: "see [configuration link]",
		},
		"tool result JSON": {
			in:   `{"url":"` + testConfigLinkURL + `","dingtalk_url":"` + deepLink + `","scope":"person"}`,
			want: `{"url":"[configuration link]","dingtalk_url":"[configuration link]","scope":"person"}`,
		},
		"no link":             {in: "configure the group in 设置", want: "configure the group in 设置"},
		"other configure URL": {in: "https://example.com/configure?x=1", want: "https://example.com/configure?x=1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := RedactConfigLinks(tc.in); got != tc.want {
				t.Fatalf("RedactConfigLinks(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// The JSON a tool result is stored as stays valid after redaction.
	raw := `{"url":"` + testConfigLinkURL + `","dingtalk_url":"` + deepLink + `"}`
	var decoded map[string]string
	if err := json.Unmarshal([]byte(RedactConfigLinks(raw)), &decoded); err != nil {
		t.Fatalf("redacted JSON does not parse: %v", err)
	}
}

func TestDecisionWithoutConfigLinksKeepsTheOriginal(t *testing.T) {
	reply := testCapabilityReply + "\n\n本群能力配置（30 分钟内有效）：" + testConfigLinkURL
	d := Decision{
		Action:              ActionReply,
		UserText:            reply,
		CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", Reply: reply}},
		configLinkURL:       testConfigLinkURL,
	}
	stored := d.WithoutConfigLinks()
	if strings.Contains(stored.UserText, "configure?link=") || strings.Contains(stored.CoordinationActions[0].Reply, "configure?link=") {
		t.Fatalf("stored copy keeps the link: %+v", stored)
	}
	if !strings.HasSuffix(stored.UserText, ConfigLinkPlaceholder) {
		t.Fatalf("stored text = %q", stored.UserText)
	}
	if d.UserText != reply || d.CoordinationActions[0].Reply != reply {
		t.Fatal("the delivered decision must keep its link")
	}
}

func TestIssueDescriptionNeverCarriesTheConfigLink(t *testing.T) {
	d := Decision{Purpose: "整理日报", UserText: "好的，我来整理。\n\n你的个人能力配置（15 分钟内有效，限用一次）：" + testConfigLinkURL}
	description := IssueDescription(d, "帮我整理日报，顺便告诉我你能做什么")
	if strings.Contains(description, "configure?link=") {
		t.Fatalf("issue description leaks the link: %s", description)
	}
	if !strings.Contains(description, "本轮拟向用户说明：好的，我来整理。") {
		t.Fatalf("issue description lost the reception text: %s", description)
	}
}

func TestParseDWSHistoryRedactsConfigLinks(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"success": true,
		"result": map[string]any{"messages": []map[string]any{{
			"content":       "打开 " + testConfigLinkURL,
			"openMessageId": "reply",
			"sender":        "冬翔",
			"quotedMessage": map[string]any{
				"content":       "本群能力配置（30 分钟内有效）：" + testConfigLinkURL,
				"openMessageId": "quoted",
				"sender":        "数字员工",
			},
		}}},
	})
	history, err := parseDWSHistory(raw, Turn{EvidenceID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d", len(history))
	}
	want := "打开 [configuration link]\n  引用消息（数字员工）：本群能力配置（30 分钟内有效）：[configuration link]"
	if history[0].Content != want {
		t.Fatalf("history content = %q, want %q", history[0].Content, want)
	}
}

func TestFirstRoundShadowMatchesTheClaimWhenConfigLinksAreOn(t *testing.T) {
	logs := captureLogs(t)
	loader := sameHistory(2)
	c, recorder := shadowCoordinator(t, loader)
	c.ConfigLinks = &configLinkIssuerStub{link: dmConfigLink()}
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	turn := shadowTurn(cutoff)
	turn.WorkspaceID = "11111111-1111-1111-1111-111111111111"
	turn.IssueDispatchContext = []byte(`{"dispatch_event_data":{"conversation":{"openConversationId":"cid-real","type":"p2p"}}}`)
	if !c.configLinkEligible(turn) {
		t.Fatal("fixture turn must be eligible for a configuration link")
	}
	entry := builtShadow(t, c, loader, turn)
	if entry.skipped != "" || entry.request.full == "" {
		t.Fatalf("the shadow must build the request: skipped=%q", entry.skipped)
	}
	c.Decide(claimContext(), turn)
	routing := recorder.routing()
	if len(routing) == 0 {
		t.Fatal("the claimed decision must send its first request")
	}
	if !strings.Contains(string(routing[0]), "Host appends this chat's capability configuration link") {
		t.Fatal("the claim's finish schema must announce the Host-appended link")
	}
	var wire map[string]any
	if err := json.Unmarshal(routing[0], &wire); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(wire)
	if hashBytes(canonical) != entry.request.full {
		t.Fatalf("the shadow must equal the first request on the wire:\nshadow %s\nwire   %s", entry.request.full, hashBytes(canonical))
	}
	if out := logs.String(); !strings.Contains(out, "outcome=same") || !strings.Contains(out, "unexplained=false") {
		t.Fatalf("the claim must report the same request: %s", out)
	}
}
