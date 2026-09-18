package inboundcoord

import (
	"context"
	"encoding/json"
	openai "github.com/openai/openai-go/v3"
	"testing"
)

type modelUpdatingCompleter struct {
	inner  *scriptedCompleter
	update func()
}

func (c *modelUpdatingCompleter) Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	c.update()
	return c.inner.Chat(ctx, params)
}
func TestDiamondModelSnapshotAcrossDecisionAndReview(t *testing.T) {
	model := "qwen3.8-max"
	samples := 0
	coordinator := &Coordinator{ModelProvider: func() string { samples++; return model }}
	for _, want := range []string{"qwen3.8-max", "next-model"} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("finish", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好"}]}`)}}
		coordinator.Chat = &modelUpdatingCompleter{inner: chat, update: func() { model = "next-model" }}
		got := coordinator.Decide(context.Background(), Turn{Source: SourceWeb, Message: "你好", Addressed: true})
		if got.Action != ActionReply {
			t.Fatalf("decision: %+v", got)
		}
		if len(chat.params) == 0 || len(chat.checkParams) == 0 {
			t.Fatal("main loop and finish review must both run")
		}
		for _, params := range append(chat.params, chat.checkParams...) {
			if string(params.Model) != want {
				t.Fatalf("model=%s want %s", params.Model, want)
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err = json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["enable_thinking"] != false || payload["reasoning_effort"] != "none" {
				t.Fatalf("thinking settings: %s", raw)
			}
		}
	}
	if samples != 2 {
		t.Fatalf("provider sampled %d times, want once per decision", samples)
	}
}
