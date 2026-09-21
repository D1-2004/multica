package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

type providerFailureCompleter struct {
	calls      []openai.ChatCompletionNewParams
	failure    error
	alwaysFail bool
}

func (f *providerFailureCompleter) Chat(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	f.calls = append(f.calls, p)
	if len(f.calls) == 1 || f.alwaysFail {
		return nil, f.failure
	}
	return &openai.ChatCompletion{Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "This is not a valid tool plan."}}}}, nil
}
func providerParameterError(status int, code string) *openai.Error {
	return &openai.Error{StatusCode: status, Code: code, Message: "<400> InternalError.Algo: An error occurred in model serving, error message is: [Invalid request parameters.]"}
}
func TestProviderToolChoiceFallback(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		err         error
		always      bool
		want        int
	}{
		{"known_qwen_parameter_failure", "qwen3.8-max", providerParameterError(400, "provider_error"), false, 2},
		{"bounded_retry", "qwen3.8-max", providerParameterError(400, "provider_error"), true, 2},
		{"other_provider", "other-model", providerParameterError(400, "provider_error"), false, 1},
		{"auth_failure", "qwen3.8-max", providerParameterError(401, "provider_error"), false, 1},
		{"other_parameter_failure", "qwen3.8-max", providerParameterError(400, "invalid_request_error"), false, 1},
		{"network_failure", "qwen3.8-max", errors.New("network unavailable"), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &providerFailureCompleter{failure: tc.err, alwaysFail: tc.always}
			c := &Coordinator{Chat: f}
			_, _ = c.completeWithModelLimit(context.Background(), tc.model, []openai.ChatCompletionMessageParamUnion{openai.UserMessage("synthetic request")}, nil, 256, 0.3, shared.ReasoningEffortNone)
			if len(f.calls) != tc.want {
				t.Fatalf("calls=%d want=%d", len(f.calls), tc.want)
			}
			if tc.want == 2 {
				a, _ := json.Marshal(f.calls[0])
				b, _ := json.Marshal(f.calls[1])
				var first, second map[string]any
				_ = json.Unmarshal(a, &first)
				_ = json.Unmarshal(b, &second)
				if first["tool_choice"] != "required" || second["tool_choice"] != "auto" {
					t.Fatal("wrong tool choice fallback")
				}
				delete(first, "tool_choice")
				delete(second, "tool_choice")
				a, _ = json.Marshal(first)
				b, _ = json.Marshal(second)
				if string(a) != string(b) {
					t.Fatal("fallback changed context, tools or model parameters")
				}
			}
		})
	}
}
func TestProviderFallbackTextCannotBecomeUserDecisionPlan(t *testing.T) {
	f := &providerFailureCompleter{failure: providerParameterError(400, "provider_error")}
	c := &Coordinator{Chat: f, model: "qwen3.8-max"}
	d, err := c.proposeUserDecision(context.Background(), Turn{Message: "synthetic request", Addressed: true}, nil, nil, nil, Decision{})
	if err == nil || d.UserDecision != nil || len(f.calls) != 2 {
		t.Fatalf("text response must fail closed: error=%v calls=%d", err, len(f.calls))
	}
}
