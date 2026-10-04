package inboundcoord

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/util"
)

// probeShapedPayload mirrors the 2026-10-03 probe of a real group page read as
// the employee's own DWS identity (dws v1.0.63, im.message-list.v1): no
// senderUid, viewer-relative senderId/senderOpenDingTalkId, sendType instead
// of senderType, Shanghai display times, and a quote without senderId.
const probeShapedPayload = `{
	"contractVersion":"im.message-list.v1","success":true,"hasMore":true,"count":5,
	"messages":[
		{"content":"@员工  谢谢","text":"@员工  谢谢","createTime":"2026-10-03 19:20:44","messageId":"msg-5","openMessageId":"msg-5","sendType":"user","sender":"冬翔","senderId":"open-dx","senderOpenDingTalkId":"open-dx"},
		{"content":"@冬翔  合计 100。","createTime":"2026-10-03 19:20:22","messageId":"msg-4","openMessageId":"msg-4","sendType":"digital_employee","sender":"员工","senderId":"open-self","senderOpenDingTalkId":"open-self",
		 "quotedMessage":{"content":"@6986548921  继续：合计多少？","openMessageId":"msg-3","sendType":"user","sender":"冬翔","senderOpenDingTalkId":"open-dx"}},
		{"content":"回执：E3 已收到；G5 已收到。","createTime":"2026-10-03 18:55:20","messageId":"msg-2","openMessageId":"msg-2","sendType":"user","sender":"主管","senderId":"open-boss","senderOpenDingTalkId":"open-boss"},
		{"content":"","text":"本场候选编号有 E3、G5、Z5。","createTime":"2026-10-03 18:55:09","messageId":"msg-1","sendType":"user","sender":"冬翔","senderId":"open-dx"},
		{"content":"截止之后","createTime":"2026-10-03 19:30:00","openMessageId":"msg-late","sendType":"user","sender":"冬翔"}
	]
}`

type rangeCLI struct {
	goldenCLI
}

func TestLoadRangeReadsAsAgentIdentityWithOwnAuditPurpose(t *testing.T) {
	issuer := &goldenIssuer{}
	cli := &rangeCLI{goldenCLI{payload: probeShapedPayload}}
	loader := &dwsHistoryLoader{issuer: issuer, redeemer: fakeDWSRedeemer{}, cli: cli, mkdir: os.MkdirTemp, remove: os.RemoveAll}
	cutoff := time.Date(2026, 10, 3, 11, 25, 0, 0, time.UTC) // 19:25 Shanghai
	page, err := loader.LoadRange(context.Background(), RangeRequest{AgentID: testAgentID(), UID: "507523443", OrgID: "44675729", ConversationID: " cid-group ", Before: cutoff, Limit: 41})
	if err != nil {
		t.Fatal(err)
	}
	if len(cli.limits) != 1 || cli.limits[0] != 41 || cli.cids[0] != "cid-group" || !cli.before[0].Equal(cutoff) {
		t.Fatalf("range request: limits=%v cids=%q before=%v", cli.limits, cli.cids, cli.before)
	}
	if len(issuer.requests) != 1 {
		t.Fatalf("issue requests=%d", len(issuer.requests))
	}
	req := issuer.requests[0]
	if req.Reason != "Multica employee scene transcript" || req.Source["identity_source"] != "employee_scene_transcript" || !strings.HasPrefix(req.RequestID, "employee-transcript-") || req.UID != "507523443" || req.OrgID != "44675729" || req.RuntimeType != "SERVER" {
		t.Fatalf("range identity request: %#v", req)
	}
	if !page.HasMore || page.RawCount != 5 || len(page.Messages) != 4 {
		t.Fatalf("page=%+v", page)
	}
	first, self := page.Messages[0], page.Messages[2]
	if first.ID != "msg-1" || first.Content != "本场候选编号有 E3、G5、Z5。" || first.SendType != "user" || first.SenderID != "open-dx" ||
		!first.SentAt.Equal(time.Date(2026, 10, 3, 10, 55, 9, 0, time.UTC)) || first.SentAtRaw != "2026-10-03 18:55:09" {
		t.Fatalf("oldest message must come first with text fallback and Shanghai time: %+v", first)
	}
	if self.SendType != "digital_employee" || self.SenderOpenID != "open-self" || self.Quoted == nil || self.Quoted.ID != "msg-3" || self.Quoted.SenderOpenID != "open-dx" || self.Quoted.Content != "@6986548921  继续：合计多少？" {
		t.Fatalf("self line or quote lost: %+v quote=%+v", self, self.Quoted)
	}
	for _, message := range page.Messages {
		if message.ID == "msg-late" {
			t.Fatal("a message at or after the cutoff was returned")
		}
	}
}

