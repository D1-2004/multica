package clicompat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Current-version D openDingTalkIds valid under dws's
// LooksLikeCurrentDOpenDingTalkID; the first is dws's own test fixture
// (internal/helpers/chat_coverage_more_test.go:18).
const (
	testDID  = "DAAAAAAAAAAAiE"
	testDID2 = "DAAAAAAAAAAAAAAAAAAAAAAiEiE"
)

func TestSanitizeTitleFromText(t *testing.T) {
	// Expected values for the first cases were observed from dws
	// (testdata/dws_goldens.json send/title-*); the rest follow chat.go:2057.
	tests := []struct{ name, text, want string }{
		{"empty", "", "消息"},
		{"blank", "   ", "消息"},
		{"url in the middle", "查看报告 https://example.com/a?b=1 谢谢", "查看报告"},
		{"http in the middle", "see http://a.test now", "see"},
		{"https wins over an earlier http", "see http://a.test and https://b.test", "see http://a.test and"},
		{"text starting with url", "https://example.com/x 看这个", "消息"},
		{"blank prefix before url is kept whole", "   http://example.com/x", "   http://example.com/x"},
		{"percent", "完成率 50%", "消息"},
		{"30 runes kept", strings.Repeat("a", 30), strings.Repeat("a", 30)},
		{"31 runes truncated", strings.Repeat("a", 31), strings.Repeat("a", 30) + "..."},
		{"cjk truncated at 30 runes", strings.Repeat("中", 40), strings.Repeat("中", 30) + "..."},
		{"emoji truncated at 97 bytes", strings.Repeat("😀", 30), strings.Repeat("😀", 24) + "..."},
		{"bytes cut past trailing ascii", strings.Repeat("😀", 25) + "abcde", strings.Repeat("😀", 24) + "..."},
		{"long prefix before url", strings.Repeat("中", 40) + " https://x.test", strings.Repeat("中", 30) + "..."},
		{"mention stays bare in title", "@" + testDID + " ok", "@" + testDID + " ok"},
		{"text is not trimmed", "  body  ", "  body  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTitleFromText(tt.text); got != tt.want {
				t.Fatalf("title(%q) = %q, want %q", tt.text, got, tt.want)
			}
			if got := len(sanitizeTitleFromText(tt.text)); got > 100 {
				t.Fatalf("title is %d bytes", got)
			}
		})
	}
}

func TestNormalizeAtPlaceholders(t *testing.T) {
	// dws fixture: internal/helpers/chat_coverage_more_test.go:137.
	if got := normalizeAtPlaceholders("hello @u1 <@u2>", []string{"", "u1", "u2"}, true); got != "hello <@u1> <@u2>" {
		t.Fatalf("wrap = %q", got)
	}
	if got := normalizeAtPlaceholders("hello @u1 <@u2>", []string{"", "u1", "u2"}, false); got != "hello @u1 @u2" {
		t.Fatalf("unwrap = %q", got)
	}
	if got := normalizeAtPlaceholders("<@u1><@u1>@u1", []string{" u1 "}, true); got != "<@u1><@u1><@u1>" {
		t.Fatalf("wrapped placeholders must not double wrap: %q", got)
	}
	if got := PrepareChatReplyMentions("@alliance and @all", nil, false, true); got != "<@all>iance and <@all>" {
		t.Fatalf("reply @all wrapping = %q", got)
	}
}

func decodeContentJSON(t *testing.T, args map[string]any) map[string]string {
	t.Helper()
	var content map[string]string
	if err := json.Unmarshal([]byte(args["content"].(string)), &content); err != nil {
		t.Fatal(err)
	}
	return content
}

