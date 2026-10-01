package clicompat

// Golden tests against the real dws v1.0.62-beta.8 binary.
//
// testdata/dws_goldens.json was recorded by running dws's production
// entrypoint (app.Execute, via the TestCLIHelperProcess re-exec of dws's own
// test/mock_mcp harness) against a fake MCP gateway, with DWS_LANG=en and the
// CGO SafeChat backend compiled in, as in release builds. Each case holds the
// CLI arguments, the canned gateway answer per tool, and what dws did: the
// tool calls it made, stdout, stderr and the exit code. The generator is kept
// in testdata/golden_gen/clicompat_golden_test.go.txt; see its header for how to
// rerun it.
//
// Here each case is replayed through dws.CallRaw against a fake gateway
// that gives the same answers, with clicompat standing in for dws; the bytes must
// match except for the documented deviations in goldenDeviations.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

type goldenResponse struct {
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	Status     int    `json:"status"`
	RPCCode    int    `json:"rpcCode"`
	RPCMessage string `json:"rpcMessage"`
}

type goldenRequest struct {
	Path      string          `json:"path"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type goldenCase struct {
	Name      string                    `json:"name"`
	Args      []string                  `json:"args"`
	Responses map[string]goldenResponse `json:"responses"`
	Requests  []goldenRequest           `json:"requests"`
	Stdout    string                    `json:"stdout"`
	Stderr    string                    `json:"stderr"`
	Exit      int                       `json:"exit"`
}

func loadGoldens(t *testing.T) []goldenCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/dws_goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// fakeGateway answers tools/call like the golden recording and serves the
// DWS-hosted token refresh dws uses after a token rejection.
type fakeGateway struct {
	server    *httptest.Server
	responses map[string]goldenResponse
	mu        sync.Mutex
	requests  []goldenRequest
}

var serverNames = map[string]string{
	"0a1609437385696b77fc4771c3ddaf5656b487f809966c0cc8d4755e7b1d3b74": "chat",
	"450eede6b54d83e030140e66ec77c98a2e89a0869ef4db481f8217a98a42f821": "im",
}

func newFakeGateway(t *testing.T, responses map[string]goldenResponse) *fakeGateway {
	g := &fakeGateway{responses: responses}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth2/refreshToken" {
			_, _ = w.Write([]byte(`{"accessToken":"token-2","refreshToken":"refresh-2","expiresIn":7200}`))
			return
		}
		var envelope struct {
			ID     int `json:"id"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		path := r.URL.Path
		if name, ok := serverNames[strings.TrimPrefix(path, "/server/")]; ok {
			path = "/server/" + name
		}
		g.mu.Lock()
		g.requests = append(g.requests, goldenRequest{Path: path, Tool: envelope.Params.Name, Arguments: envelope.Params.Arguments})
		g.mu.Unlock()
		spec, ok := g.responses[envelope.Params.Name]
		if !ok {
			spec = goldenResponse{Kind: "rpc", RPCCode: -32601, RPCMessage: "unexpected tool " + envelope.Params.Name}
		}
		switch spec.Kind {
		case "http":
			w.WriteHeader(spec.Status)
			_, _ = w.Write([]byte(spec.Text))
		case "rpc":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID,
				"error": map[string]any{"code": spec.RPCCode, "message": spec.RPCMessage}})
		default:
			result := map[string]any{"content": []map[string]any{{"type": "text", "text": spec.Text}}}
			if spec.Kind == "isError" {
				result["isError"] = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": result})
		}
	}))
	t.Cleanup(g.server.Close)
	return g
}

// emulation is what clicompat produced for one golden case.
type emulation struct {
	stdout, stderr string
	exit           int
	requests       []goldenRequest
}

type emulator struct {
	t      *testing.T
	client *dws.Client
	out    emulation
}

func (e *emulator) call(server dws.Server, tool string, args map[string]any) ([]byte, *Failure) {
	raw, err := e.client.CallRaw(context.Background(), server, tool, args)
	if err != nil {
		return nil, CallFailure(err)
	}
	return CheckPayload(string(server), tool, raw)
}

