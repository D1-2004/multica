package inboundcoord

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// wireRecorder is a model endpoint that keeps every request body it receives
// and answers routing with an acknowledge plan the review allows.
type wireRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *wireRecorder) routing() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]byte
	for _, body := range r.bodies {
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)
		if len(req.Tools) == 1 && (req.Tools[0].Function.Name == toolConversationReplies || req.Tools[0].Function.Name == "finish_check") {
			continue
		}
		out = append(out, body)
	}
	return out
}

func (r *wireRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func recordingLLM(t *testing.T) (*llm.Client, *wireRecorder) {
	t.Helper()
	recorder := &wireRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		recorder.mu.Lock()
		recorder.bodies = append(recorder.bodies, raw)
		recorder.mu.Unlock()
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name       string         `json:"name"`
					Parameters map[string]any `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		if len(body.Tools) == 1 && body.Tools[0].Function.Name == toolConversationReplies {
			_, _ = io.WriteString(w, `{"id":"cmpl-render","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"r1","type":"function","function":{"name":"render_conversation_replies","arguments":"{\"replies\":[{\"action_ref\":\"a1\",\"reply\":\"我在。\"}]}"}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		if len(body.Tools) == 1 && body.Tools[0].Function.Name == "finish_check" {
			requestRef, candidateRef := scriptedReferenceEnums(body.Tools[0].Function.Parameters)
			_ = json.NewEncoder(w).Encode(withScriptedFinishReferences(scriptedFinishVerdict("allow", "Scripted shadow fixture allows the candidate."), body.Messages[len(body.Messages)-1].Content, requestRef, candidateRef))
			return
		}
		_, _ = io.WriteString(w, `{"id":"cmpl-2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"f1","type":"function","function":{"name":"finish","arguments":"{\"actions\":[{\"kind\":\"acknowledge\",\"source_refs\":[\"u1\"],\"ack_kind\":\"greeting\",\"reply\":\"我在。\"}]}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	t.Cleanup(server.Close)
	return llm.New(llm.Config{APIKey: "test", BaseURL: server.URL}), recorder
}

const shadowJobID = "7483ac52-ed51-441a-a2f5-3221dcf011d8"

func shadowTurn(cutoff time.Time) Turn {
	turn := windowTurn(cutoff)
	turn.Message = "6 点"
	turn.TraceID = shadowJobID
	return turn
}

func shadowCoordinator(t *testing.T, loader DingTalkHistoryLoader) (*Coordinator, *wireRecorder) {
	t.Helper()
	client, recorder := recordingLLM(t)
	c := &Coordinator{LLM: client, DWSHistory: loader, windowHistory: newWindowHistoryReads(), firstRoundShadows: newFirstRoundShadows()}
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return true }
	return c, recorder
}

func sameHistory(n int) *scriptedHistory {
	answers := make([][]HistoryLine, n)
	for i := range answers {
		answers[i] = []HistoryLine{{Role: "菲迪", Content: "晚上几点出发？", EvidenceID: "m1"}}
	}
	return &scriptedHistory{answers: answers}
}

// builtShadow starts the window read and the shadow for turn and waits for
// the shadow to finish.
func builtShadow(t *testing.T, c *Coordinator, loader *scriptedHistory, turn Turn) *firstRoundShadow {
	t.Helper()
	if !c.PrefetchWindowHistory(turn) {
		t.Fatal("the window read must start")
	}
	c.ShadowFirstRound(turn)
	c.firstRoundShadows.mu.Lock()
	entry := c.firstRoundShadows.entries[firstRoundShadowKey(turn)]
	c.firstRoundShadows.mu.Unlock()
	if entry == nil {
		t.Fatal("the shadow must start for a pilot turn")
	}
	select {
	case <-entry.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the shadow must finish")
	}
	return entry
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

func claimContext() context.Context {
	return ContextWithTraceID(firstClaim(), shadowJobID)
}

func TestFirstRoundShadowIsTheRequestTheClaimSends(t *testing.T) {
	logs := captureLogs(t)
	client, exporter := langfuseTestClient(t)
	loader := sameHistory(2)
	c, recorder := shadowCoordinator(t, loader)
	c.Langfuse = client
	// A recalled Issue makes the first round disclose work_state with its id.
	c.Tools = &stubTools{recall: `{"items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"确认晚上几点出发","status":"queued","on_this_scene":true}]}`}
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	entry := builtShadow(t, c, loader, shadowTurn(cutoff))
	if entry.skipped != "" || entry.request.full == "" {
		t.Fatalf("the shadow must build the request: skipped=%q", entry.skipped)
	}

	// The shadow calls no model, starts no trace, logs no Coordinator event
	// and reads DingTalk no further than the window read it looked at.
	if recorder.count() != 0 {
		t.Fatalf("the shadow must not call the model, requests=%d", recorder.count())
	}
	if err := client.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spans := exporter.GetSpans(); len(spans) != 0 {
		t.Fatalf("the shadow must not trace, spans=%d", len(spans))
	}
	for _, event := range []string{"inbound_coordinator_history_prefetch", "inbound_coordinator_scene_prefetch", "scene_memory_recall_injected", "inbound_coordinator_llm_request", "inbound_coordinator_decided"} {
		if strings.Contains(logs.String(), event) {
			t.Fatalf("the shadow must not log %s: %s", event, logs.String())
		}
	}
	if loader.count() != 1 {
		t.Fatalf("the shadow must only look at the window read, loads=%d", loader.count())
	}

	c.Decide(claimContext(), shadowTurn(cutoff))
	routing := recorder.routing()
	if len(routing) == 0 {
		t.Fatal("the claimed decision must send its first request")
	}
	var wire map[string]any
	if err := json.Unmarshal(routing[0], &wire); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(wire)
	if hashBytes(canonical) != entry.request.full {
		t.Fatalf("the shadow must equal the first request on the wire:\nshadow %s\nwire   %s", entry.request.full, hashBytes(canonical))
	}
	out := logs.String()
	for _, want := range []string{"event=inbound_coordinator_speculation_shadow", "outcome=same", "unexplained=false", "window=hit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the claim must report %q: %s", want, out)
		}
	}
}

func TestFirstRoundShadowNamesTheInputThatMoved(t *testing.T) {
	logs := captureLogs(t)
	loader := sameHistory(2)
	c, _ := shadowCoordinator(t, loader)
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	builtShadow(t, c, loader, shadowTurn(cutoff))
	// A task of the agent started between the window and the claim.
	claimed := shadowTurn(cutoff)
	claimed.Busy = true
	c.Decide(claimContext(), claimed)
	out := logs.String()
	for _, want := range []string{"outcome=diff", "diff_inputs=busy", "unexplained=false"} {
		if !strings.Contains(out, want) {
			t.Fatalf("a moved input must be named, want %q: %s", want, out)
		}
	}
}

func TestClaimWithoutItsShadowReportsUnavailable(t *testing.T) {
	logs := captureLogs(t)
	loader := sameHistory(3)
	c, _ := shadowCoordinator(t, loader)
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	builtShadow(t, c, loader, shadowTurn(cutoff))
	// A second message merged into the job moved its cutoff: the shadow of
	// the earlier window is not this claim's.
	merged := shadowTurn(cutoff.Add(time.Second))
	merged.Utterances = append(merged.Utterances, WindowUtterance{Text: "晚一点", EvidenceID: "msg-3", Timestamp: cutoff.Add(time.Second)})
	c.Decide(claimContext(), merged)
	if out := logs.String(); !strings.Contains(out, "outcome=unavailable") {
		t.Fatalf("a claim without its shadow must say so: %s", out)
	}
}

func TestFirstRoundShadowStaysInThePilotScope(t *testing.T) {
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	for name, change := range map[string]func(*Turn){
		"group chat":       func(turn *Turn) { turn.ChatType = "group" },
		"user decision":    func(turn *Turn) { turn.UserDecisionEnabled = true },
		"task finished":    func(turn *Turn) { turn.Loop = LoopTaskFinished },
		"proactive":        func(turn *Turn) { turn.ProactiveConversation = true },
		"no job trace id":  func(turn *Turn) { turn.TraceID = "" },
		"no message":       func(turn *Turn) { turn.Message = "" },
		"web conversation": func(turn *Turn) { turn.Source = SourceWeb },
	} {
		t.Run(name, func(t *testing.T) {
			loader := sameHistory(2)
			c, _ := shadowCoordinator(t, loader)
			turn := shadowTurn(cutoff)
			c.PrefetchWindowHistory(turn)
			change(&turn)
			c.ShadowFirstRound(turn)
			if n := len(c.firstRoundShadows.entries); n != 0 {
				t.Fatalf("no shadow outside the pilot scope, have %d", n)
			}
		})
	}
	t.Run("switch off", func(t *testing.T) {
		loader := sameHistory(2)
		c, _ := shadowCoordinator(t, loader)
		turn := shadowTurn(cutoff)
		c.PrefetchWindowHistory(turn)
		c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { return false }
		c.ShadowFirstRound(turn)
		if n := len(c.firstRoundShadows.entries); n != 0 {
			t.Fatalf("no shadow when the switch is off, have %d", n)
		}
	})
	t.Run("no window read", func(t *testing.T) {
		loader := sameHistory(2)
		c, _ := shadowCoordinator(t, loader)
		c.ShadowFirstRound(shadowTurn(cutoff))
		if n := len(c.firstRoundShadows.entries); n != 0 || loader.count() != 0 {
			t.Fatalf("the shadow never reads DingTalk itself: shadows=%d loads=%d", n, loader.count())
		}
	})
}

func TestRequestFingerprintIsCanonicalAndSectioned(t *testing.T) {
	c := &Coordinator{}
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("system"), openai.UserMessage("user")}
	tools := []openai.ChatCompletionToolUnionParam{contextReadTool()}
	first := ""
	for i := 0; i < 20; i++ {
		params, err := c.wireParams("qwen3.7-plus", messages, tools, 4096, temperature, shared.ReasoningEffortNone)
		if err != nil {
			t.Fatal(err)
		}
		request, err := fingerprintRequest(params)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = request.full
		} else if request.full != first {
			t.Fatal("the fingerprint must not depend on map order")
		}
	}
	base, _ := c.wireParams("qwen3.7-plus", messages, tools, 4096, temperature, shared.ReasoningEffortNone)
	changed, _ := c.wireParams("qwen3.7-plus", []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("system"), openai.UserMessage("other")}, tools, 4096, temperature, shared.ReasoningEffortNone)
	a, _ := fingerprintRequest(base)
	b, _ := fingerprintRequest(changed)
	if got := differingKeys(a.sections, b.sections); strings.Join(got, ",") != "messages" || a.full == b.full {
		t.Fatalf("only the user messages differ, got %v", got)
	}
	budget, _ := c.wireParams("qwen3.7-plus", messages, tools, 1536, temperature, shared.ReasoningEffortNone)
	d, _ := fingerprintRequest(budget)
	if got := differingKeys(a.sections, d.sections); strings.Join(got, ",") != "route" {
		t.Fatalf("a budget change is a route change, got %v", got)
	}
}