func TestSendArgsGroupMentionsAndDirect(t *testing.T) {
	args, fail := SendArgs(SendFlags{
		Content: "@" + testDID + " hi <@" + testDID2 + "> @" + testDID + " <b>&", IdempotencyKey: "K",
		AITag: true, ConversationID: " cid1 ", AtOpenDingTalkIDs: testDID + ", " + testDID2,
	})
	if fail != nil {
		t.Fatal(fail)
	}
	if args["openConversationId"] != "cid1" || args["msgType"] != "markdown" || args["clawType"] != "openClaw" || args["uuid"] != "K" {
		t.Fatalf("args = %#v", args)
	}
	if ids := args["atOpenDingTalkIds"].([]string); !reflect.DeepEqual(ids, []string{testDID, " " + testDID2}) {
		t.Fatalf("atOpenDingTalkIds = %#v (dws splits on ',' without trimming)", ids)
	}
	content := decodeContentJSON(t, args)
	if content["text"] != "<@"+testDID+"> hi <@"+testDID2+"> <@"+testDID+"> <b>&" {
		t.Fatalf("text = %q", content["text"])
	}
	if !strings.Contains(args["content"].(string), "<b>&") {
		t.Fatalf("content must not be HTML-escaped: %s", args["content"])
	}
	if !strings.HasPrefix(args["content"].(string), `{"text":`) {
		t.Fatalf("content keys must be sorted: %s", args["content"])
	}
	if content["title"] != "@"+testDID+" hi <@DAAAAAAAA..." {
		t.Fatalf("title is computed before mention wrapping: %q", content["title"])
	}

	direct, fail := SendArgs(SendFlags{Content: "x", IdempotencyKey: "K2", OpenDingTalkID: testDID, Title: "T"})
	if fail != nil {
		t.Fatal(fail)
	}
	want := map[string]any{"receiverOpenDingTalkId": testDID, "msgType": "markdown", "clawType": "", "uuid": "K2", "content": `{"text":"x","title":"T"}`}
	if !reflect.DeepEqual(direct, want) {
		t.Fatalf("direct = %#v", direct)
	}
}

func TestSendArgsRejections(t *testing.T) {
	tests := []struct {
		name   string
		flags  SendFlags
		reason string
		exit   int
		msg    string
	}{
		{"no target", SendFlags{Content: "x"}, "require_one_of", 3, "--conversation-id, --user or --open-dingtalk-id is required"},
		{"two targets", SendFlags{Content: "x", ConversationID: "c", OpenDingTalkID: testDID}, "mutually_exclusive", 3, ""},
		{"user id is not a D id", SendFlags{Content: "x", OpenDingTalkID: "user-1"}, "target_type_mismatch", 3, "--open-dingtalk-id 收到的值不符合当前 D 版本 openDingTalkId 格式"},
		{"padded D id", SendFlags{Content: "x", OpenDingTalkID: " " + testDID}, "target_type_mismatch", 3, ""},
		{"empty content", SendFlags{ConversationID: "c"}, "require_one_of", 3, ""},
		{"zero width in content", SendFlags{Content: "a\u200bb", ConversationID: "c"}, "", 3, "content contains dangerous Unicode characters"},
		{"control char in key", SendFlags{Content: "x", ConversationID: "c", IdempotencyKey: "K\x01"}, "", 3, "uuid contains invalid control characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, fail := SendArgs(tt.flags)
			if fail == nil {
				t.Fatalf("accepted: %#v", args)
			}
			if fail.Category != "validation" || fail.Reason != tt.reason || fail.ExitCode != tt.exit {
				t.Fatalf("failure = %+v", fail)
			}
			if tt.msg != "" && fail.Message != tt.msg {
				t.Fatalf("message = %q", fail.Message)
			}
		})
	}
	// A raw CR inside the text is escaped inside the content JSON, so it passes
	// (dws golden send/cr-in-content).
	if _, fail := SendArgs(SendFlags{Content: "a\r\nb", ConversationID: "c"}); fail != nil {
		t.Fatalf("CR in content rejected: %v", fail)
	}
}

