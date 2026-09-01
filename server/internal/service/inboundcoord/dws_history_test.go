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

func decisionLLM(t *testing.T, calls *atomic.Int32, prompt *string) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role == "user" {
				*prompt = message.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"r1","type":"function","function":{"name":"assoc_recall","arguments":"{}"}},{"id":"f1","type":"function","function":{"name":"finish","arguments":"{\"action\":\"reply\",\"text\":\"刚才在聊新闻。\",\"look_into\":\"\",\"reason\":\"直接回答近期对话\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	t.Cleanup(server.Close)
	return llm.New(llm.Config{APIKey: "test", BaseURL: server.URL})
}

func TestDecideLoadsDWSHistoryForRobotAndDigitalEmployee(t *testing.T) {
	for _, source := range []Source{SourceRobot, SourceDigitalEmployee} {
		t.Run(string(source), func(t *testing.T) {
			loader := &dwsHistoryStub{history: []HistoryLine{
				{Role: "须莫", Content: "看看今天的新闻"},
				{Role: "机器人", Content: "我去查一下。"},
			}}
			var calls atomic.Int32
			var prompt string
			c := &Coordinator{
				LLM:        decisionLLM(t, &calls, &prompt),
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
			if got.Action != ActionReply || calls.Load() != 1 || loader.calls != 1 {
				t.Fatalf("decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
			}
			if !strings.Contains(prompt, "recent_dingtalk_history:") || !strings.Contains(prompt, "看看今天的新闻") {
				t.Fatalf("DWS history missing from prompt: %q", prompt)
			}
		})
	}
}

func TestDecideDWSHistoryFailureContinuesWithoutLLM(t *testing.T) {
	loader := &dwsHistoryStub{err: errors.New("read failed")}
	var calls atomic.Int32
	var prompt string
	c := &Coordinator{LLM: decisionLLM(t, &calls, &prompt), DWSHistory: loader}
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
	if got.Action != ActionContinue || calls.Load() != 0 || loader.calls != 1 {
		t.Fatalf("decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
	}
}

func TestDecideWebDoesNotLoadDWSHistory(t *testing.T) {
	loader := &dwsHistoryStub{err: errors.New("must not be called")}
	var calls atomic.Int32
	var prompt string
	c := &Coordinator{LLM: decisionLLM(t, &calls, &prompt), DWSHistory: loader}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionReply || calls.Load() != 1 || loader.calls != 0 {
		t.Fatalf("decision=%+v llm_calls=%d history_calls=%d", got, calls.Load(), loader.calls)
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
	mu   sync.Mutex
	dirs []string
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

func (f *fakeDWSCLI) ListMessages(_ context.Context, _ string, conversationID string, limit int) ([]byte, error) {
	if conversationID == "" || limit != dwsHistoryQueryLimit {
		return nil, errors.New("unexpected history query")
	}
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
	cli.mu.Unlock()
	if len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatalf("DWS config dirs = %v", dirs)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("DWS config dir was not removed: %s", dir)
		}
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
