package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
)

// goldenHistoryPayload exercises every branch of parseDWSHistory: nested and
// contract timestamps, cutoff, window exclusion, clipping, quotes, redaction
// and the ten-line cap.
var goldenHistoryPayload = `{
	"contractVersion":"im.message-list.v1",
	"success":true,
	"result":{"messages":[
		{"content":"窗口内的当前句","openMessageId":"current","sender":"冬翔","createTime":"2026-10-03 18:59:59"},
		{"content":"截止之后的消息","openMessageId":"late","sender":"冬翔","createTime":"2026-10-03 19:00:01"},
		{"content":"看这里 https://pre-fde-workbench.dingtalk.com/dingtalk/configure?link=secret-token 配置","openMessageId":"m12","sender":"Qwen-Real","senderOpenDingTalkId":"self-open","sendType":"digital_employee","createTime":"2026-10-03 18:58:00"},
		{"content":"` + goldenLongRunes + `","openMessageId":"m11","sender":"  主管   张三 ","senderUid":"1001","createTime":"1791021480000"},
		{"content":"回执：K6 已收到","openMessageId":"m10","sender":"李四","senderId":"open-lisi","createTime":"2026-10-03T10:56:00Z",
		 "quotedMessage":{"content":"` + goldenLongRunes + `","sender":"","senderUid":"1001","openMessageId":"m09"}},
		{"content":"   ","openMessageId":"blank","sender":"李四","createTime":"2026-10-03 18:55:00"},
		{"content":"本场候选编号有 K6、X3、Z2。","openMessageId":"m09","sender":"","senderUid":"1001","createTime":"1791021240"},
		{"content":"中午吃什么？","openMessageId":"m08","sender":"王五","senderId":"open-wangwu","createTime":"2026-10-03 18:53:00"},
		{"content":"消息七","openMessageId":"m07","sender":"王五","createTime":"display-only"},
		{"content":"消息六","openMessageId":"m06","sender":"王五","createTime":null},
		{"content":"消息五","openMessageId":"m05","sender":"王五","createTime":"2026-10-03 18:50:00"},
		{"content":"消息四","openMessageId":"m04","sender":"王五","createTime":"2026-10-03 18:49:00"},
		{"content":"消息三","openMessageId":"m03","sender":"王五","createTime":"2026-10-03 18:48:00"},
		{"content":"消息二（超出十条上限）","openMessageId":"m02","sender":"王五","createTime":"2026-10-03 18:47:00"}
	]}
}`

var goldenLongRunes = strings.Repeat("长", 170)

// goldenHistoryLoad is the Coordinator Load output captured before LoadRange
// was extracted. Any byte change here is a Coordinator behavior change.
const goldenHistoryLoad = `[{"Role":"王五","Content":"消息三","EvidenceID":"m03","Timestamp":"2026-10-03T18:48:00+08:00","TimestampRaw":"2026-10-03 18:48:00","SenderID":"","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"王五","Content":"消息四","EvidenceID":"m04","Timestamp":"2026-10-03T18:49:00+08:00","TimestampRaw":"2026-10-03 18:49:00","SenderID":"","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"王五","Content":"消息五","EvidenceID":"m05","Timestamp":"2026-10-03T18:50:00+08:00","TimestampRaw":"2026-10-03 18:50:00","SenderID":"","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"王五","Content":"消息六","EvidenceID":"m06","Timestamp":"0001-01-01T00:00:00Z","TimestampRaw":"","SenderID":"","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"王五","Content":"消息七","EvidenceID":"m07","Timestamp":"0001-01-01T00:00:00Z","TimestampRaw":"display-only","SenderID":"","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"王五","Content":"中午吃什么？","EvidenceID":"m08","Timestamp":"2026-10-03T18:53:00+08:00","TimestampRaw":"2026-10-03 18:53:00","SenderID":"open-wangwu","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"dingtalk","Content":"本场候选编号有 K6、X3、Z2。","EvidenceID":"m09","Timestamp":"2026-10-03T09:54:00Z","TimestampRaw":"1791021240","SenderID":"1001","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false},{"Role":"李四","Content":"回执：K6 已收到\n  引用消息（dingtalk）：长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长","EvidenceID":"m10","Timestamp":"2026-10-03T10:56:00Z","TimestampRaw":"2026-10-03T10:56:00Z","SenderID":"open-lisi","ReplyToEvidenceID":"m09","ReplyToSenderID":"1001","ContentTruncated":true},{"Role":"主管 张三","Content":"长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长长","EvidenceID":"m11","Timestamp":"2026-10-03T09:58:00Z","TimestampRaw":"1791021480000","SenderID":"1001","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":true},{"Role":"Qwen-Real","Content":"看这里 [configuration link] 配置","EvidenceID":"m12","Timestamp":"2026-10-03T18:58:00+08:00","TimestampRaw":"2026-10-03 18:58:00","SenderID":"","ReplyToEvidenceID":"","ReplyToSenderID":"","ContentTruncated":false}]`