func TestValidateReplyRejections(t *testing.T) {
	ok := ReplyFlags{Content: "收到", MessageID: "m", ConversationID: "c"}
	tests := []struct {
		name    string
		content string
		mid     string
		want    string
	}{
		{"blank body", "  \n ", "m", "必填参数 --content 不能为空"},
		{"blank message id", "ok", "  ", "请指定 --ref-msg-id、--message-id 之一"},
		{"undeclared mention", "hi @" + testDID, "m", "正文 @成员占位符必须通过 --at-open-dingtalk-ids 声明"},
		{"wrapped undeclared mention", "<@" + testDID + "> hi", "m", "正文 @成员占位符必须通过 --at-open-dingtalk-ids 声明"},
		{"@all", "hi @all", "m", "正文 <@all> 必须同时指定 --at-all"},
		{"<@all>", "<@all> hi", "m", "正文 <@all> 必须同时指定 --at-all"},
		{"@all before punctuation", "a@all.", "m", "正文 <@all> 必须同时指定 --at-all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := ok
			f.Content, f.MessageID = tt.content, tt.mid
			fail := ValidateReply(f)
			if fail == nil || fail.Message != tt.want || fail.ExitCode != 3 || fail.Category != "validation" {
				t.Fatalf("ValidateReply = %+v, want %q", fail, tt.want)
			}
			if got := string(fail.LegacyJSON()); got != "{\n  \"error\": {\n    \"category\": \"validation\",\n    \"code\": 3,\n    \"message\": \""+tt.want+"\"\n  }\n}\n" {
				t.Fatalf("LegacyJSON = %s", got)
			}
		})
	}
	for _, content := range []string{"@alliance hi", "@all_x", "@all-x", "@" + testDID + "x", "@user-1 ok", "  ok  "} {
		f := ok
		f.Content = content
		if fail := ValidateReply(f); fail != nil {
			t.Fatalf("ValidateReply(%q) = %v", content, fail)
		}
	}
}

func TestReplySourceAndArgs(t *testing.T) {
	f := ReplyFlags{Content: "  收到 <b>&  ", IdempotencyKey: " K ", ConversationID: " cid ", MessageID: " mid "}
	tests := []struct {
		name, payload, cid, sender, fail string
	}{
		{"flat sender", `{"success":true,"result":{"messages":[{"openMessageId":"mid","openConversationId":"cid","senderOpenDingTalkId":"` + testDID + `"}]}}`, "cid", testDID, ""},
		{"nested sender in result array", `{"result":[{"messageId":"mid","conversationId":"cid","sender":{"name":"n","openDingTalkId":"` + testDID2 + `"}}]}`, "cid", testDID2, ""},
		{"other ids skipped", `{"messages":[{"openMessageId":"x","openConversationId":"other"},{"openMessageId":"mid","openCid":"cid","senderOpenId":"S"}]}`, "cid", "S", ""},
		{"conversation mismatch", `{"messages":[{"openMessageId":"mid","openConversationId":"other","senderOpenDingTalkId":"S"}]}`, "", "", "源消息会话与 --group/--conversation-id 不一致或下游未提供会话身份"},
		{"conversation missing", `{"messages":[{"openMessageId":"mid","senderOpenDingTalkId":"S"}]}`, "", "", "源消息会话与 --group/--conversation-id 不一致或下游未提供会话身份"},
		{"duplicate", `{"messages":[{"openMessageId":"mid","openConversationId":"cid","senderOpenDingTalkId":"S"},{"openMessageId":"mid","openConversationId":"cid","senderOpenDingTalkId":"S"}]}`, "", "", "消息 ID 返回重复记录，无法确定唯一源消息"},
		{"not found", `{"messages":[]}`, "", "", "未找到精确匹配的源消息 ID；未执行写入"},
		{"null payload", `null`, "", "", "未找到精确匹配的源消息 ID；未执行写入"},
		{"sender is a display name", `{"messages":[{"openMessageId":"mid","openConversationId":"cid","sender":"张三"}]}`, "", "", "源消息缺少发送者 openDingTalkId，未执行回复"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cid, sender, fail := ReplySource([]byte(tt.payload), f)
			if tt.fail != "" {
				if fail == nil || fail.Message != tt.fail || fail.ExitCode != 3 {
					t.Fatalf("ReplySource = %q %q %+v, want %q", cid, sender, fail, tt.fail)
				}
				return
			}
			if fail != nil || cid != tt.cid || sender != tt.sender {
				t.Fatalf("ReplySource = %q %q %+v", cid, sender, fail)
			}
		})
	}

	args := ReplyArgs(f, "cid", testDID)
	want := map[string]any{
		"openConversationId": "cid",
		"msgType":            "reply",
		"uuid":               "K",
		"content":            `{"content":"收到 \u003cb\u003e\u0026","referenceOpenMessageId":"mid","replyMsgType":"text","srcMsgSendOpenDingTalkId":"` + testDID + `"}`,
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ReplyArgs without --ai-tag = %#v", args)
	}
	f.AITag = true
	if got := ReplyArgs(f, "cid", testDID)["clawType"]; got != "openClaw" {
		t.Fatalf("clawType = %#v", got)
	}
}

