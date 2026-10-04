package modelregistry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeStreamedAttemptKeepsFrozenModelAndCurrentAuthorization(t *testing.T) {
	r, snapshot := employeeConfiguredRegistry(t)
	ctx := context.Background()
	plan, err := r.CoordinatorPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := snapshot.Config
	cfg.Coordinator = []Ref{{"second", "model-b"}}
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "first" {
			cfg.Providers[i].BaseURL = "https://rotated.example.test/v2"
			cfg.Providers[i].APIKey = "STREAM_ROTATION_FIXTURE"
		}
	}
	snapshot, err = r.Save(ctx, cfg, "rotation")
	if err != nil {
		t.Fatal(err)
	}
	var requests, chunks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		if req.URL.Path != "/v2/chat/completions" || req.Header.Get("Authorization") != "Bearer STREAM_ROTATION_FIXTURE" || body["model"] != "model-a" || body["stream"] != true {
			t.Error("stream changed frozen identity or used stale authorization")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"model-a-served\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"model-a-served\",\"choices\":[],\"usage\":{\"prompt_tokens\":31,\"completion_tokens\":7,\"total_tokens\":38}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	r.coordinatorTransport = employeeTestTransport(t, srv, "rotated.example.test")
	attempt, err := r.PrepareCoordinatorAttempt(ctx, plan, 0)
	if err != nil {
		t.Fatal(err)
	}
	streamed, ok := attempt.(interface {
		ChatStreamed(context.Context, openai.ChatCompletionNewParams, func(openai.ChatCompletionChunk) error) (*openai.ChatCompletion, error)
	})
	if !ok {
		t.Fatal("prepared attempt lost streaming capability")
	}
	out, err := streamed.ChatStreamed(ctx, openai.ChatCompletionNewParams{Model: "model-a", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")}}, func(openai.ChatCompletionChunk) error { chunks.Add(1); return nil })
	if err != nil || requests.Load() != 1 || chunks.Load() != 2 || out.Model != "model-a-served" || out.Usage.TotalTokens != 38 || out.Choices[0].Message.Content != "ok" {
		t.Fatal(out, err, requests.Load(), chunks.Load())
	}
	if _, err = streamed.ChatStreamed(ctx, openai.ChatCompletionNewParams{Model: "model-b"}, nil); err == nil || requests.Load() != 1 {
		t.Fatal("mismatched model triggered I/O", err, requests.Load())
	}
	cfg = snapshot.Config
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "first" {
			cfg.Providers[i].Enabled = false
		}
	}
	if _, err = r.Save(ctx, cfg, "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.PrepareCoordinatorAttempt(ctx, plan, 0); err == nil || requests.Load() != 1 {
		t.Fatal("revoked provider reached I/O", err, requests.Load())
	}
}
