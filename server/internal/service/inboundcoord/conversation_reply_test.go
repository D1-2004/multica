package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"
)

type conversationReplyCompleter struct {
	completion openai.ChatCompletion
	err        error
	params     []openai.ChatCompletionNewParams
	deadline   time.Time
}

func (f *conversationReplyCompleter) Chat(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.params = append(f.params, p)
	f.deadline, _ = ctx.Deadline()
	return &f.completion, f.err
}

func TestConversationReplyStageExcludesRoutingDataAndPreservesWork(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "single", DWSUID: "employee", PersonID: "sender", Message: "反复就一句话？", Instructions: "private-job-marker", Persona: "private-persona-marker", SceneMemory: "private-memory-marker", ReplyTone: "简短自然", DingTalkHistory: []HistoryLine{{Role: "employee", SenderID: "opaque-other", Content: "unknown-history-marker"}, {SenderID: "sender", Content: "原来的回复"}}}
	d := Decision{Action: ActionIssue, Items: []WindowItem{{Purpose: "保持工作", Reply: "工作接单"}}, CoordinationActions: []CoordinationAction{{Kind: "start_work", SourceRefs: []string{"u1"}, Purpose: "保持工作", Reply: "工作接单"}, {Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1"}, Reply: "private-bad-candidate-marker"}}}
	beforeItems := append([]WindowItem(nil), d.Items...)
	beforeWork := d.CoordinationActions[0]
	f := &conversationReplyCompleter{completion: assistantTool("response", toolConversationReplies, `{"replies":[{"action_ref":"a2","reply":"你说得对，我没回答到你的问题。"}]}`)}
	started := time.Now()
	if err := (&Coordinator{Chat: f}).renderConversationReplies(context.Background(), turn, &d, 2); err != nil {
		t.Fatal(err)
	}
	if len(f.params) != 1 || string(f.params[0].Model) != conversationReplyModel || !reflect.DeepEqual(toolParamNames(f.params[0].Tools), []string{toolConversationReplies}) {
		t.Fatal("renderer exposed more than one response tool")
	}
	if f.deadline.Sub(started) > conversationReplyTimeout+time.Second || f.params[0].MaxCompletionTokens.Value != conversationReplyTokenBudget {
		t.Fatal("renderer exceeded bounded request budget")
	}
	raw, _ := json.Marshal(f.params[0].Messages)
	if len(f.params[0].Messages) != 3 {
		t.Fatal("renderer must separate policy, background and current request")
	}
	last, _ := json.Marshal(f.params[0].Messages[2])
	background, _ := json.Marshal(f.params[0].Messages[1])
	if !strings.Contains(string(last), "current_actions") || strings.Contains(string(last), "原来的回复") || !strings.Contains(string(background), "原来的回复") || strings.Contains(string(background), "反复就一句话") {
		t.Fatal("current request must follow historical data in its own segment")
	}
	for _, marker := range []string{"private-job-marker", "private-persona-marker", "private-memory-marker", "private-bad-candidate-marker", "unknown-history-marker", "保持工作"} {
		if strings.Contains(string(raw), marker) {
			t.Fatalf("routing/candidate data leaked: %s", marker)
		}
	}
	if !reflect.DeepEqual(d.Items, beforeItems) || !reflect.DeepEqual(d.CoordinationActions[0], beforeWork) || d.Action != ActionIssue {
		t.Fatal("renderer changed work plan")
	}
	if d.UserText != "工作接单\n\n你说得对，我没回答到你的问题。" {
		t.Fatalf("replies not aggregated in action order: %q", d.UserText)
	}
}

func TestConversationHistoryUsesStableIDsAndStrictBudget(t *testing.T) {
	turn := Turn{DWSUID: "employee", PersonID: "sender", Message: "你好", DingTalkHistory: []HistoryLine{{Role: "员工", SenderID: "other", Content: "unknown"}, {SenderID: "employee", Content: "self"}, {SenderID: "sender", Content: "human"}}}
	_, history := conversationReplyInput(turn, Decision{})
	if len(history) != 2 || history[0].Author != "known_employee" || history[1].Author != "known_sender" {
		t.Fatalf("incorrect identity inference: %+v", history)
	}
	turn.DingTalkHistory = nil
	for i := 0; i < 10; i++ {
		turn.DingTalkHistory = append(turn.DingTalkHistory, HistoryLine{SenderID: "sender", Content: strings.Repeat("字", 350)})
	}
	_, history = conversationReplyInput(turn, Decision{})
	total := 0
	for _, h := range history {
		total += utf8.RuneCountInString(h.Text)
	}
	if len(history) > 6 || total > conversationHistoryBudget || !history[len(history)-1].Truncated {
		t.Fatalf("history budget exceeded: count=%d chars=%d", len(history), total)
	}
}

func TestConversationReplyKeepsEachSourceSpeaker(t *testing.T) {
	turn := Turn{PersonID: "window-last", Utterances: []WindowUtterance{{Text: "first", SenderID: "first"}, {Text: "second", SenderID: "second"}, {Text: "missing"}}, DingTalkHistory: []HistoryLine{{SenderID: "first", Content: "older first"}, {SenderID: "second", Content: "older second"}, {SenderID: "window-last", Content: "not a known window sender"}}}
	d := Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1", "u3"}}, {Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u2"}}}}
	actions, history := conversationReplyInput(turn, d)
	if actions[0].Sources[0].SenderID != "first" || actions[0].Sources[1].SenderID != "" || actions[1].Sources[0].SenderID != "second" {
		t.Fatalf("window identity bled into sources: %+v", actions)
	}
	if len(history) != 2 || history[0].Author != "known_sender" || history[0].SenderID != "first" || history[1].SenderID != "second" {
		t.Fatalf("history author mapping guessed: %+v", history)
	}
}

