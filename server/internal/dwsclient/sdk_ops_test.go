package dwsclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/userdecision"
	"github.com/multica-ai/multica/server/pkg/dws"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// fakeDWS is the DWS gateway and DingTalk's token endpoint: every tool call
// is recorded and answered by reply (tool name → tool text payload).
type fakeDWS struct {
	mu     sync.Mutex
	calls  []fakeToolCall
	tokens []map[string]string
	reply  func(tool string, args map[string]any) string
}

type fakeToolCall struct {
	Tool   string
	Args   map[string]any
	Header http.Header
}

func (f *fakeDWS) toolCalls(tool string) []fakeToolCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeToolCall
	for _, c := range f.calls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// startSDK points the SDK transport at a fake DWS and exchanges one
// directory through it.
func startSDK(t *testing.T, reply func(tool string, args map[string]any) string) (*fakeDWS, CLI, string) {
	t.Helper()
	f := &fakeDWS{reply: reply}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.tokens = append(f.tokens, body)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"accessToken":"uat-1","refreshToken":"rt-1","expireIn":7200,"corpId":"corp-1"}`))
	})
	mux.HandleFunc("/server/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls = append(f.calls, fakeToolCall{Tool: req.Params.Name, Args: req.Params.Arguments, Header: r.Header.Clone()})
		f.mu.Unlock()
		text := f.reply(req.Params.Name, req.Params.Arguments)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sdkTestConfig = func(cfg *dws.Config) { cfg.GatewayURL, cfg.TokenURL = srv.URL, srv.URL+"/token" }
	SetSDKSelector(func() bool { return true })
	pools.Clear() // pools remember their endpoints: each test has its own server
	t.Cleanup(func() { sdkTestConfig = nil; SetSDKSelector(nil); pools.Clear() })

	cli := CLI{ClientSecret: "secret", Environment: "staging"}
	dir := t.TempDir()
	if err := cli.Exchange(context.Background(), dir, Credential{UID: "42", ClientID: "client-1", AuthCode: "code-1"}); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	return f, cli, dir
}

func toolOK(result string) string { return `{"success":true,"result":` + result + `}` }

func TestSDKExchangeKeepsTheTokenOutOfTheDirectory(t *testing.T) {
	f, _, dir := startSDK(t, func(string, map[string]any) string { return toolOK(`{}`) })
	if len(f.tokens) != 1 || f.tokens[0]["clientSecret"] != "secret" || f.tokens[0]["code"] != "code-1" || f.tokens[0]["clientId"] != "client-1" {
		t.Fatalf("token request = %v", f.tokens)
	}
	raw, err := os.ReadFile(filepath.Join(dir, sdkSessionFile))
	if err != nil || strings.Contains(string(raw), "uat-1") || strings.Contains(string(raw), "rt-1") {
		t.Fatalf("session file = %s, %v", raw, err)
	}
	session, ok, err := readSDKSession(dir)
	if !ok || err != nil || session.Gateway != "https://pre-mcp-gw.dingtalk.com" || session.CorpID != "corp-1" || session.UID != "42" {
		t.Fatalf("session = %+v %v %v", session, ok, err)
	}
	// With the switch off a new directory stays with the CLI.
	SetSDKSelector(func() bool { return false })
	other := t.TempDir()
	if _, ok, _ := readSDKSession(other); ok {
		t.Fatal("an unexchanged directory reads as SDK")
	}
}

func TestSDKDecisionOrganizationComesFromTheExchange(t *testing.T) {
	f, cli, dir := startSDK(t, func(string, map[string]any) string { return toolOK(`{}`) })
	corp, err := cli.DecisionOrganization(context.Background(), dir)
	if err != nil || corp != "corp-1" || len(f.calls) != 0 {
		t.Fatalf("corp = %q, %v, calls %v", corp, err, f.calls)
	}
}

func TestSDKListReadsLikeTheCLI(t *testing.T) {
	f, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
		return `{"messages":[{"openMessageId":"m1","openConversationId":"cid","senderOpenDingTalkId":"D1","sender":"张三","content":"hello","createTime":"2026-09-30 11:58:03"}],"hasMore":true,"nextCursor":1790000000000}`
	})
	raw, err := cli.List(context.Background(), dir, ListRequest{ConversationID: "cid", Limit: 21})
	if err != nil {
		t.Fatal(err)
	}
	call := f.toolCalls("list_conversation_message_v2")
	if len(call) != 1 || call[0].Args["openconversation_id"] != "cid" || call[0].Args["forward"] != false || call[0].Args["limit"] != float64(21) {
		t.Fatalf("list call = %+v", call)
	}
	if call[0].Header.Get("x-user-access-token") != "uat-1" {
		t.Fatalf("token header missing")
	}
	var out struct {
		ContractVersion string `json:"contractVersion"`
		Success         bool   `json:"success"`
		HasMore         *bool  `json:"hasMore"`
		NextCursor      int64  `json:"nextCursor"`
		Messages        []struct {
			OpenMessageID string `json:"openMessageId"`
			SenderID      string `json:"senderId"`
			Content       string `json:"content"`
			CreateTime    string `json:"createTime"`
		} `json:"messages"`
	}
	if json.Unmarshal(raw, &out) != nil || out.ContractVersion != "im.message-list.v1" || !out.Success || out.HasMore == nil || !*out.HasMore ||
		out.NextCursor != 1790000000000 || len(out.Messages) != 1 || out.Messages[0].SenderID != "D1" || out.Messages[0].Content != "hello" ||
		out.Messages[0].CreateTime != "2026-09-30 11:58:03" {
		t.Fatalf("list output = %s", raw)
	}
}

func TestSDKListFailuresKeepTheirDiagnostics(t *testing.T) {
	code := "130003"
	_, cli, dir := startSDK(t, func(string, map[string]any) string {
		return `{"success":false,"errorCode":"` + code + `","errorMsg":"OpenId is not in conversation","traceId":"trace-1"}`
	})
	_, err := cli.List(context.Background(), dir, ListRequest{ConversationID: "cid"})
	var detail *HistoryError
	if !errors.As(err, &detail) || !detail.NotInConversation() || !detail.BusinessError() || detail.field("trace_id") != "trace-1" {
		t.Fatalf("not-in-conversation = %v", err)
	}
	code = "CrossOrgPermissionDenied"
	if _, err := cli.List(context.Background(), dir, ListRequest{ConversationID: "cid"}); !IsCrossOrgPermissionDenied(err) {
		t.Fatalf("cross-org = %v", err)
	}
}

func TestSDKRenewCrossOrgRead(t *testing.T) {
	f, cli, dir := startSDK(t, func(string, map[string]any) string {
		return toolOK(`{"scope":"chat.data:cross-org","grantType":"timed","expireAt":4102444800000}`)
	})
	if err := cli.RenewCrossOrgRead(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	call := f.toolCalls("chat_permission_grant")
	if len(call) != 1 || call[0].Args["scope"] != "chat.data:cross-org" || call[0].Args["ttl"] != "7d" || call[0].Args["agentCode"] != "wukong" {
		t.Fatalf("grant call = %+v", call)
	}
}

func TestSDKPlainSend(t *testing.T) {
	f, cli, dir := startSDK(t, func(string, map[string]any) string { return toolOK(`{"openTaskId":"task-1"}`) })
	sent, err := cli.Send(context.Background(), dir, SendRequest{ConversationID: "cid", AtOpenDingTalkID: "D1",
		Content: "<@D1> 已完成", IdempotencyKey: "key-1", ShowAITag: true})
	if err != nil || sent.OpenTaskID != "task-1" {
		t.Fatalf("send = %+v, %v", sent, err)
	}
	args := f.toolCalls("send_personal_message")[0].Args
	var content map[string]string
	if args["openConversationId"] != "cid" || args["msgType"] != "markdown" || args["clawType"] != "openClaw" || args["uuid"] != "key-1" ||
		json.Unmarshal([]byte(args["content"].(string)), &content) != nil || content["text"] != "<@D1> 已完成" {
		t.Fatalf("send args = %v", args)
	}
	if _, err := cli.Send(context.Background(), dir, SendRequest{RecipientOpenDingTalkID: "not-a-d-id", Content: "x", IdempotencyKey: "k"}); err == nil {
		t.Fatal("dws refuses a malformed --open-dingtalk-id before sending")
	}
}

func TestSDKQuoteReplyAndDuplicate(t *testing.T) {
	duplicate := false
	f, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
		switch tool {
		case "list_messages_by_ids":
			return toolOK(`{"messages":[{"openMessageId":"m-src","openConversationId":"cid","senderOpenDingTalkId":"D-sender"}]}`)
		default:
			if duplicate {
				return `{"success":false,"errorCode":"1001","errorMsg":"sendPersonalMessageSyncByServerPush error: Request is repeated with uuid 'key-1'."}`
			}
			return toolOK(`{"openTaskId":"task-2"}`)
		}
	})
	req := SendRequest{ConversationID: "cid", ReplyToOpenMsgID: "m-src", Content: "收到", IdempotencyKey: "key-1"}
	sent, err := cli.Send(context.Background(), dir, req)
	if err != nil || sent.OpenTaskID != "task-2" {
		t.Fatalf("reply = %+v, %v", sent, err)
	}
	args := f.toolCalls("send_personal_message")[0].Args
	var content map[string]string
	if args["msgType"] != "reply" || args["openConversationId"] != "cid" || json.Unmarshal([]byte(args["content"].(string)), &content) != nil ||
		content["referenceOpenMessageId"] != "m-src" || content["srcMsgSendOpenDingTalkId"] != "D-sender" || content["content"] != "收到" {
		t.Fatalf("reply args = %v", args)
	}
	if _, present := args["clawType"]; present {
		t.Fatal("a reply without the AI tag carries no clawType")
	}
	duplicate = true
	_, err = cli.Send(context.Background(), dir, req)
	var op *MessageOperationError
	if !errors.As(err, &op) || !op.DuplicateRequest {
		t.Fatalf("duplicate reply = %v", err)
	}
}

func TestSDKSendStatusAndSender(t *testing.T) {
	_, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
		if tool == "query_message_send_status" {
			return toolOK(`{"sendStatus":"SUCCESS","openConversationId":"cid","openMessageId":"m-1"}`)
		}
		return toolOK(`{"messages":[{"openMessageId":"m-src","openConversationId":"cid","senderOpenDingTalkId":"D-sender"}]}`)
	})
	status, err := cli.QuerySendStatus(context.Background(), dir, "task-1")
	if err != nil || status.State != "delivered" || status.OpenMessageID != "m-1" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	sender, err := cli.ResolveMessageSender(context.Background(), dir, "cid", "m-src")
	if err != nil || sender != "D-sender" {
		t.Fatalf("sender = %q, %v", sender, err)
	}
}

func TestSDKA2UICards(t *testing.T) {
	reject := false
	f, cli, dir := startSDK(t, func(tool string, _ map[string]any) string {
		if reject {
			return `{"success":false,"errorCode":"A2UI_TARGET_INVALID","errorMsg":"A2UI card target group does not belong to the creator organization","traceId":"trace-9"}`
		}
		if tool == "create_and_send_a2ui_card" {
			return toolOK(`{"bizId":"card-1","cardInstanceId":42}`)
		}
		return toolOK(`{}`)
	})
	in := A2UISendRequest{ConversationID: "cid", BizID: "card-1", RequestID: "req-1", Summary: "选一个", Messages: []string{`{"surfaceUpdate":{}}`}}
	receipt, err := cli.SendA2UI(context.Background(), dir, in)
	if err != nil || receipt.BizID != "card-1" || receipt.CardInstanceID != 42 {
		t.Fatalf("send = %+v, %v", receipt, err)
	}
	args := f.toolCalls("create_and_send_a2ui_card")[0].Args
	if args["openConversationId"] != "cid" || args["bizCardId"] != "card-1" || args["requestId"] != "req-1" || args["flowStatus"] != "PROCESSING" {
		t.Fatalf("a2ui args = %v", args)
	}
	annotations := []A2UIAnnotation{{SurfaceID: "s", ComponentID: "question", Type: "artifact"}}
	if err := cli.UpdateA2UI(context.Background(), dir, "card-1", "CONFIRMING", []string{`{}`}, annotations); err != nil {
		t.Fatal(err)
	}
	update := f.toolCalls("update_a2ui_card")[0].Args
	if update["bizId"] != "card-1" || update["flowStatus"] != "CONFIRMING" || update["requestId"] == "" {
		t.Fatalf("update args = %v", update)
	}
	reject = true
	_, err = cli.SendA2UI(context.Background(), dir, in)
	var op *MessageOperationError
	if !errors.As(err, &op) {
		t.Fatalf("rejected card = %v", err)
	}
	if text, ok := op.CardSendRejection(); !ok || !strings.Contains(text, "A2UI_TARGET_INVALID") || !strings.Contains(text, "trace-9") {
		t.Fatalf("rejection text = %q %v", text, ok)
	}
}

// A card event reaches the decision parser as it did through the CLI.
func TestCardEventLineParsesAsADecision(t *testing.T) {
	data := `{"eventId":"ev-1","eventKey":"user_card_action_triggered","occurredAtMs":1790000000000,"subId":"sub-1","payload":{"corpid":"corp-1","body":{"bizInfoDTO":{"bizId":"card-1"},"conversationContextDTO":{"openConversationId":"cid"},"operatorDTO":{"openDingTalkId":"D-op"},"a2uiEvent":{"action":{"name":"runtime.clarification.submit","context":{"sourceTurnId":"dec-1","sourceProjectionVersion":"coordinator-user-decision-v1","outcome":"answered","answers":{"q0":{"selected":["a"],"custom":""}}}}},"triggerTimestamp":1790000000000}}}`
	ev := dwsevents.Event{ID: "ev-1", Key: dws.EventCardAction, SubscriptionID: "sub-1", CorpID: "corp-1", Data: json.RawMessage(data)}
	e, err := userdecision.ParseEvent(EventLine(ev))
	if err != nil || e.ID != "ev-1" || e.CorpID != "corp-1" || e.CardID != "card-1" || e.OperatorID != "D-op" || e.RequestID != "dec-1" || len(e.Selected) != 1 {
		t.Fatalf("decision = %+v, %v", e, err)
	}
}

// Shared sessions mint once per identity, pass the caller's mint failure
// through, refuse a credential for another user, and clean up after
// themselves; with the switch off the caller keeps the CLI path.
func TestSharedSessionsMintOncePerIdentity(t *testing.T) {
	f, _, _ := startSDK(t, func(string, map[string]any) string { return toolOK(`{"openTaskId":"task-1"}`) })
	mints := 0
	mint := func(context.Context, Identity) (Credential, error) {
		mints++
		return Credential{UID: "42", ClientID: "client-1", AuthCode: "code-2"}, nil
	}
	shared := Shared{CLI: CLI{ClientSecret: "secret", Environment: "staging"}}
	id := Identity{AgentID: "agent-1", UID: "42", OrgID: "org-1"}
	var dirs []string
	for i := 0; i < 2; i++ {
		dir, cleanup, ok, err := shared.Open(context.Background(), id, mint)
		if !ok || err != nil {
			t.Fatalf("open %d: %v %v", i, ok, err)
		}
		if _, err := (CLI{}).Send(context.Background(), dir, SendRequest{ConversationID: "cid", Content: "x", IdempotencyKey: "k"}); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
		cleanup()
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("directory kept: %v", err)
		}
		if _, ok := sdkClients.Load(filepath.Clean(dir)); ok {
			t.Fatal("client kept after cleanup")
		}
	}
	if mints != 1 || len(f.tokens) != 2 { // one exchange from startSDK, one for the identity
		t.Fatalf("mints=%d exchanges=%d", mints, len(f.tokens))
	}
	boom := errors.New("identity context unavailable")
	if _, _, ok, err := shared.Open(context.Background(), Identity{AgentID: "agent-2", UID: "43", OrgID: "org-1"},
		func(context.Context, Identity) (Credential, error) { return Credential{}, boom }); !ok || !errors.Is(err, boom) {
		t.Fatalf("mint failure = %v %v", ok, err)
	}
	if _, _, _, err := shared.Open(context.Background(), Identity{AgentID: "agent-3", UID: "44", OrgID: "org-1"},
		func(context.Context, Identity) (Credential, error) {
			return Credential{UID: "99", ClientID: "c", AuthCode: "a"}, nil
		}); err == nil {
		t.Fatal("a credential for another user was accepted")
	}
	SetSDKSelector(func() bool { return false })
	if _, _, ok, _ := shared.Open(context.Background(), id, mint); ok {
		t.Fatal("switch off must leave the caller on the CLI")
	}
}
