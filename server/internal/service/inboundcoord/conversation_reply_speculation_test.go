package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
)

func speculationTurn(texts ...string) Turn {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "p2p", DWSUID: "24710833", ConversationID: "cid-spec", HistoryStatus: "loaded"}
	for _, text := range texts {
		turn.Utterances = append(turn.Utterances, WindowUtterance{Text: text})
	}
	turn.Message = texts[0]
	return turn
}

func renderRound(id, reply string) openai.ChatCompletion {
	return assistantTool(id, toolConversationReplies, `{"replies":[{"action_ref":"a1","reply":"`+reply+`"}]}`)
}

// The speculative render is only reusable because its request is identical to
// the one the routed proposal produces. Hash equality is that guarantee.
func TestConversationReplyRequestIdentityFollowsSelectedActions(t *testing.T) {
	turn := speculationTurn("Hi")
	hypothesis := speculativeConversationDecision(turn)
	if len(hypothesis.CoordinationActions) != 1 || !equalRefs(hypothesis.CoordinationActions[0].SourceRefs, []string{"u1"}) {
		t.Fatalf("hypothesis must cover every source owed an answer: %+v", hypothesis)
	}
	speculated, err := conversationReplyRequestFor(turn, hypothesis)
	if err != nil || speculated == nil {
		t.Fatalf("hypothesis request: %v", err)
	}
	routed, err := conversationReplyRequestFor(turn, Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1"}}}})
	if err != nil || routed == nil || routed.inputHash != speculated.inputHash {
		t.Fatalf("identical selection must ask an identical question: %v", err)
	}
	other, err := conversationReplyRequestFor(speculationTurn("Hi", "顺便问个事"), Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1", "u2"}}}})
	if err != nil || other == nil || other.inputHash == speculated.inputHash {
		t.Fatalf("a different source set must not reuse the speculation: %v", err)
	}
	if _, err := conversationReplyRequestFor(turn, Decision{CoordinationActions: []CoordinationAction{{Kind: "start_work", SourceRefs: []string{"u1"}}}}); err != nil {
		t.Fatalf("work-only proposals have no render request: %v", err)
	}
}

func equalRefs(got, want []string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// renderFirstCompleter refuses to answer a routing round until a render request
// has already been issued. Counting calls alone cannot tell a reused
// speculation from an ordinary render; holding routing open can.
type renderFirstCompleter struct {
	inner    *scriptedCompleter
	rendered chan struct{}
	once     sync.Once
	mu       sync.Mutex
	stalled  bool
}

func (c *renderFirstCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	names := toolParamNames(params.Tools)
	isRender := len(names) == 1 && names[0] == toolConversationReplies
	// Holding routing open until a render has been issued both proves the
	// speculation ran early and fixes which scripted render each call takes.
	if !isRender && len(names) > 0 && names[0] != toolFinishCheck {
		select {
		case <-c.rendered:
		case <-time.After(5 * time.Second):
			c.mu.Lock()
			c.stalled = true
			c.mu.Unlock()
		}
	}
	completion, err := c.inner.Chat(ctx, params)
	if isRender {
		c.once.Do(func() { close(c.rendered) })
	}
	return completion, err
}

func (c *renderFirstCompleter) speculationStalled(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stalled {
		t.Fatal("the render must be in flight while routing is still deciding")
	}
}

// awaitSpeculation joins the worker so an unconsumed speculation is counted.
func (c *renderFirstCompleter) awaitSpeculation(t *testing.T) {
	t.Helper()
	select {
	case <-c.rendered:
	case <-time.After(5 * time.Second):
		t.Fatal("no render request was ever issued")
	}
}

func TestConversationReplySpeculationRunsBesideRoutingAndServesAMatchingProposal(t *testing.T) {
	inner := &scriptedCompleter{
		rounds:             []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"actions":[{"kind":"acknowledge","ack_kind":"conversation","source_refs":["u1"],"reply":"草稿"}]}`)},
		conversationRounds: []openai.ChatCompletion{renderRound("speculative", "我在，你说。"), renderRound("second", "不应该走到这一轮。")},
		checkRounds:        []openai.ChatCompletion{scriptedFinishVerdict("allow", "Greeting answered from the current source.")},
	}
	chat := &renderFirstCompleter{inner: inner, rendered: make(chan struct{})}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), speculationTurn("Hi"))
	chat.speculationStalled(t)
	if err != nil || d.Action != ActionReply || d.UserText != "我在，你说。" {
		t.Fatalf("speculative reply must be the rendered one: d=%+v err=%v", d, err)
	}
	if got := inner.conversationCallCount(); got != 1 {
		t.Fatalf("a matching proposal must not re-ask the render: calls=%d", got)
	}
}

func TestConversationReplySpeculationIsDiscardedOnADifferentProposal(t *testing.T) {
	inner := &scriptedCompleter{
		rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"actions":[{"kind":"acknowledge","ack_kind":"conversation","source_refs":["u1"],"reply":"草稿一"},{"kind":"acknowledge","ack_kind":"conversation","source_refs":["u2"],"reply":"草稿二"}]}`)},
		conversationRounds: []openai.ChatCompletion{
			renderRound("speculative", "只回了一句。"),
			assistantTool("routed", toolConversationReplies, `{"replies":[{"action_ref":"a1","reply":"第一件事回复。"},{"action_ref":"a2","reply":"第二件事回复。"}]}`),
		},
		checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Both current sources are answered.")},
	}
	chat := &renderFirstCompleter{inner: inner, rendered: make(chan struct{})}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), speculationTurn("Hi", "顺便问个事"))
	chat.speculationStalled(t)
	if err != nil || d.Action != ActionReply {
		t.Fatalf("routing must still decide: d=%+v err=%v", d, err)
	}
	if got := inner.conversationCallCount(); got != 2 || !strings.Contains(d.UserText, "第一件事回复") || !strings.Contains(d.UserText, "第二件事回复") {
		t.Fatalf("a different selection must be rendered for real: calls=%d text=%q", got, d.UserText)
	}
	if strings.Contains(d.UserText, "只回了一句") {
		t.Fatal("a discarded speculation must never reach the reply")
	}
}