func TestCheckPayloadPredicates(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		payload  string
		ok       string // expected success text
		category string
		reason   string
		code     string
		trace    string
		message  string
		exit     int
		legacy   string // substring of LegacyJSON
	}{
		{name: "stamps success and keeps numbers", payload: `{"a":12345678901234567891,"f":1.50,"s":"<&>"}`,
			ok: `{"a":12345678901234567891,"f":1.50,"s":"<&>","success":true}`},
		{name: "keeps explicit success", payload: ` {"success":true,"b":1} `, ok: `{"b":1,"success":true}`},
		{name: "zero codes are not errors", payload: `{"code":0,"errcode":"0","errorCode":"ok","err_code":"success","error":""}`,
			ok: `{"code":0,"err_code":"success","errcode":"0","error":"","errorCode":"ok","success":true}`},
		{name: "success false", payload: `{"success":false,"errorCode":"CrossOrgPermissionDenied","errorMsg":" denied ","traceId":"t1"}`,
			category: "api", reason: "business_error", code: "CrossOrgPermissionDenied", trace: "t1", message: "denied", exit: 1,
			legacy: `"reason": "business_error"`},
		{name: "nested success false wins", payload: `{"success":false,"errorMsg":"outer","data":{"success":false,"errorMsg":"inner","requestId":"r9"}}`,
			category: "api", reason: "business_error", trace: "r9", message: "inner", exit: 1},
		{name: "nested under result array", payload: `{"result":[{"success":false,"errorCode":"X1"}]}`,
			category: "api", reason: "business_error", code: "X1", message: "business error: code X1", exit: 1},
		{name: "numeric errorCode dropped", payload: `{"success":false,"errorCode":130003,"errorMsg":"not in conversation"}`,
			category: "api", reason: "business_error", message: "not in conversation", exit: 1},
		{name: "wrapper code defers to nested", payload: `{"success":false,"code":"BUSINESS_ERROR","result":{"errorCode":"ROBOT_NOT_FOUND"}}`,
			category: "api", reason: "business_error", code: "ROBOT_NOT_FOUND", message: "business error: success=false", exit: 1},
		{name: "wrapper code kept without nested", payload: `{"success":false,"code":"-1"}`,
			category: "api", reason: "business_error", code: "-1", message: "business error: success=false", exit: 1},
		{name: "PARAM_ERROR override", payload: `{"success":false,"errorCode":"PARAM_ERROR","errorMsg":"bad"}`,
			category: "api", reason: "invalid_request", code: "PARAM_ERROR", message: "bad", exit: 1, legacy: `"stage": "tool_validation"`},
		{name: "NETWORK_ERROR override", payload: `{"success":false,"errorCode":"NETWORK_ERROR","errorMsg":"down"}`,
			category: "api", reason: "backend_dependency_unavailable", code: "NETWORK_ERROR", message: "MCP 后端依赖暂时不可用", exit: 1, legacy: `"dws doctor --json"`},
		{name: "999 NPE override", payload: `{"success":false,"errorCode":"999","errorMsg":"java.lang.NullPointerException"}`,
			category: "api", reason: "upstream_internal_error", code: "999", exit: 1},
		{name: "code 200 with success true is untyped", payload: `{"success":true,"code":200}`,
			category: "api", message: `{"code":200,"success":true}`, exit: 1,
			legacy: `"message": "[MCP_TOOL_ERROR] {\"code\":200,\"success\":true}"`},
		{name: "string success false is untyped", payload: `{"success":"false"}`, category: "api", exit: 1, legacy: `"category": "internal"`},
		{name: "status error is untyped", payload: `{"status":"error"}`, category: "api", exit: 1},
		{name: "error object is untyped", payload: `{"error":{"x":1}}`, category: "api", exit: 1},
		{name: "projected read adds operation", tool: "list_conversation_message_v2", payload: `{"errcode":7}`, category: "api", exit: 1,
			legacy: `(operation: chat/list_conversation_message_v2)`},
		{name: "print path shows extracted message", tool: "update_a2ui_card", payload: `{"errorCode":"E1","errorMsg":"boom","logId":"L1"}`,
			category: "api", message: "boom (code: E1, logId: L1)", exit: 1},
		{name: "gateway auth code", payload: `{"errorCode":"DWS_SERVICE_UNAUTHORIZED"}`, category: "auth", exit: 2,
			legacy: `"message": "[AUTH_TOKEN_EXPIRED] {\"errorCode\":\"DWS_SERVICE_UNAUTHORIZED\",\"success\":true}\n  hint: Re-authenticate: dws auth login"`},
		{name: "gateway auth code with success false is a business error", payload: `{"success":false,"code":"USER_TOKEN_ILLEGAL"}`,
			category: "api", reason: "business_error", code: "USER_TOKEN_ILLEGAL", exit: 1},
		{name: "not logged in", payload: `{"error":"Missing service_id or access_key"}`, category: "auth", message: "当前未登录", exit: 2},
		{name: "plain text becomes null", payload: `"hello"`, ok: `null`},
		{name: "array becomes null", payload: `[1]`, ok: `null`},
		{name: "trailing data becomes null", payload: `{"a":1} x`, ok: `null`},
		{name: "empty payload", payload: " \n", category: "api", reason: "empty_tool_response", exit: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := tt.tool
			if tool == "" {
				tool = "send_personal_message"
			}
			got, fail := CheckPayload("chat", tool, []byte(tt.payload))
			if tt.ok != "" {
				if fail != nil || string(got) != tt.ok {
					t.Fatalf("CheckPayload = %s %+v, want %s", got, fail, tt.ok)
				}
				return
			}
			if fail == nil {
				t.Fatalf("accepted: %s", got)
			}
			if fail.Category != tt.category || fail.Reason != tt.reason || fail.ServerErrorCode != tt.code ||
				fail.TraceID != tt.trace || fail.ExitCode != tt.exit {
				t.Fatalf("failure = %+v", fail)
			}
			if tt.message != "" && fail.Message != tt.message {
				t.Fatalf("message = %q, want %q", fail.Message, tt.message)
			}
			if tt.legacy != "" && !strings.Contains(string(fail.LegacyJSON()), tt.legacy) {
				t.Fatalf("LegacyJSON lacks %s:\n%s", tt.legacy, fail.LegacyJSON())
			}
		})
	}
}