func TestConversationReplyRejectsInvalidOutputAtomically(t *testing.T) {
	for _, raw := range []string{
		`{"replies":[]}`,
		`{"replies":[{"action_ref":"a2","reply":"错位"}]}`,
		`{"replies":[{"action_ref":"a1","reply":"一个"},{"action_ref":"a1","reply":"重复"}]}`,
		`{"replies":[{"action_ref":"a1","reply":"   "}]}`,
		`{"replies":[{"action_ref":"a1","reply":"正文","purpose":"不允许"}]}`,
		`{"replies":[{"action_ref":"a1","reply":"正文"}]} {}`,
		`{"replies":[{"action_ref":"a1","reply":"` + strings.Repeat("字", 601) + `"}]}`,
	} {
		d := Decision{UserText: "原聚合", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1"}, Reply: "原回复"}}}
		f := &conversationReplyCompleter{completion: assistantTool("response", toolConversationReplies, raw)}
		if err := (&Coordinator{Chat: f}).renderConversationReplies(context.Background(), Turn{Message: "问题"}, &d, 0); err == nil {
			t.Fatalf("accepted invalid renderer response: %s", raw)
		}
		if d.UserText != "原聚合" || d.CoordinationActions[0].Reply != "原回复" {
			t.Fatal("invalid output partially mutated candidate")
		}
	}
}

func TestConversationReplySkipsOtherKindsAndPropagatesFailure(t *testing.T) {
	f := &conversationReplyCompleter{err: errors.New("unavailable")}
	c := &Coordinator{Chat: f}
	d := Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "greeting", Reply: "你好"}}}
	if err := c.renderConversationReplies(context.Background(), Turn{}, &d, 0); err != nil || len(f.params) != 0 {
		t.Fatal("greeting invoked conversational renderer")
	}
	d.CoordinationActions[0].AckKind = "conversation"
	d.CoordinationActions[0].SourceRefs = []string{"u1"}
	if err := c.renderConversationReplies(context.Background(), Turn{Loop: LoopTaskFinished}, &d, 0); err != nil || len(f.params) != 0 {
		t.Fatal("completion invoked conversational renderer")
	}
	if err := c.renderConversationReplies(context.Background(), Turn{Message: "问题"}, &d, 0); err == nil || len(f.params) != 1 || d.CoordinationActions[0].Reply != "你好" {
		t.Fatal("renderer error failed open or changed reply")
	}
}