func TestParseDWSRangeKeepsFullBodiesAndRedactsBeforeClipping(t *testing.T) {
	long := strings.Repeat("长", 700) // 2100 bytes: beyond the Coordinator's 160 runes
	huge := strings.Repeat("a", DWSRangeContentBytes+10)
	raw := []byte(`{"success":true,"result":{"hasMore":false,"messages":[
		{"content":"` + huge + `","openMessageId":"h","createTime":"1791021480000","sender":"x"},
		{"content":"https://pre-fde-workbench.dingtalk.com/dingtalk/configure?link=secret 看","openMessageId":"c","createTime":"1791021470000","sender":"x"},
		{"content":"` + long + `","openMessageId":"l","createTime":"1791021460000","senderUid":"1001","senderType":"Robot","sender":" 主管  张三 "}
	]}}`)
	page, err := parseDWSRange(raw, time.UnixMilli(1791021490000))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 3 || page.HasMore {
		t.Fatalf("page=%+v", page)
	}
	l, c, h := page.Messages[0], page.Messages[1], page.Messages[2]
	if l.Content != long || l.Truncated || l.SendType != "robot" || l.Sender != "主管 张三" || l.SenderUID != "1001" {
		t.Fatalf("long human body must stay whole: truncated=%v type=%q sender=%q", l.Truncated, l.SendType, l.Sender)
	}
	if strings.Contains(c.Content, "secret") || !strings.Contains(c.Content, ConfigLinkPlaceholder) {
		t.Fatalf("config link not redacted: %q", c.Content)
	}
	if len(h.Content) != DWSRangeContentBytes || !h.Truncated || h.OriginalBytes != len(huge) {
		t.Fatalf("oversized body must clip at %d bytes: len=%d truncated=%v original=%d", DWSRangeContentBytes, len(h.Content), h.Truncated, h.OriginalBytes)
	}
}

func TestLoadRangeRejectsUnboundedOrIncompleteRequestsBeforeIdentityUse(t *testing.T) {
	issuer := &goldenIssuer{}
	loader := &dwsHistoryLoader{issuer: issuer, redeemer: fakeDWSRedeemer{}, cli: &rangeCLI{goldenCLI{payload: probeShapedPayload}}, mkdir: os.MkdirTemp, remove: os.RemoveAll}
	base := RangeRequest{AgentID: testAgentID(), UID: "507523443", OrgID: "44675729", ConversationID: "cid", Before: time.Now(), Limit: 41}
	for name, mutate := range map[string]func(*RangeRequest){
		"zero limit": func(r *RangeRequest) { r.Limit = 0 },
		"over limit": func(r *RangeRequest) { r.Limit = DWSRangeMaxLimit + 1 },
		"no cutoff":  func(r *RangeRequest) { r.Before = time.Time{} },
		"no uid":     func(r *RangeRequest) { r.UID = " " },
		"no org":     func(r *RangeRequest) { r.OrgID = "" },
		"no cid":     func(r *RangeRequest) { r.ConversationID = "" },
	} {
		req := base
		mutate(&req)
		if _, err := loader.LoadRange(context.Background(), req); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	if len(issuer.requests) != 0 {
		t.Fatalf("identity was issued for a rejected request: %d", len(issuer.requests))
	}
}

func TestLoadRangeSharesAuthorizedCrossOrgRenewal(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	body := `{"error":{"server_error_code":"CrossOrgPermissionDenied","category":"api"}}`
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'RESPONSE'\n"+body+"\nRESPONSE\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, denied := (dwsclient.CLI{Path: bin}).List(context.Background(), dir, dwsclient.ListRequest{ConversationID: "cid-test"})
	cli := &renewingRangeCLI{denial: denied, payload: probeShapedPayload}
	loader := &dwsHistoryLoader{issuer: fakeDWSIssuer{}, redeemer: fakeDWSRedeemer{}, cli: cli, mkdir: os.MkdirTemp, remove: os.RemoveAll,
		crossOrgRenewAgentIDs: map[string]bool{util.UUIDToString(testAgentID()): true}}
	page, err := loader.LoadRange(context.Background(), RangeRequest{AgentID: testAgentID(), UID: "24710833", OrgID: "439446171", ConversationID: "cid-test", Before: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), Limit: 41})
	if err != nil || cli.lists != 2 || cli.grants != 1 || len(page.Messages) != 5 {
		t.Fatalf("err=%v lists=%d grants=%d messages=%d", err, cli.lists, cli.grants, len(page.Messages))
	}
}

type renewingRangeCLI struct {
	fakeDWSCLI
	denial        error
	payload       string
	lists, grants int
}

func (c *renewingRangeCLI) ListMessages(context.Context, string, string, time.Time, int) ([]byte, error) {
	c.lists++
	if c.lists == 1 {
		return nil, c.denial
	}
	return []byte(c.payload), nil
}

func (c *renewingRangeCLI) RenewCrossOrgRead(context.Context, string) error {
	c.grants++
	return nil
}