func (e *emulator) legacyFail(f *Failure) {
	e.out.stderr = string(f.LegacyJSON())
	e.out.exit = f.ExitCode
}

func (e *emulator) unifiedFail(f *Failure) {
	e.out.stdout = string(f.UnifiedJSON())
	e.out.exit = f.ExitCode
}

func parseFlags(args []string) map[string]string {
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name := strings.TrimPrefix(arg, "--")
		if key, value, ok := strings.Cut(name, "="); ok {
			flags[key] = value
			continue
		}
		if name == "yes" || name == "all" {
			flags[name] = "true"
			continue
		}
		if i+1 < len(args) {
			flags[name] = args[i+1]
			i++
		}
	}
	return flags
}

func emulate(t *testing.T, c goldenCase) emulation {
	t.Helper()
	gateway := newFakeGateway(t, c.Responses)
	client, err := dws.NewWithToken(context.Background(),
		dws.Config{GatewayURL: gateway.server.URL, AuthURL: gateway.server.URL, SkipVerify: true},
		dws.Token{AccessToken: "token-1", RefreshToken: "refresh-1", ClientID: "client-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	e := &emulator{t: t, client: client}
	flags := parseFlags(c.Args)
	family, _, _ := strings.Cut(c.Name, "/")
	switch family {
	case "list":
		limit, _ := strconv.Atoi(flags["limit"])
		args := MessageListArgs(flags["group"], flags["time"], flags["direction"], limit)
		if fail := ValidateStrings(args); fail != nil {
			e.legacyFail(fail)
			break
		}
		payload, fail := e.call(dws.ServerChat, "list_conversation_message_v2", args)
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(MessageListOutput(payload, flags["direction"]))
	case "crossorg":
		payload, fail := e.call(dws.ServerIM, "chat_permission_grant", CrossOrgArgs())
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(LegacyOutput(payload))
	case "send":
		args, fail := SendArgs(SendFlags{
			Content: flags["content"], Title: flags["title"], IdempotencyKey: flags["idempotency-key"],
			AITag: flags["ai-tag"] == "true", ConversationID: flags["conversation-id"],
			AtOpenDingTalkIDs: flags["at-open-dingtalk-ids"], OpenDingTalkID: flags["open-dingtalk-id"],
		})
		if fail != nil {
			e.unifiedFail(fail)
			break
		}
		payload, fail := e.call(dws.ServerChat, "send_personal_message", args)
		if fail != nil {
			e.unifiedFail(fail)
			break
		}
		e.out.stdout = string(SendOutput(payload))
	case "status":
		payload, fail := e.call(dws.ServerIM, "query_message_send_status", map[string]any{"openTaskId": flags["open-task-id"]})
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(SendStatusOutput(payload, flags["open-task-id"]))
	case "byids":
		payload, fail := e.call(dws.ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": []string{flags["msg-ids"]}})
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(MessagesByIDsOutput(payload))
	case "reply":
		rf := ReplyFlags{
			Content: flags["content"], IdempotencyKey: flags["idempotency-key"], AITag: flags["ai-tag"] == "true",
			ConversationID: flags["group"], MessageID: flags["message-id"],
		}
		if fail := ValidateReply(rf); fail != nil {
			e.legacyFail(fail)
			break
		}
		lookup, fail := e.call(dws.ServerIM, "list_messages_by_ids", map[string]any{"openMsgIds": []string{strings.TrimSpace(rf.MessageID)}})
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		conversationID, sender, fail := ReplySource(lookup, rf)
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		args := ReplyArgs(rf, conversationID, sender)
		if fail := ValidateStrings(args); fail != nil {
			e.legacyFail(fail)
			break
		}
		payload, fail := e.call(dws.ServerChat, "send_personal_message", args)
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(ReplyOutput(payload, rf, conversationID, sender))
	case "a2ui":
		var messages []string
		if err := json.Unmarshal([]byte(strings.TrimSpace(flags["a2ui-messages"])), &messages); err != nil {
			t.Fatal(err)
		}
		args, fail := A2UISendArgs(A2UISendFlags{
			ChatID: flags["chat-id"], OpenDingTalkID: flags["open-dingtalk-id"], BizCardID: flags["biz-card-id"],
			RequestID: flags["request-id"], Summary: flags["card-summary"], Messages: messages,
		})
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		payload, fail := e.call(dws.ServerIM, "create_and_send_a2ui_card", args)
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(A2UISendOutput(payload))
	case "update":
		var messages []string
		if err := json.Unmarshal([]byte(flags["content"]), &messages); err != nil {
			t.Fatal(err)
		}
		var annotations json.RawMessage
		if value, ok := flags["a2ui-annotations"]; ok {
			annotations = json.RawMessage(value)
		}
		args, fail := UpdateA2UIArgs(flags["biz-id"], flags["flow-status"], messages, annotations)
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		payload, fail := e.call(dws.ServerIM, "update_a2ui_card", args)
		if fail != nil {
			e.legacyFail(fail)
			break
		}
		e.out.stdout = string(LegacyOutput(payload))
	default:
		t.Fatalf("unknown golden family %q", family)
	}
	gateway.mu.Lock()
	e.out.requests = append([]goldenRequest(nil), gateway.requests...)
	gateway.mu.Unlock()
	return e.out
}

var (
	uuidPattern          = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	dwsDecryptReasonText = regexp.MustCompile(`"reason": "读取登录态失败[^"]*"`)
)

// goldenDeviations lists the cases whose bytes intentionally differ from dws,
// with the check that replaces byte equality.
var goldenDeviations = map[string]func(t *testing.T, c goldenCase, got emulation){
	// dws's transport decodes the tool text with plain encoding/json
	// (internal/transport/client.go:392), so it prints 12345678901234567891 as
	// 12345678901234567000, 1.50 as 1.5 and 1e3 as 1000; CheckPayload keeps
	// the numbers as sent. Equal once both sides are read as float64.
	"list/numbers": func(t *testing.T, c goldenCase, got emulation) {
		if !strings.Contains(got.stdout, `"big": 12345678901234567891`) || !strings.Contains(got.stdout, `"f": 1.50`) {
			t.Fatalf("numbers were not preserved:\n%s", got.stdout)
		}
		assertFloatJSONEqual(t, c.Stdout, got.stdout)
	},
	// dws failed to open a SafeChat session (no stored login); clicompat cannot
	// decrypt at all and reports reason decrypt_unavailable in the same shape.
	"list/encrypted":  decryptReasonDeviation,
	"byids/encrypted": decryptReasonDeviation,
	// dws.CallRaw intercepts gateway auth codes and returns only the code,
	// so the untyped message carries the code instead of the raw tool text.
	"list/gateway-auth": authCodeDeviation,
	"send/gateway-auth": authCodeDeviation,
	// dws.CallRaw intercepts gateway auth codes whatever success says; dws
	// reports success:false+TOKEN_VERIFIED_FAILED as a business_error (exit 1).
	"list/gateway-auth-false": func(t *testing.T, c goldenCase, got emulation) {
		if got.exit != 2 || !strings.Contains(got.stderr, `"message": "[AUTH_TOKEN_EXPIRED] TOKEN_VERIFIED_FAILED`) {
			t.Fatalf("auth interception = exit %d\n%s", got.exit, got.stderr)
		}
	},
	// dws panics on the nil map (enrichReplyResult on a null send response,
	// internal/shortcut/chat/lark_alignment.go:293) and exits 5; clicompat prints
	// the enrichment alone, which Multica rejects for its missing success.
	"reply/send-plain-text": func(t *testing.T, c goldenCase, got emulation) {
		if c.Exit != 5 || !strings.Contains(c.Stderr, "internal panic") {
			t.Fatalf("dws golden changed: exit %d %s", c.Exit, c.Stderr)
		}
		if got.exit != 0 || strings.Contains(got.stdout, `"success"`) || !strings.Contains(got.stdout, `"contractVersion": "im.message-reply.v1"`) {
			t.Fatalf("clicompat = exit %d\n%s", got.exit, got.stdout)
		}
	},
}

func decryptReasonDeviation(t *testing.T, c goldenCase, got emulation) {
	want := dwsDecryptReasonText.ReplaceAllString(c.Stdout, `"reason": "decrypt_unavailable"`)
	if got.stdout != want {
		t.Fatalf("stdout mismatch\n--- dws (reason normalised)\n%s\n--- clicompat\n%s", want, got.stdout)
	}
}

func authCodeDeviation(t *testing.T, c goldenCase, got emulation) {
	if got.exit != c.Exit {
		t.Fatalf("exit = %d, want %d", got.exit, c.Exit)
	}
	for _, text := range []string{got.stdout, got.stderr} {
		if text == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			t.Fatal(err)
		}
		body := decoded["error"].(map[string]any)
		message, _ := body["message"].(string)
		if !strings.HasPrefix(message, "[AUTH_TOKEN_EXPIRED] ") || !strings.Contains(message, "hint: Re-authenticate: dws auth login") {
			t.Fatalf("message = %q", message)
		}
	}
}

func assertFloatJSONEqual(t *testing.T, want, got string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(want), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON mismatch\n--- dws\n%s\n--- clicompat\n%s", want, got)
	}
}

func TestGoldensMatchDWS(t *testing.T) {
	cases := loadGoldens(t)
	if len(cases) < 100 {
		t.Fatalf("only %d golden cases", len(cases))
	}
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			got := emulate(t, c)
			got.stdout = strings.ReplaceAll(got.stdout, gatewayEndpoint, "{{GATEWAY}}")
			got.stderr = strings.ReplaceAll(got.stderr, gatewayEndpoint, "{{GATEWAY}}")
			assertSameRequests(t, c.Requests, got.requests)
			if check, ok := goldenDeviations[c.Name]; ok {
				check(t, c, got)
				return
			}
			if got.exit != c.Exit {
				t.Errorf("exit = %d, want %d", got.exit, c.Exit)
			}
			if got.stdout != c.Stdout {
				t.Errorf("stdout mismatch\n--- dws\n%s\n--- clicompat\n%s", c.Stdout, got.stdout)
			}
			if got.stderr != c.Stderr {
				t.Errorf("stderr mismatch\n--- dws\n%s\n--- clicompat\n%s", c.Stderr, got.stderr)
			}
		})
	}
}