func TestValidateStrings(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"tab and newline allowed", map[string]any{"a": "x\ty\nz", "b": 1, "c": true}, ""},
		{"left-to-right mark allowed", map[string]any{"a": "x\u200ey"}, ""},
		{"carriage return", map[string]any{"a": "x\ry"}, "a contains invalid control characters"},
		{"delete", map[string]any{"a": "x\x7f"}, "a contains invalid control characters"},
		{"zero width space", map[string]any{"a": "\u200b"}, "a contains dangerous Unicode characters"},
		{"zero width joiner", map[string]any{"a": "\u200d"}, "a contains dangerous Unicode characters"},
		{"bom", map[string]any{"a": "\ufeff"}, "a contains dangerous Unicode characters"},
		{"bidi override", map[string]any{"a": "\u202e"}, "a contains dangerous Unicode characters"},
		{"line separator", map[string]any{"a": "\u2028"}, "a contains dangerous Unicode characters"},
		{"bidi isolate", map[string]any{"a": "\u2069"}, "a contains dangerous Unicode characters"},
		{"nested map uses the inner key", map[string]any{"outer": map[string]any{"inner": "\u200b"}}, "inner contains dangerous Unicode characters"},
		{"string in []any", map[string]any{"list": []any{"ok", "\x01"}}, "list[1] contains invalid control characters"},
		{"map in []any", map[string]any{"list": []any{map[string]any{"k": "\u2066"}}}, "k contains dangerous Unicode characters"},
		{"typed []string is not checked", map[string]any{"ids": []string{"\u200b"}}, ""},
		{"[]any inside []any is not checked", map[string]any{"list": []any{[]any{"\u200b"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fail := ValidateStrings(tt.args)
			if tt.want == "" {
				if fail != nil {
					t.Fatalf("rejected: %v", fail)
				}
				return
			}
			if fail == nil || fail.Message != tt.want || fail.Category != "validation" || fail.ExitCode != 3 || fail.Reason != "" {
				t.Fatalf("ValidateStrings = %+v, want %q", fail, tt.want)
			}
		})
	}
}

