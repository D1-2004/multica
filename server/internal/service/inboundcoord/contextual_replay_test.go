package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

type contextualReplayCompleter struct {
	observer          *frozenReplayCompleter
	renderModel       string
	renderCurrentOnly bool
	upstreamErrors    []string
}

func (c *contextualReplayCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	if names := toolParamNames(params.Tools); len(names) == 1 && names[0] == toolConversationReplies {
		if c.renderModel != "" {
			params.Model = shared.ChatModel(c.renderModel)
		}
		if c.renderCurrentOnly && len(params.Messages) >= 2 {
			raw, _ := json.Marshal(params.Messages[1])
			var msg struct {
				Content string `json:"content"`
			}
			_ = json.Unmarshal(raw, &msg)
			var input map[string]any
			if json.Unmarshal([]byte(msg.Content), &input) == nil {
				input["history"] = []any{}
				input["history_scope"] = "No history supplied in this controlled replay condition"
				body, _ := json.Marshal(input)
				params.Messages[1] = openai.UserMessage(string(body))
			}
		}
	}
	completion, err := c.observer.Chat(ctx, params)
	if err != nil {
		detail := fmt.Sprintf("%T", err)
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			detail = fmt.Sprintf("HTTP %d: %s", apiErr.StatusCode, apiErr.Message)
		}
		if key := os.Getenv("MULTICA_LLM_API_KEY"); key != "" {
			detail = strings.ReplaceAll(detail, key, "[redacted]")
		}
		c.upstreamErrors = append(c.upstreamErrors, detail)
	}
	return completion, err
}

// Explicitly opted-in model replay with operator-supplied private snapshots.
// No dispatcher, database, checkpoint, live read tool, or message sender exists.
// Route assertions do not replace manual semantic review or IM E2E delivery.
func TestCoordinatorContextualReplay(t *testing.T) {
	if os.Getenv("MULTICA_RUN_CONTEXTUAL_REPLAY") != "1" {
		t.Skip("real model replay requires explicit opt-in")
	}
	path, reportPath := os.Getenv("MULTICA_CONTEXTUAL_REPLAY_FIXTURE"), os.Getenv("MULTICA_CONTEXTUAL_REPLAY_REPORT")
	if path == "" || reportPath == "" {
		t.Skip("private fixture and report paths required")
	}
	var fixture struct {
		TraceID string `json:"trace_id"`
		Turn    Turn   `json:"turn"`
		Recall  string `json:"recall"`
		Cases   []struct {
			ID        string           `json:"id"`
			Message   string           `json:"message"`
			Rename    string           `json:"rename"`
			Proactive bool             `json:"proactive"`
			Work      bool             `json:"work"`
			Ignore    bool             `json:"ignore"`
			Addressed *bool            `json:"addressed"`
			Mentions  []MessageMention `json:"mentions"`
		} `json:"cases"`
	}
	body, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(body, &fixture) != nil {
		t.Fatal("cannot load private replay fixture")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Skip("LLM credentials unavailable")
	}
	type result struct {
		ID             string               `json:"id"`
		RoutePassed    bool                 `json:"route_passed"`
		Reply          string               `json:"reply"`
		Action         Action               `json:"action"`
		Actions        []CoordinationAction `json:"actions"`
		Rounds         []frozenReplayRound  `json:"rounds"`
		Failure        string               `json:"failure,omitempty"`
		UpstreamErrors []string             `json:"upstream_errors,omitempty"`
		ElapsedMS      int64                `json:"elapsed_ms"`
	}
	report := struct {
		TraceID       string   `json:"trace_id"`
		PolicyVersion string   `json:"policy_version"`
		Scope         string   `json:"scope"`
		Results       []result `json:"results"`
	}{TraceID: fixture.TraceID, PolicyVersion: coordinatorPolicy.Version, Scope: "Real-model replay with frozen production identity/configuration/history. No live IM E2E, business execution or external delivery."}
	if len(fixture.Cases) == 0 || len(fixture.Turn.Utterances) == 0 {
		t.Fatal("private fixture requires cases and current utterances")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			started := time.Now()
			turn := fixture.Turn
			turn.Utterances = append([]WindowUtterance(nil), fixture.Turn.Utterances...)
			turn.CoordinationReads = append([]CoordinationRead(nil), fixture.Turn.CoordinationReads...)
			turn.ProactiveConversation = tc.Proactive
			if tc.Addressed != nil {
				turn.Addressed = *tc.Addressed
			}
			if tc.Mentions != nil {
				turn.Utterances[0].Mentions = append([]MessageMention(nil), tc.Mentions...)
			}
			if tc.Message != "" {
				turn.Message = tc.Message
				turn.Utterances[0].Text = tc.Message
			}
			if tc.Rename != "" {
				turn.EmployeeAccountName = tc.Rename
			}
			observer := &frozenReplayCompleter{client: client}
			completer := &contextualReplayCompleter{observer: observer, renderModel: os.Getenv("MULTICA_CONTEXTUAL_RENDER_MODEL"), renderCurrentOnly: os.Getenv("MULTICA_CONTEXTUAL_RENDER_CURRENT_ONLY") == "1"}
			reads := &frozenReplayTools{scene: turn.ConversationID, recall: fixture.Recall}
			ctx, cancel := context.WithTimeout(context.Background(), decisionTimeout)
			d, callErr := (&Coordinator{Chat: completer, Tools: reads}).runLoop(ctx, turn)
			cancel()
			r := result{ID: tc.ID, RoutePassed: true, Reply: d.UserText, Action: d.Action, Actions: d.CoordinationActions, Rounds: observer.rounds, ElapsedMS: time.Since(started).Milliseconds()}
			r.UpstreamErrors = completer.upstreamErrors
			if callErr != nil {
				r.Failure = "loop failed; inspect private model rounds"
			} else if tc.Ignore {
				if d.Action != ActionSilence || len(d.Items) != 0 {
					r.Failure = "other-recipient control was not ignored"
				}
			} else if tc.Work {
				if d.Action != ActionIssue || len(d.Items) == 0 {
					r.Failure = "business request was not delegated"
				}
			} else {
				if d.Action != ActionReply || len(d.Items) != 0 || d.LoopStopFallback() || strings.TrimSpace(d.UserText) == "" {
					r.Failure = "social dialogue did not produce a normal non-work reply"
				}
				if strings.Contains(d.UserText, "我在，看到你的消息了。抱歉，刚才没接上。") {
					r.Failure = "old static receipt repeated"
				}
			}
			if r.Failure != "" {
				r.RoutePassed = false
				t.Error(r.Failure)
			}
			report.Results = append(report.Results, r)
			encoded, _ := json.MarshalIndent(report, "", "  ")
			if os.WriteFile(reportPath, encoded, 0600) != nil {
				t.Fatal("cannot write private replay report")
			}
			t.Logf("case=%s route_passed=%t model_rounds=%d elapsed_ms=%d", tc.ID, r.RoutePassed, len(r.Rounds), r.ElapsedMS)
		})
	}
}

