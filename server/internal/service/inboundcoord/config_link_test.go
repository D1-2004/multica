package inboundcoord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
)

const (
	testConfigLinkURL     = "https://app.multica.example/dingtalk/configure?link=AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcdE"
	testCapabilityReply   = "我可以整理日报、查询会议纪要，也能帮你跟进待办。"
	testConfigLinkContext = `{"dispatch_event_data":{"conversation":{"openConversationId":"cidCapGroup==","type":"group"}}}`
)

type configLinkIssuerStub struct {
	mu       sync.Mutex
	link     ConfigLink
	err      error
	panicMsg string
	wait     bool
	requests []ConfigLinkRequest
}

func (s *configLinkIssuerStub) IssueConfigLink(ctx context.Context, req ConfigLinkRequest) (ConfigLink, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.panicMsg != "" {
		panic(s.panicMsg)
	}
	if s.wait {
		<-ctx.Done()
		return ConfigLink{}, ctx.Err()
	}
	return s.link, s.err
}

func (s *configLinkIssuerStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func sceneConfigLink() ConfigLink {
	return ConfigLink{URL: testConfigLinkURL, Scope: "scene", ValidFor: 30 * time.Minute, ExpiresAt: time.Now().Add(30 * time.Minute)}
}

func personConfigLink() ConfigLink {
	return ConfigLink{URL: testConfigLinkURL, Scope: "person", ValidFor: 15 * time.Minute, SingleUse: true, ExpiresAt: time.Now().Add(15 * time.Minute)}
}

func capabilityTurn(chatType string) Turn {
	return Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: chatType, ConversationID: "cidCapGroup==",
		Message: "你有哪些能力？", SenderName: "冬翔", AgentID: testAgentID(), WorkspaceID: "11111111-1111-1111-1111-111111111111",
		IssueDispatchContext: []byte(testConfigLinkContext), TraceID: "coord-trace-config-link",
	}
}

func capabilityChat(actions string) *scriptedCompleter {
	return &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, actions)}}
}

const capabilityActions = `{"actions":[{"kind":"describe_capabilities","source_refs":["u1"],"reply":"` + testCapabilityReply + `"}]}`

// finishToolSchema returns the finish tool definition the model saw in its
// first routing round, as sent on the wire.
func finishToolSchema(t *testing.T, chat *scriptedCompleter) string {
	t.Helper()
	if len(chat.params) == 0 {
		t.Fatal("no routing request recorded")
	}
	for _, tool := range chat.params[0].Tools {
		if fn := tool.GetFunction(); fn != nil && fn.Name == toolFinish {
			raw, err := json.Marshal(fn.Parameters)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}
	}
	t.Fatal("finish tool missing from the routing request")
	return ""
}

func TestCapabilityAnswerEndsWithConversationConfigLink(t *testing.T) {
	cases := []struct {
		name     string
		chatType string
		link     ConfigLink
		wantLine string
	}{
		{name: "group gets the reusable scene link", chatType: "group", link: sceneConfigLink(), wantLine: "本群能力配置（30 分钟内有效）：" + testConfigLinkURL},
		{name: "1:1 gets the single-use personal link", chatType: "p2p", link: personConfigLink(), wantLine: "你的个人能力配置（15 分钟内有效，限用一次）：" + testConfigLinkURL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(prev)

			issuer := &configLinkIssuerStub{link: tc.link}
			chat := capabilityChat(capabilityActions)
			var saved []Decision
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
			c := &Coordinator{Chat: chat, Tools: &stubTools{}, Queries: &coordQueriesStub{inbound: true}, ConfigLinks: issuer}
			turn := capabilityTurn(tc.chatType)
			decision := c.Decide(ctx, turn)

			want := testCapabilityReply + "\n\n" + tc.wantLine
			if decision.Action != ActionReply || decision.UserText != want {
				t.Fatalf("action=%s text=%q want %q", decision.Action, decision.UserText, want)
			}
			if !strings.HasSuffix(decision.UserText, testConfigLinkURL) {
				t.Fatal("the capability answer must end with the link")
			}
			if len(decision.CoordinationActions) != 1 || decision.CoordinationActions[0].Reply != want {
				t.Fatalf("describe_capabilities reply=%+v", decision.CoordinationActions)
			}
			// The checkpoint is what a redelivered job restores and sends.
			if len(saved) != 1 || saved[0].UserText != want || saved[0].CoordinationActions[0].Reply != want {
				t.Fatalf("checkpoint does not carry the linked answer: %+v", saved)
			}
			if issuer.calls() != 1 {
				t.Fatalf("issuer calls=%d", issuer.calls())
			}
			req := issuer.requests[0]
			if req.WorkspaceID != turn.WorkspaceID || req.AgentID != "01000000-0000-0000-0000-000000000000" ||
				string(req.DispatchContext) != testConfigLinkContext || req.TraceID != "coord-trace-config-link" {
				t.Fatalf("issuer request=%+v", req)
			}
			// The model is told Host owns the link; the reviewer never sees it.
			if !strings.Contains(finishToolSchema(t, chat), "Host appends this chat's capability configuration link") {
				t.Fatal("finish schema does not tell the model Host appends the link")
			}
			if chat.checkCalls != 1 {
				t.Fatalf("finish_check calls=%d", chat.checkCalls)
			}
			for _, params := range chat.checkParams {
				for _, message := range params.Messages {
					raw, _ := message.MarshalJSON()
					if strings.Contains(string(raw), "configure?link=") {
						t.Fatal("the reviewer saw the Host-appended link")
					}
				}
			}
			var linkSteps int
			for _, step := range decision.Steps {
				if step.Tool == toolContextConfigLink {
					linkSteps++
					if step.Error || strings.Contains(step.Output, "configure?link=") {
						t.Fatalf("link step=%+v", step)
					}
				}
			}
			if linkSteps != 2 {
				t.Fatalf("config link steps=%d", linkSteps)
			}
			// Logs record the effect but never the bearer URL.
			if !strings.Contains(logs.String(), `"event":"inbound_coordinator_config_link"`) || !strings.Contains(logs.String(), `"status":"issued"`) {
				t.Fatalf("config link event missing: %s", logs.String())
			}
			if strings.Contains(logs.String(), "configure?link=") {
				t.Fatalf("logs leak the configuration link: %s", logs.String())
			}
		})
	}
}