// With several bad keys dws reports whichever validateCallArguments meets
// first in Go's map order.
func TestValidateStringsSeveralBadKeys(t *testing.T) {
	fail := ValidateStrings(map[string]any{"b": "\x01", "a": "\u200b"})
	if fail == nil || (fail.Message != "a contains dangerous Unicode characters" && fail.Message != "b contains invalid control characters") {
		t.Fatalf("ValidateStrings = %+v", fail)
	}
}

func TestUpdateA2UIArgs(t *testing.T) {
	for raw, want := range map[string]string{
		"confirming": "CONFIRMING", " 8 ": "CONFIRMING", "1": "PROCESSING", "Finish": "FINISH", "2": "INPUTTING",
		"4": "EXECUTING", "5": "ERROR", "6": "ABORTED", "7": "TIMEOUT", "9": "CONFIRMED", "confirmed": "CONFIRMED",
	} {
		args, fail := UpdateA2UIArgs(" B1 ", raw, []string{"{}"}, nil)
		if fail != nil || args["flowStatus"] != want || args["bizId"] != "B1" {
			t.Fatalf("flow status %q = %#v %v, want %s", raw, args, fail, want)
		}
	}
	first, _ := UpdateA2UIArgs("B1", "FINISH", []string{"{}"}, nil)
	second, _ := UpdateA2UIArgs("B1", "FINISH", []string{"{}"}, json.RawMessage(`[]`))
	if !uuidPattern.MatchString(first["requestId"].(string)) || first["requestId"] == second["requestId"] {
		t.Fatalf("requestIds = %v, %v", first["requestId"], second["requestId"])
	}
	if got, _ := json.Marshal(first["a2uiAnnotations"]); string(got) != "[]" {
		t.Fatalf("absent annotations = %s", got)
	}
	if got, _ := json.Marshal(second["a2uiAnnotations"]); string(got) != "[]" {
		t.Fatalf("empty annotations = %s", got)
	}
	annotated, _ := UpdateA2UIArgs("B1", "FINISH", []string{"{}"}, json.RawMessage(`[{"surfaceId":"s","type":"artifact"}]`))
	if got, _ := json.Marshal(annotated["a2uiAnnotations"]); string(got) != `[{"surfaceId":"s","type":"artifact"}]` {
		t.Fatalf("annotations = %s", got)
	}

	rejections := []struct {
		name, biz, status, annotations string
		messages                       []string
		category                       string
		exit                           int
		prefix                         string
	}{
		{"unknown status", "B1", "DONE", "", []string{"{}"}, "internal", 5, "--flow-status must be one of"},
		{"missing status", "B1", "", "", []string{"{}"}, "validation", 3, "missing required flag(s): --flow-status\n"},
		{"missing biz and status", "", "", "", []string{"{}"}, "validation", 3, "missing required flag(s): --biz-id, --flow-status\n"},
		{"blank biz", "  ", "FINISH", "", []string{"{}"}, "internal", 5, "--biz-id 不能为空"},
		{"biz with space", "B 1", "FINISH", "", []string{"{}"}, "internal", 5, "--biz-id 必须是"},
		{"placeholder biz", "${bizId}", "FINISH", "", []string{"{}"}, "internal", 5, "--biz-id 仍是占位符 \"${bizId}\""},
		{"zero width biz", "B\u200b1", "FINISH", "", []string{"{}"}, "validation", 3, "bizId contains dangerous Unicode characters"},
		{"null annotations", "B1", "FINISH", "null", []string{"{}"}, "internal", 5, "--a2ui-annotations must be a JSON object array"},
		{"null annotation", "B1", "FINISH", "[null]", []string{"{}"}, "internal", 5, "--a2ui-annotations must contain only JSON objects"},
		{"no messages", "B1", "FINISH", "", nil, "internal", 5, "--content must contain at least one message"},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			var annotations json.RawMessage
			if tt.annotations != "" {
				annotations = json.RawMessage(tt.annotations)
			}
			args, fail := UpdateA2UIArgs(tt.biz, tt.status, tt.messages, annotations)
			if fail == nil || fail.Category != tt.category || fail.ExitCode != tt.exit || !strings.HasPrefix(fail.Message, tt.prefix) {
				t.Fatalf("UpdateA2UIArgs = %#v %+v", args, fail)
			}
		})
	}
}

