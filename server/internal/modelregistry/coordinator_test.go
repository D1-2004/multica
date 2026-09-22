package modelregistry

import (
	"context"
	"errors"
	openai "github.com/openai/openai-go/v3"
	"strings"
	"testing"
)

func TestRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   bool
	}{{400, "provider_error", true}, {400, "invalid_request_error", false}, {401, "", true}, {403, "", true}, {404, "", true}, {429, "", true}, {500, "", true}, {422, "", false}} {
		if got := Retryable(&openai.Error{StatusCode: tc.status, Code: tc.code}); got != tc.want {
			t.Fatalf("status=%d code=%s got=%v", tc.status, tc.code, got)
		}
	}
	if Retryable(context.Canceled) || Retryable(errors.New("plan rejected")) {
		t.Fatal("cancellation and business rejection must not trigger model fallback")
	}
}
func TestQualifiedModelsPreserveProviderAndSlash(t *testing.T) {
	s := Snapshot{Config: Config{AgentModels: []Ref{{"mass", "qwen"}, {"bailian", "org/model"}}, Providers: []Provider{{ID: "mass", Enabled: true, Models: []string{"qwen"}}, {ID: "bailian", Enabled: true, Models: []string{"org/model"}}}}}
	for _, v := range []string{"qwen", "mass/qwen", "bailian/org/model"} {
		ref, e := s.AgentRef(v)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, e = s.Resolve(ref); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.AgentRef("unknown/qwen"); e == nil {
		t.Fatal("unknown provider accepted")
	}
	s.Config.Providers[1].Enabled = false
	if _, _, e := s.Resolve(Ref{"bailian", "org/model"}); e == nil {
		t.Fatal("disabled provider accepted")
	}
}
func TestProviderURLBoundary(t *testing.T) {
	for _, u := range []string{"http://example.com/v1", "https://user:key@example.com/v1", "https://127.0.0.1/v1", "https://169.254.169.254/latest", "https://localhost/v1", "https://example.com/v1?key=x", "https://[::1]/v1"} {
		if ValidateURL(u) == nil {
			t.Fatalf("accepted unsafe URL %s", u)
		}
	}
	if e := ValidateURL("https://api-deap.dingtalk.com/deapai"); e != nil {
		t.Fatal(e)
	}
}
func TestCrossProviderFallbackPreservesRequestAndSticks(t *testing.T) {
	r := &Route{Snapshot: Snapshot{Config: Config{Revision: 7, Coordinator: []Ref{{"mass", "a"}, {"bailian", "b"}}}}}
	var calls []string
	r.call = func(ctx context.Context, ref Ref, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls = append(calls, ref.String())
		if string(p.Model) != ref.Model {
			t.Fatal("wrong upstream model")
		}
		if len(p.Messages) != 1 {
			t.Fatal("context changed")
		}
		raw, e := p.MarshalJSON()
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(raw), `"tool_choice":"required"`) {
			t.Fatal("tool choice rewritten")
		}
		if ref.Provider == "mass" {
			return nil, &openai.Error{StatusCode: 400, Code: "provider_error"}
		}
		return &openai.ChatCompletion{}, nil
	}
	p := openai.ChatCompletionNewParams{Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("frozen input")}}
	p.SetExtraFields(map[string]any{"tool_choice": "required"})
	for i := 0; i < 2; i++ {
		if _, e := r.Chat(context.Background(), p); e != nil {
			t.Fatal(e)
		}
	}
	if strings.Join(calls, ",") != "mass/a,bailian/b,bailian/b" {
		t.Fatalf("unexpected calls %v", calls)
	}
}
func TestFallbackIsBoundedAndStopsOnCancellation(t *testing.T) {
	r := &Route{Snapshot: Snapshot{Config: Config{Coordinator: []Ref{{"one", "a"}, {"two", "b"}}}}}
	calls := 0
	r.call = func(context.Context, Ref, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		return nil, &openai.Error{StatusCode: 503}
	}
	if _, e := r.Chat(context.Background(), openai.ChatCompletionNewParams{}); e == nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", e, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := r.Chat(ctx, openai.ChatCompletionNewParams{}); !errors.Is(e, context.Canceled) || calls != 2 {
		t.Fatal("canceled request consumed another candidate")
	}
}