func TestCapabilityAnswerStaysUnchangedWhenLinkFails(t *testing.T) {
	bad := sceneConfigLink()
	bad.URL = "not a url"
	shared := personConfigLink()
	shared.SingleUse = false
	cases := map[string]*configLinkIssuerStub{
		"issuer error":                  {err: errors.New("This run did not come from a DingTalk group chat, so there is no group to configure.")},
		"issuer panic":                  {panicMsg: "nil pointer"},
		"issuer timeout":                {wait: true},
		"malformed url":                 {link: bad},
		"unknown scope":                 {link: ConfigLink{URL: testConfigLinkURL, Scope: "org", ValidFor: time.Minute}},
		"no lifetime":                   {link: ConfigLink{URL: testConfigLinkURL, Scope: "scene"}},
		"personal link not single use":  {link: shared},
		"unknown conversation, no link": {err: errors.New("This run has no single identifiable DingTalk sender")},
	}
	for name, issuer := range cases {
		t.Run(name, func(t *testing.T) {
			chat := capabilityChat(capabilityActions)
			var saved []Decision
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
			c := &Coordinator{Chat: chat, Tools: &stubTools{}, ConfigLinks: issuer}
			decision, err := c.runLoop(ctx, capabilityTurn("group"))
			if err != nil || decision.Action != ActionReply || decision.UserText != testCapabilityReply {
				t.Fatalf("action=%s text=%q err=%v", decision.Action, decision.UserText, err)
			}
			if len(saved) != 1 || saved[0].UserText != testCapabilityReply {
				t.Fatalf("checkpoint=%+v", saved)
			}
			failed := false
			for _, step := range decision.Steps {
				if step.Tool == toolContextConfigLink && step.Type == "tool_result" {
					failed = step.Error && strings.Contains(step.Output, `"status":"unavailable"`)
				}
			}
			if !failed {
				t.Fatalf("failed link effect is not recorded: %+v", decision.Steps)
			}
		})
	}
}

func TestConfigLinkOnlyForCapabilityAnswersOfEligibleTurns(t *testing.T) {
	// Other kinds never mint a link, even on an eligible turn.
	issuer := &configLinkIssuerStub{link: sceneConfigLink()}
	chat := capabilityChat(`{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"在的，你说。"}]}`)
	decision, err := (&Coordinator{Chat: chat, Tools: &stubTools{}, ConfigLinks: issuer}).runLoop(context.Background(), capabilityTurn("group"))
	if err != nil || decision.UserText != "在的，你说。" || issuer.calls() != 0 {
		t.Fatalf("greeting text=%q calls=%d err=%v", decision.UserText, issuer.calls(), err)
	}
	// The link follows the capability answer when a window mixes kinds.
	issuer = &configLinkIssuerStub{link: sceneConfigLink()}
	turn := capabilityTurn("group")
	turn.Utterances = []WindowUtterance{{Sender: "冬翔", Text: "你好"}, {Sender: "冬翔", Text: "你有哪些能力？"}}
	chat = capabilityChat(`{"actions":[{"kind":"describe_capabilities","source_refs":["u2"],"reply":"` + testCapabilityReply + `"},{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好！"}]}`)
	decision, err = (&Coordinator{Chat: chat, Tools: &stubTools{}, ConfigLinks: issuer}).runLoop(context.Background(), turn)
	want := testCapabilityReply + "\n\n本群能力配置（30 分钟内有效）：" + testConfigLinkURL + "\n\n你好！"
	if err != nil || decision.UserText != want || decision.CoordinationActions[1].Reply != "你好！" {
		t.Fatalf("mixed text=%q err=%v", decision.UserText, err)
	}

	ineligible := map[string]func(*Turn){
		"web chat":            func(t *Turn) { t.Source = SourceWeb },
		"unknown source":      func(t *Turn) { t.Source = "" },
		"no dispatch context": func(t *Turn) { t.IssueDispatchContext = nil },
		"A2UI choice card":    func(t *Turn) { t.UserDecisionEnabled = true },
		"task finished":       func(t *Turn) { t.Loop = LoopTaskFinished },
		"no agent":            func(t *Turn) { t.AgentID.Valid = false },
		"no workspace":        func(t *Turn) { t.WorkspaceID = " " },
	}
	withIssuer := &Coordinator{ConfigLinks: &configLinkIssuerStub{}}
	if !withIssuer.configLinkEligible(capabilityTurn("group")) || !withIssuer.configLinkEligible(capabilityTurn("p2p")) {
		t.Fatal("inbound DingTalk turns with a dispatch context are eligible")
	}
	robot := capabilityTurn("group")
	robot.Source = SourceRobot
	if !withIssuer.configLinkEligible(robot) {
		t.Fatal("robot turns are DingTalk turns too")
	}
	for name, mutate := range ineligible {
		turn := capabilityTurn("group")
		mutate(&turn)
		if withIssuer.configLinkEligible(turn) {
			t.Fatalf("%s must not carry a configuration link", name)
		}
	}
	if (&Coordinator{}).configLinkEligible(capabilityTurn("group")) {
		t.Fatal("a Coordinator without an issuer offers no link")
	}

	// Without an issuer the answer and the schema are exactly as before.
	chat = capabilityChat(capabilityActions)
	decision, err = (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), capabilityTurn("group"))
	if err != nil || decision.UserText != testCapabilityReply {
		t.Fatalf("no-issuer text=%q err=%v", decision.UserText, err)
	}
	if strings.Contains(finishToolSchema(t, chat), "configuration link") {
		t.Fatal("the schema mentions a link the Host will not append")
	}
	for _, step := range decision.Steps {
		if step.Tool == toolContextConfigLink {
			t.Fatal("no issuer, no link step")
		}
	}
}