func TestA2UISendArgs(t *testing.T) {
	args, fail := A2UISendArgs(A2UISendFlags{ChatID: " cid ", Messages: []string{"a", "b"}})
	if fail != nil {
		t.Fatal(fail)
	}
	if args["summary"] != "a\nb" || !uuidPattern.MatchString(args["requestId"].(string)) || !uuidPattern.MatchString(args["bizCardId"].(string)) ||
		args["openConversationId"] != "cid" || args["protocolVersion"] != "1.0" || args["flowStatus"] != "PROCESSING" {
		t.Fatalf("args = %#v", args)
	}
	for _, key := range []string{"clawType", "uuid", "receiverOpenDingTalkId"} {
		if _, ok := args[key]; ok {
			t.Fatalf("unexpected %s: %#v", key, args)
		}
	}
	if _, fail := A2UISendArgs(A2UISendFlags{Messages: []string{"a"}}); fail == nil || fail.ExitCode != 3 {
		t.Fatalf("no target = %+v", fail)
	}
	if _, fail := A2UISendArgs(A2UISendFlags{ChatID: "c", OpenDingTalkID: testDID, Messages: []string{"a"}}); fail == nil || fail.ExitCode != 3 {
		t.Fatalf("two targets = %+v", fail)
	}
	if _, fail := A2UISendArgs(A2UISendFlags{OpenDingTalkID: "user-1", Messages: []string{"a"}}); fail == nil || fail.Reason != "target_type_mismatch" {
		t.Fatalf("bad open id = %+v", fail)
	}
	if _, fail := A2UISendArgs(A2UISendFlags{ChatID: "c"}); fail == nil || fail.ExitCode != 5 {
		t.Fatalf("no messages = %+v", fail)
	}
}