// Repeats an already-selected real conversation action without rerouting it.
// Suggestions versus unsupported autobiography remain a manual semantic check.
func TestCoordinatorConversationFocusedReplay(t *testing.T) {
	if os.Getenv("MULTICA_RUN_CONTEXTUAL_REPLAY") != "1" {
		t.Skip("real model replay requires explicit opt-in")
	}
	path, reportPath := os.Getenv("MULTICA_CONTEXTUAL_REPLAY_FIXTURE"), os.Getenv("MULTICA_CONTEXTUAL_REPLAY_REPORT")
	if path == "" || reportPath == "" {
		t.Skip("private fixture/report paths required")
	}
	var fixture struct {
		TraceID string `json:"trace_id"`
		Turn    Turn   `json:"turn"`
	}
	body, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(body, &fixture) != nil {
		t.Fatal("cannot read private fixture")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: coordinatorModel, MaxRetries: -1})
	if !client.Enabled() {
		t.Skip("LLM credentials unavailable")
	}
	type trial struct {
		Index          int                 `json:"index"`
		Reply          string              `json:"reply"`
		Verdict        string              `json:"verdict"`
		Failure        string              `json:"failure,omitempty"`
		ElapsedMS      int64               `json:"elapsed_ms"`
		Rounds         []frozenReplayRound `json:"rounds"`
		UpstreamErrors []string            `json:"upstream_errors,omitempty"`
	}
	report := struct {
		TraceID       string  `json:"trace_id"`
		PolicyVersion string  `json:"policy_version"`
		Scope         string  `json:"scope"`
		Trials        []trial `json:"trials"`
	}{TraceID: fixture.TraceID, PolicyVersion: coordinatorPolicy.Version, Scope: "Frozen real message/history; three direct renderer generations followed by existing reviewer. Not live IM E2E. Advice versus autobiography requires manual review."}
	for i := 1; i <= 3; i++ {
		started := time.Now()
		observer := &frozenReplayCompleter{client: client}
		completer := &contextualReplayCompleter{observer: observer}
		c := &Coordinator{Chat: completer}
		d := Decision{Action: ActionReply, CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "conversation", SourceRefs: []string{"u1"}}}}
		ctx, cancel := context.WithTimeout(context.Background(), decisionTimeout)
		err := c.renderConversationReplies(ctx, fixture.Turn, &d, 0)
		result := finishCheckResult{}
		if err == nil {
			result, err = c.checkFinish(ctx, fixture.Turn, d, nil, 0, nil)
		}
		cancel()
		r := trial{Index: i, Reply: d.UserText, Verdict: result.Verdict, ElapsedMS: time.Since(started).Milliseconds(), Rounds: observer.rounds, UpstreamErrors: completer.upstreamErrors}
		if err != nil {
			r.Failure = "renderer or review failed; inspect private report"
			t.Error(r.Failure)
		} else if result.Verdict != "allow" {
			r.Failure = "review did not allow candidate"
			t.Error(r.Failure)
		}
		report.Trials = append(report.Trials, r)
		out, _ := json.MarshalIndent(report, "", "  ")
		if os.WriteFile(reportPath, out, 0600) != nil {
			t.Fatal("cannot write private replay report")
		}
		t.Logf("trial=%d verdict=%s elapsed_ms=%d", i, r.Verdict, r.ElapsedMS)
	}
}