func TestConfigLinkAbsentWhenCoordinatorIsOff(t *testing.T) {
	issuer := &configLinkIssuerStub{link: sceneConfigLink()}
	chat := capabilityChat(capabilityActions)
	c := &Coordinator{Chat: chat, Tools: &stubTools{}, Queries: &coordQueriesStub{inbound: false}, ConfigLinks: issuer}
	decision := c.Decide(context.Background(), capabilityTurn("group"))
	if decision.Action != ActionContinue || decision.UserText != "" || issuer.calls() != 0 || chat.calls != 0 {
		t.Fatalf("switch-off agent changed: action=%s text=%q issuer=%d model=%d", decision.Action, decision.UserText, issuer.calls(), chat.calls)
	}
}

func TestRestoredCapabilityAnswerKeepsItsLinkWithoutMintingAgain(t *testing.T) {
	saved := Decision{
		Action: ActionReply, PlanVersion: WindowPlanVersion,
		UserText:            testCapabilityReply + "\n\n本群能力配置（30 分钟内有效）：" + testConfigLinkURL,
		CoordinationActions: []CoordinationAction{{Kind: "describe_capabilities", SourceRefs: []string{"u1"}, Reply: testCapabilityReply + "\n\n本群能力配置（30 分钟内有效）：" + testConfigLinkURL}},
	}
	issuer := &configLinkIssuerStub{link: sceneConfigLink()}
	ctx := ContextWithPlanCheckpoint(context.Background(), &saved, func(Decision) error { return nil })
	decision := (&Coordinator{Chat: capabilityChat(capabilityActions), ConfigLinks: issuer}).Decide(ctx, capabilityTurn("group"))
	if decision.UserText != saved.UserText || issuer.calls() != 0 {
		t.Fatalf("restored text=%q issuer=%d", decision.UserText, issuer.calls())
	}
}

func TestConfigLinkLineLanguages(t *testing.T) {
	for language, want := range map[string]string{
		"zh": "本群能力配置（30 分钟内有效）：",
		"en": "Configure this group's capabilities (valid for 30 min): ",
		"ja": "このグループの機能設定（30 分間有効）：",
		"ko": "이 그룹의 기능 설정 (30분간 유효): ",
	} {
		line, err := configLinkLine(language, sceneConfigLink())
		if err != nil || line != want+testConfigLinkURL {
			t.Fatalf("%s line=%q err=%v", language, line, err)
		}
	}
	line, err := configLinkLine("en", personConfigLink())
	if err != nil || line != "Configure your personal capabilities (valid for 15 min, single use): "+testConfigLinkURL {
		t.Fatalf("personal line=%q err=%v", line, err)
	}
	if got := redactConfigLink("see "+testConfigLinkURL, testConfigLinkURL); got != "see [configuration link]" {
		t.Fatalf("redacted=%q", got)
	}
	actions := []CoordinationAction{{Kind: "describe_capabilities", Reply: "x " + testConfigLinkURL}}
	if redacted := redactConfigLinkActions(actions, testConfigLinkURL); redacted[0].Reply != "x [configuration link]" || actions[0].Reply != "x "+testConfigLinkURL {
		t.Fatalf("redaction must copy: %+v / %+v", redacted, actions)
	}
}