func TestMessageListArgs(t *testing.T) {
	got := MessageListArgs(" cid ", "2026-09-30 11:59:59.123", "older", 21)
	want := map[string]any{"openconversation_id": "cid", "time": "2026-09-30 11:59:59.123", "forward": false, "limit": 21}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("older = %#v", got)
	}
	for direction, forward := range map[string]bool{"newer": true, " NEWER ": true, "": true, "older": false, "sideways": false} {
		if got := MessageListArgs("c", "t", direction, 0); got["forward"] != forward {
			t.Fatalf("direction %q forward = %v", direction, got["forward"])
		}
		if _, ok := MessageListArgs("c", "t", direction, 0)["limit"]; ok {
			t.Fatal("limit sent when 0")
		}
	}
	if got := MessageListArgs("c", "", "", 0); got["forward"] != false || len(got["time"].(string)) != len("2006-01-02 15:04:05") {
		t.Fatalf("default time = %#v", got)
	}
}

func TestCrossOrgArgs(t *testing.T) {
	want := `{"agentCode":"wukong","grantCategory":"data","grantParams":"{\"targetOrgId\":\"*\"}","grantType":"timed","scope":"chat.data:cross-org","ttl":"7d"}`
	if got, _ := json.Marshal(CrossOrgArgs()); string(got) != want {
		t.Fatalf("CrossOrgArgs = %s", got)
	}
}

func TestFailureRenderingOfLiterals(t *testing.T) {
	// A Failure literal without a Reason renders as dws's untyped CLIError.
	untyped := &Failure{Category: "api", Message: `{"code":5}`, ExitCode: 1}
	if got := string(untyped.LegacyJSON()); got != "{\n  \"error\": {\n    \"category\": \"internal\",\n    \"code\": 1,\n    \"message\": \"[MCP_TOOL_ERROR] {\\\"code\\\":5}\"\n  }\n}\n" {
		t.Fatalf("untyped legacy = %s", got)
	}
	if got := string(untyped.UnifiedJSON()); got != "{\n  \"ok\": false,\n  \"outcome\": \"failure\",\n  \"error\": {\n    \"type\": \"api\",\n    \"exit_code\": 1,\n    \"upstream_code\": \"MCP_TOOL_ERROR\",\n    \"message\": \"[MCP_TOOL_ERROR] {\\\"code\\\":5}\"\n  }\n}\n" {
		t.Fatalf("untyped unified = %s", got)
	}
	typed := &Failure{Category: "api", Reason: "business_error", ServerErrorCode: "1001", TraceID: "t", Message: "dup", ExitCode: 1, Server: "chat"}
	legacy := string(typed.LegacyJSON())
	for _, want := range []string{`"reason": "business_error"`, `"server_error_code": "1001"`, `"trace_id": "t"`, `"server_key": "chat"`, `"category": "api"`} {
		if !strings.Contains(legacy, want) {
			t.Fatalf("typed legacy lacks %s:\n%s", want, legacy)
		}
	}
	unified := string(typed.UnifiedJSON())
	for _, want := range []string{`"subtype": "business_error"`, `"upstream_code": "1001"`, `"trace_id": "t"`} {
		if !strings.Contains(unified, want) {
			t.Fatalf("typed unified lacks %s:\n%s", want, unified)
		}
	}
	if strings.Contains(unified, `"category"`) || strings.Contains(unified, `"server_error_code"`) {
		t.Fatalf("unified carries legacy keys:\n%s", unified)
	}
	var nilFailure *Failure
	if nilFailure.Error() != "" || nilFailure.LegacyJSON() != nil || nilFailure.UnifiedJSON() != nil {
		t.Fatal("nil Failure must render nothing")
	}
}