// A speculative failure is not the turn's verdict. The same question is asked
// again on the normal path and only that answer counts.
func TestConversationReplySpeculationFailureIsRenderedAgain(t *testing.T) {
	inner := &scriptedCompleter{
		rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"actions":[{"kind":"acknowledge","ack_kind":"conversation","source_refs":["u1"],"reply":"草稿"}]}`)},
		conversationRounds: []openai.ChatCompletion{
			assistantTool("broken", toolConversationReplies, `{"replies":[{"action_ref":"a9","reply":"错的引用。"}]}`),
			renderRound("recovered", "我在，你说。"),
		},
		checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Greeting answered from the current source.")},
	}
	chat := &renderFirstCompleter{inner: inner, rendered: make(chan struct{})}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), speculationTurn("Hi"))
	chat.speculationStalled(t)
	if got := inner.conversationCallCount(); err != nil || d.UserText != "我在，你说。" || got != 2 {
		t.Fatalf("failed speculation must fall through to a real render: d=%+v calls=%d err=%v", d, got, err)
	}
}

// greeting and thanks acknowledgements never enter the render, so their
// speculation is spent and discarded. The cost is reported, not hidden.
func TestConversationReplySpeculationIsUnusedForGreetingAcknowledgements(t *testing.T) {
	inner := &scriptedCompleter{
		rounds:             []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"actions":[{"kind":"acknowledge","ack_kind":"greeting","source_refs":["u1"],"reply":"我在。"}]}`)},
		conversationRounds: []openai.ChatCompletion{renderRound("speculative", "不会被使用。")},
		checkRounds:        []openai.ChatCompletion{scriptedFinishVerdict("allow", "Greeting acknowledged.")},
	}
	chat := &renderFirstCompleter{inner: inner, rendered: make(chan struct{})}
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(context.Background(), speculationTurn("Hi"))
	chat.speculationStalled(t)
	chat.awaitSpeculation(t)
	if err != nil || d.UserText != "我在。" {
		t.Fatalf("greeting keeps the routed reply: d=%+v err=%v", d, err)
	}
	if got := inner.conversationCallCount(); got != 1 {
		t.Fatalf("exactly one discarded speculation is expected: calls=%d", got)
	}
}

// The render stopped using thinking mode, which is what returns it to the
// forced response tool the Host validates.
func TestConversationReplyRequestsTheResponseToolWithoutThinking(t *testing.T) {
	chat := &scriptedCompleter{conversationRounds: []openai.ChatCompletion{renderRound("render", "我在，你说。")}}
	d := Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1"}}}}
	if err := (&Coordinator{Chat: chat}).renderConversationReplies(context.Background(), speculationTurn("Hi"), &d, 0, nil); err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(chat.conversationParams) != 1 {
		t.Fatalf("one render request expected: %d", len(chat.conversationParams))
	}
	body, err := json.Marshal(chat.conversationParams[0])
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	var wire struct {
		EnableThinking *bool  `json:"enable_thinking"`
		ToolChoice     string `json:"tool_choice"`
		Model          string `json:"model"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if wire.EnableThinking == nil || *wire.EnableThinking || wire.ToolChoice != "required" || wire.Model != conversationReplyModel {
		t.Fatalf("render must ask the bounded model for its response tool without thinking: %s", body)
	}
}