type goldenIssuer struct {
	mu       sync.Mutex
	requests []agentidentityhsf.CreateContextRequest
}

func (g *goldenIssuer) CreateContext(_ context.Context, req agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error) {
	g.mu.Lock()
	g.requests = append(g.requests, req)
	g.mu.Unlock()
	return agentidentityhsf.CreateContextResult{ContextToken: "context-token", ExpiresAt: 4102444800000}, nil
}

type goldenCLI struct {
	fakeDWSCLI
	payload string
	limits  []int
	cids    []string
}

func (c *goldenCLI) ListMessages(_ context.Context, _ string, conversationID string, before time.Time, limit int) ([]byte, error) {
	if before.IsZero() {
		return nil, errors.New("missing cutoff")
	}
	c.mu.Lock()
	c.limits = append(c.limits, limit)
	c.cids = append(c.cids, conversationID)
	c.before = append(c.before, before)
	c.mu.Unlock()
	return []byte(c.payload), nil
}

func TestCoordinatorHistoryLoadUnchangedGolden(t *testing.T) {
	issuer := &goldenIssuer{}
	cli := &goldenCLI{payload: goldenHistoryPayload}
	loader := &dwsHistoryLoader{issuer: issuer, redeemer: fakeDWSRedeemer{}, cli: cli, mkdir: os.MkdirTemp, remove: os.RemoveAll}
	cutoff := time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)
	history, err := loader.Load(context.Background(), Turn{
		Source: SourceDigitalEmployee, AgentID: testAgentID(), SceneID: testSceneID("cid-golden"), ConversationID: " cid-golden ",
		DWSUID: "507523443", DWSOrgID: "44675729", EvidenceID: "current", HistoryBefore: cutoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != goldenHistoryLoad {
		t.Fatalf("Coordinator history output changed:\n got: %s\nwant: %s", raw, goldenHistoryLoad)
	}
	if len(cli.limits) != 1 || cli.limits[0] != dingtalkHistoryLimit+1 || cli.cids[0] != "cid-golden" || !cli.before[0].Equal(cutoff) {
		t.Fatalf("Coordinator list request changed: limits=%v cids=%q before=%v", cli.limits, cli.cids, cli.before)
	}
	if len(issuer.requests) != 1 {
		t.Fatalf("issue requests=%d", len(issuer.requests))
	}
	req := issuer.requests[0]
	if req.RuntimeType != "SERVER" || req.Reason != "Multica inbound coordinator DingTalk history" || req.TTLSeconds != 120 ||
		req.Source["app"] != "dt-fde-multica" || req.Source["identity_source"] != "inbound_dws_history" || len(req.Source) != 2 ||
		!strings.HasPrefix(req.RequestID, "inbound-dws-") || req.TaskID != req.RequestID || req.RuntimeID != req.RequestID ||
		req.UID != "507523443" || req.OrgID != "44675729" {
		t.Fatalf("Coordinator identity request changed: %#v", req)
	}
}