// windowedRecall answers recall like the association service: the scope is
// the read's own window ending now, the items are what it found.
type windowedRecall struct {
	mu    sync.Mutex
	reads int
}

func (w *windowedRecall) Call(_ context.Context, _ Turn, name, _ string) (string, error) {
	if name != toolAssocRecall {
		return `{}`, nil
	}
	w.mu.Lock()
	w.reads++
	until := time.Date(2026, 9, 29, 1, 0, w.reads, 0, time.UTC)
	w.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{
		"status": "loaded", "conversation_id": "cid-real",
		"scope": map[string]any{"since": until.Add(-48 * time.Hour).Format(time.RFC3339), "until": until.Format(time.RFC3339)},
		"items": []map[string]any{{"issue_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "purpose": "确认晚上几点出发", "status": "queued", "on_this_scene": true}},
	})
	return string(raw), nil
}

func TestFirstRoundShadowSeparatesTheRecallWindowFromItsItems(t *testing.T) {
	logs := captureLogs(t)
	loader := sameHistory(2)
	c, _ := shadowCoordinator(t, loader)
	c.Tools = &windowedRecall{}
	cutoff := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	builtShadow(t, c, loader, shadowTurn(cutoff))
	c.Decide(claimContext(), shadowTurn(cutoff))
	out := logs.String()
	for _, want := range []string{"outcome=diff", "diff_inputs=recall_scope", "unexplained=false"} {
		if !strings.Contains(out, want) {
			t.Fatalf("a recall read later in time must be named as its window, want %q: %s", want, out)
		}
	}
}