// assertSameRequests compares the tool calls. dws retries a call once
// after a token rejection (dws does not), so consecutive repeats collapse.
func assertSameRequests(t *testing.T, want, got []goldenRequest) {
	t.Helper()
	var collapsed []goldenRequest
	for _, request := range got {
		if n := len(collapsed); n > 0 && collapsed[n-1].Tool == request.Tool && string(collapsed[n-1].Arguments) == string(request.Arguments) {
			continue
		}
		collapsed = append(collapsed, request)
	}
	if len(collapsed) != len(want) {
		t.Fatalf("requests = %d %s, want %d %s", len(collapsed), describeRequests(collapsed), len(want), describeRequests(want))
	}
	for i := range want {
		if collapsed[i].Path != want[i].Path || collapsed[i].Tool != want[i].Tool {
			t.Fatalf("request %d = %s %s, want %s %s", i, collapsed[i].Path, collapsed[i].Tool, want[i].Path, want[i].Tool)
		}
		if a, b := normalizedArguments(t, want[i].Arguments), normalizedArguments(t, collapsed[i].Arguments); !reflect.DeepEqual(a, b) {
			t.Fatalf("request %d arguments\n dws    %s\n clicompat %s", i, want[i].Arguments, collapsed[i].Arguments)
		}
	}
}

func describeRequests(requests []goldenRequest) string {
	var parts []string
	for _, request := range requests {
		parts = append(parts, request.Path+" "+request.Tool)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// normalizedArguments decodes the wire arguments; random UUIDs (requestId,
// a defaulted bizCardId) are replaced by a placeholder.
func normalizedArguments(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var args map[string]any
	if err := decoder.Decode(&args); err != nil {
		t.Fatal(err)
	}
	for key, value := range args {
		if id, ok := value.(string); ok && uuidPattern.MatchString(id) {
			args[key] = "<uuid>"
		}
	}
	return args
}
