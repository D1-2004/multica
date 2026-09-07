package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type dwsHistoryStub struct {
	mu      sync.Mutex
	calls   int
	turns   []Turn
	history []HistoryLine
	err     error
}

func (s *dwsHistoryStub) Load(_ context.Context, turn Turn) ([]HistoryLine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.turns = append(s.turns, turn)
	return append([]HistoryLine(nil), s.history...), s.err
}

func decisionLLM(t *testing.T, calls *atomic.Int32, prompt *string, readHistory bool) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role == "user" || message.Role == "tool" {
				*prompt += "\n" + message.Role + ": " + message.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if call == 1 && readHistory {
			_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"history1","type":"function","function":{"name":"context_read","arguments":"{\"kind\":\"history\"}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"cmpl-2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"f1","type":"function","function":{"name":"finish","arguments":"{\"action\":\"reply\",\"text\":\"我在。\",\"reason\":\"本轮只沟通\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	t.Cleanup(server.Close)
	return llm.New(llm.Config{APIKey: "test", BaseURL: server.URL})
}

func TestDecideReadsDWSHistoryOnDemandForRobotAndDigitalEmployee(t *testing.T) {
	for _, source := range []Source{SourceRobot, SourceDigitalEmployee} {
		t.Run(string(source), func(t *testing.T) {
			loader := &dwsHistoryStub{history: []HistoryLine{
				{Role: "须莫", Content: "看看今天的新闻"},
				{Role: "机器人", Content: "我去查一下。"},
			}}
			var calls atomic.Int32
			var prompt string
			c := &Coordinator{
				LLM:        decisionLLM(t, &calls, &prompt, true),
				DWSHistory: loader,
			}
			got := c.Decide(context.Background(), Turn{
				Source:         source,
				Addressed:      true,
				ChatType:       "p2p",
				Message:        "刚才聊了什么",
				AgentID:        testAgentID(),
				ConversationID: "cid-real",
				DWSUID:         "24710833",
				DWSOrgID:       "439446171",
			})
			if got.Action != ActionReply || calls.Load() != 2 || loader.calls != 1 {
				t.Fatalf("decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
			}
			if !strings.Contains(prompt, `"status":"loaded"`) || !strings.Contains(prompt, "看看今天的新闻") {
				t.Fatalf("DWS history missing from prompt: %q", prompt)
			}
			if len(got.Steps) < 2 || got.Steps[0].Tool != "context_read" || got.Steps[0].Type != "tool_use" || got.Steps[1].Type != "tool_result" {
				t.Fatalf("DWS timeline missing: %#v", got.Steps)
			}
			if !strings.Contains(got.Steps[1].Output, "看看今天的新闻") {
				t.Fatalf("DWS timeline omitted loaded content: %#v", got.Steps[1])
			}
			if len(loader.turns) != 1 || loader.turns[0].HistoryBefore.IsZero() {
				t.Fatalf("on-demand history must receive the fixed window cutoff: %#v", loader.turns)
			}
		})
	}
}

func TestDecideDWSHistoryTimeoutStillRunsLLM(t *testing.T) {
	loader := &dwsHistoryStub{err: context.DeadlineExceeded}
	var calls atomic.Int32
	var prompt string
	c := &Coordinator{LLM: decisionLLM(t, &calls, &prompt, true), DWSHistory: loader}
	got := c.Decide(context.Background(), Turn{
		Source:            SourceDigitalEmployee,
		Addressed:         true,
		ChatType:          "group",
		ConversationTitle: "OwnerGraph",
		Message:           "刚才口径是什么",
		AgentID:           testAgentID(),
		ConversationID:    "cid-ownergraph",
		DWSUID:            "24710833",
		DWSOrgID:          "439446171",
	})
	if got.Action == ActionContinue || calls.Load() != 2 || loader.calls != 1 {
		t.Fatalf("timeout must still judge: decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
	}
	if !strings.Contains(prompt, `"status":"unavailable"`) {
		t.Fatalf("timeout must be explicit, not empty history: %q", prompt)
	}
}

func TestDecideDWSHistoryFailureStillRunsLLM(t *testing.T) {
	loader := &dwsHistoryStub{err: errors.New("read failed")}
	var calls atomic.Int32
	var prompt string
	c := &Coordinator{LLM: decisionLLM(t, &calls, &prompt, true), DWSHistory: loader}
	got := c.Decide(context.Background(), Turn{
		Source:         SourceRobot,
		Addressed:      true,
		ChatType:       "p2p",
		Message:        "刚才聊了什么",
		AgentID:        testAgentID(),
		ConversationID: "cid-real",
		DWSUID:         "24710833",
		DWSOrgID:       "439446171",
	})
	if got.Action == ActionContinue || calls.Load() != 2 || loader.calls != 1 {
		t.Fatalf("read_failed must still judge: decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
	}
	if !strings.Contains(prompt, `"status":"unavailable"`) {
		t.Fatalf("failure must be explicit, not empty history: %q", prompt)
	}
	if len(got.Steps) < 2 || got.Steps[1].Tool != "context_read" || !strings.Contains(got.Steps[1].Output, `"status":"unavailable"`) {
		t.Fatalf("DWS failure timeline=%#v", got.Steps)
	}
}

func TestDecideWebDoesNotLoadDWSHistory(t *testing.T) {
	loader := &dwsHistoryStub{err: errors.New("must not be called")}
	var calls atomic.Int32
	var prompt string
	c := &Coordinator{LLM: decisionLLM(t, &calls, &prompt, false), DWSHistory: loader}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionReply || calls.Load() != 1 || loader.calls != 0 {
		t.Fatalf("decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
	}
}

func TestDecideSelfContainedChannelReplyDoesNotLoadDWSHistory(t *testing.T) {
	for _, source := range []Source{SourceRobot, SourceDigitalEmployee} {
		t.Run(string(source), func(t *testing.T) {
			loader := &dwsHistoryStub{err: errors.New("must not be called")}
			var calls atomic.Int32
			var prompt string
			c := &Coordinator{LLM: decisionLLM(t, &calls, &prompt, false), DWSHistory: loader}
			got := c.Decide(context.Background(), Turn{
				Source: source, Addressed: true, ChatType: "p2p", Message: "你好", ConversationID: "cid-current",
			})
			if got.Action != ActionReply || calls.Load() != 1 || loader.calls != 0 {
				t.Fatalf("self-contained reply should not fetch unrelated history: decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
			}
		})
	}
}

type fakeDWSIssuer struct{}

func (fakeDWSIssuer) CreateContext(_ context.Context, req agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error) {
	if req.UID == "" || req.OrgID == "" || req.TTLSeconds != dwsHistoryContextTTLSeconds {
		return agentidentityhsf.CreateContextResult{}, errors.New("invalid issue request")
	}
	return agentidentityhsf.CreateContextResult{ContextToken: "context-token", ExpiresAt: 4102444800000}, nil
}

type fakeDWSRedeemer struct{}

func (fakeDWSRedeemer) Redeem(_ context.Context, token string) (dwsCredential, error) {
	if token != "context-token" {
		return dwsCredential{}, errors.New("unexpected token")
	}
	return dwsCredential{UID: "24710833", ClientID: "client-id", AuthCode: "auth-code"}, nil
}

type fakeDWSCLI struct {
	mu     sync.Mutex
	dirs   []string
	before []time.Time
}

func (f *fakeDWSCLI) Exchange(_ context.Context, dir string, credential dwsCredential) error {
	if credential.UID == "" || credential.ClientID == "" || credential.AuthCode == "" {
		return errors.New("missing credential")
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		return errors.New("DWS directory is not private")
	}
	f.mu.Lock()
	f.dirs = append(f.dirs, dir)
	f.mu.Unlock()
	return nil
}

func (f *fakeDWSCLI) ListMessages(_ context.Context, _ string, conversationID string, before time.Time, limit int) ([]byte, error) {
	if conversationID == "" || limit != dwsHistoryQueryLimit || before.IsZero() {
		return nil, errors.New("unexpected history query")
	}
	f.mu.Lock()
	f.before = append(f.before, before)
	f.mu.Unlock()
	return []byte(`{
		"success":true,
		"result":{"messages":[
			{"content":"当前句","openMessageId":"current","sender":"须莫"},
			{"content":"消息J","openMessageId":"j","sender":"机器人"},
			{"content":"消息I","openMessageId":"i","sender":"须莫"},
			{"content":"消息H","openMessageId":"h","sender":"机器人"},
			{"content":"消息G","openMessageId":"g","sender":"须莫"},
			{"content":"消息F","openMessageId":"f","sender":"机器人"},
			{"content":"消息E","openMessageId":"e","sender":"须莫"},
			{"content":"消息D","openMessageId":"d","sender":"机器人"},
			{"content":"消息C","openMessageId":"c","sender":"须莫"},
			{"content":"消息B","openMessageId":"b","sender":"机器人"},
			{"content":"消息A","openMessageId":"a","sender":"须莫"}
		]}
	}`), nil
}

func TestDWSHistoryLoaderIsolatesConcurrentCallsAndKeepsLatestTen(t *testing.T) {
	cli := &fakeDWSCLI{}
	loader := &dwsHistoryLoader{
		issuer:   fakeDWSIssuer{},
		redeemer: fakeDWSRedeemer{},
		cli:      cli,
		mkdir:    os.MkdirTemp,
		remove:   os.RemoveAll,
	}
	turn := Turn{
		Source:         SourceRobot,
		AgentID:        testAgentID(),
		ConversationID: "cid-real",
		DWSUID:         "24710833",
		DWSOrgID:       "439446171",
		EvidenceID:     "current",
		HistoryBefore:  time.Date(2026, 9, 7, 11, 14, 1, 0, time.UTC),
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			history, err := loader.Load(context.Background(), turn)
			if err == nil && (len(history) != 10 || history[0].Content != "消息A" || history[9].Content != "消息J") {
				err = errors.New("history order or length is wrong")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cli.mu.Lock()
	dirs := append([]string(nil), cli.dirs...)
	cutoffs := append([]time.Time(nil), cli.before...)
	cli.mu.Unlock()
	if len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatalf("DWS config dirs = %v", dirs)
	}
	if len(cutoffs) != 2 || !cutoffs[0].Equal(turn.HistoryBefore) || !cutoffs[1].Equal(turn.HistoryBefore) {
		t.Fatalf("repeated reads must use the same sealed-window cutoff: %v", cutoffs)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("DWS config dir was not removed: %s", dir)
		}
	}
}

func TestParseDWSHistoryAcceptsTopLevelMessages(t *testing.T) {
	raw := []byte(`{
		"success":true,
		"messages":[
			{"content":"上一句","openMessageId":"prev","sender":"冬翔"}
		]
	}`)
	history, err := parseDWSHistory(raw, Turn{EvidenceID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "上一句" || history[0].EvidenceID != "prev" {
		t.Fatalf("history=%#v", history)
	}
}

func TestParseDWSHistoryRejectedIncludesErrorMsg(t *testing.T) {
	_, err := parseDWSHistory([]byte(`{
		"success":false,
		"errorCode":null,
		"errorMsg":"无权限查看会话"
	}`), Turn{EvidenceID: "current"})
	if err == nil {
		t.Fatal("rejected envelope must error")
	}
	got := err.Error()
	if !strings.Contains(got, "operation_failed") || !strings.Contains(got, "无权限查看会话") {
		t.Fatalf("got %q", got)
	}
}

func TestParseDWSHistoryKeepsMessagesWhenSuccessFalse(t *testing.T) {
	history, err := parseDWSHistory([]byte(`{
		"success":false,
		"errorCode":null,
		"messages":[{"content":"仍可用","openMessageId":"m1","sender":"冬翔"}]
	}`), Turn{EvidenceID: "current"})
	if err != nil || len(history) != 1 || history[0].Content != "仍可用" {
		t.Fatalf("history=%#v err=%v", history, err)
	}
}

func TestParseDWSHistoryIncludesQuotedMessage(t *testing.T) {
	raw := []byte(`{
		"success":true,
		"result":{"messages":[{
			"content":"那就按这个方案",
			"openMessageId":"reply",
			"sender":"冬翔",
			"quotedMessage":{
				"content":"周五先发预发，验证通过后再上线",
				"openMessageId":"quoted",
				"sender":"须莫"
			}
		}]}
	}`)

	history, err := parseDWSHistory(raw, Turn{EvidenceID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d", len(history))
	}
	if got := history[0].Content; got != "那就按这个方案\n  引用消息（须莫）：周五先发预发，验证通过后再上线" {
		t.Fatalf("history content = %q", got)
	}
	if history[0].EvidenceID != "reply" {
		t.Fatalf("history evidence = %q", history[0].EvidenceID)
	}
	if history[0].ReplyToEvidenceID != "quoted" {
		t.Fatalf("quoted evidence = %q", history[0].ReplyToEvidenceID)
	}
}

func TestParseDWSHistoryFreezesWatermarkAndExcludesWholeWindow(t *testing.T) {
	cutoff := time.Date(2026, 9, 7, 11, 14, 1, 0, time.UTC)
	turn := Turn{
		EvidenceID: "current", HistoryBefore: cutoff,
		Utterances: []WindowUtterance{{EvidenceID: "first-window-line"}, {EvidenceID: "current"}},
	}
	raw := []byte(`{"success":true,"messages":[
		{"content":"后来的同意不能倒灌","openMessageId":"future","createTime":"2026-09-07T11:15:00Z"},
		{"content":"上界本身不属于更早历史","openMessageId":"boundary","createTime":"2026-09-07T11:14:01Z"},
		{"content":"当前句","openMessageId":"current","createTime":"2026-09-07T11:14:00Z"},
		{"content":"本窗第一句","openMessageId":"first-window-line","createTime":"2026-09-07T11:13:59Z"},
		{"content":"需要按刚才这版发吗？","openMessageId":"question","sender":"数字员工","senderUid":"agent-uid","createTime":"2026-09-07T11:13:58Z"}
	]}`)
	history, err := parseDWSHistory(raw, turn)
	if err != nil || len(history) != 1 || history[0].EvidenceID != "question" {
		t.Fatalf("window and future messages leaked into history: history=%#v err=%v", history, err)
	}
	if history[0].SenderID != "agent-uid" || !history[0].Timestamp.Equal(cutoff.Add(-3*time.Second)) {
		t.Fatalf("trusted history origin was lost: %#v", history[0])
	}
}

func TestParseDWSHistoryPreservesOriginWithoutInventingMissingMetadata(t *testing.T) {
	history, err := parseDWSHistory([]byte(`{"success":true,"messages":[
		{"content":"行","openMessageId":"reply","sender":"同名","senderId":"uid-b","createTime":1788779639000,
		 "quotedMessage":{"openMessageId":"question","sender":"同名","senderUid":"uid-a","content":"周五三点可以吗？"}},
		{"content":"没有来源信息","sender":"同名","createTime":"2026-09-07 19:13:58"},
		{"content":"没有时间"}
	]}`), Turn{})
	if err != nil || len(history) != 3 {
		t.Fatalf("history=%#v err=%v", history, err)
	}
	reply := history[2]
	if reply.SenderID != "uid-b" || reply.ReplyToSenderID != "uid-a" || reply.ReplyToEvidenceID != "question" || reply.TimestampRaw != "1788779639000" || reply.Timestamp.IsZero() {
		t.Fatalf("explicit source identities must stay distinct: %#v", reply)
	}
	unknownZone := history[1]
	if !unknownZone.Timestamp.IsZero() || unknownZone.TimestampRaw != "2026-09-07 19:13:58" || unknownZone.EvidenceID != "" || unknownZone.SenderID != "" {
		t.Fatalf("unknown timestamp or sender identity was manufactured: %#v", unknownZone)
	}
	missing := history[0]
	if !missing.Timestamp.IsZero() || missing.TimestampRaw != "" || missing.EvidenceID != "" || missing.SenderID != "" {
		t.Fatalf("absent metadata must remain absent: %#v", missing)
	}
}

func TestParseDWSHistoryMarksClippedEvidence(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"success": true, "messages": []map[string]any{
		{"content": strings.Repeat("甲", 161), "openMessageId": "long"},
		{"content": "看这条", "quotedMessage": map[string]any{"content": strings.Repeat("乙", 161)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	history, err := parseDWSHistory(raw, Turn{})
	if err != nil || len(history) != 2 || !history[0].ContentTruncated || !history[1].ContentTruncated {
		t.Fatalf("clipped evidence must be explicitly marked: history=%#v err=%v", history, err)
	}
}

func TestParseDWSHistoryDistinguishesEmptyFromUnavailable(t *testing.T) {
	for _, raw := range []string{
		`{"success":true}`,
		`{"success":true,"result":{}}`,
		`{"success":true,"messages":null}`,
		`{"success":true,"messages":"unavailable"}`,
		`{"success":false,"errorCode":"denied","errorMsg":"不能读取"}`,
	} {
		if _, err := parseDWSHistory([]byte(raw), Turn{}); err == nil {
			t.Fatalf("unavailable history must not masquerade as an empty chat: %s", raw)
		}
	}
	for _, raw := range []string{`{"success":true,"messages":[]}`, `{"success":true,"result":{"messages":[]}}`} {
		if history, err := parseDWSHistory([]byte(raw), Turn{}); err != nil || len(history) != 0 {
			t.Fatalf("explicitly empty history must remain valid: history=%#v err=%v", history, err)
		}
	}
}

func TestDWSHistoryLoaderRejectsMissingCutoffBeforeIdentityUse(t *testing.T) {
	cli := &fakeDWSCLI{}
	loader := &dwsHistoryLoader{issuer: fakeDWSIssuer{}, redeemer: fakeDWSRedeemer{}, cli: cli}
	_, err := loader.Load(context.Background(), Turn{
		AgentID: testAgentID(), ConversationID: "cid", DWSUID: "uid", DWSOrgID: "org",
	})
	if err == nil || !strings.Contains(err.Error(), "fixed window cutoff") || len(cli.dirs) != 0 {
		t.Fatalf("missing cutoff must fail before credential or filesystem work: err=%v dirs=%v", err, cli.dirs)
	}
}

func TestHTTPDWSCredentialRedeemerValidatesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer context-token" {
			t.Fatal("missing bearer token")
		}
		_, _ = io.WriteString(w, `{
			"ok":true,
			"identity":{"key":"dws","type":"DWS_UID","uid":"24710833","clientId":"client-id"},
			"credential":{"type":"DWS_AUTH_CODE","authCode":"auth-code"}
		}`)
	}))
	defer server.Close()
	redeemer := &httpDWSCredentialRedeemer{baseURL: server.URL, client: server.Client()}
	credential, err := redeemer.Redeem(context.Background(), "context-token")
	if err != nil || credential.UID != "24710833" || credential.ClientID != "client-id" || credential.AuthCode != "auth-code" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}
