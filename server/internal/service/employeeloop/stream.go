package employeeloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	openai "github.com/openai/openai-go/v3"
)

const FirstFeedbackToolName = "first_feedback"

// StreamingModel uses exactly the original provider request. Only a complete
// explicit public feedback frame may be accepted before that request finishes.
// Business tools continue through the ordinary complete-batch path.
type StreamingModel interface {
	ChatStreamed(context.Context, openai.ChatCompletionNewParams, func(openai.ChatCompletionChunk) error) (*openai.ChatCompletion, error)
}

// feedbackObserver never forwards ordinary text, reasoning or business JSON.
// The Host journals its effect independently; replaying the finished native
// call later must recover the same receipt, not send a second message.
func (l *Loop) feedbackObserver(ctx context.Context) func(openai.ChatCompletionChunk) error {
	var acc openai.ChatCompletionAccumulator
	seen := map[string]string{}
	return func(chunk openai.ChatCompletionChunk) error {
		if !acc.AddChunk(chunk) {
			return errors.New("inconsistent feedback stream identity")
		}
		if len(acc.Choices) == 0 {
			return nil
		}
		if len(acc.Choices) != 1 {
			return errors.New("multiple choices in employee stream")
		}
		if l.GetState().ModelCalls != 1 {
			return nil
		}
		tool, available := l.tools.Get(FirstFeedbackToolName)
		if !available {
			return nil
		}
		for _, native := range acc.Choices[0].Message.ToolCalls {
			if native.Type != "function" || native.Function.Name != FirstFeedbackToolName || strings.TrimSpace(native.ID) == "" {
				continue
			}
			var args map[string]any
			if !json.Valid([]byte(native.Function.Arguments)) || json.Unmarshal([]byte(native.Function.Arguments), &args) != nil {
				continue
			}
			valid, _ := l.tools.Validate(tool.Name, args)
			if !valid {
				continue
			}
			if text, _ := args["text"].(string); strings.TrimSpace(text) == "" {
				continue
			}
			if err := l.validatePublicToolText([]ToolCall{{Name: tool.Name, Arguments: args}}); err != nil {
				return err
			}
			normalized, _ := json.Marshal(args)
			if prior, ok := seen[native.ID]; ok {
				if prior != string(normalized) {
					return errors.New("public feedback frame changed after acceptance")
				}
				continue
			}
			if len(seen) > 0 {
				return errors.New("multiple first feedback frames")
			}
			seen[native.ID] = string(normalized)
			if l.host == nil {
				return errors.New("feedback host unavailable")
			}
			_, err := l.host.Execute(ctx, l.input.Identity, ToolCall{Name: tool.Name, NativeToolCallID: native.ID, Arguments: args})
			// A refusal grants no authority or effect. The full response records the
			// paired result normally, and other correctly authorized reads may proceed.
			if err != nil && !errors.Is(err, ErrToolRefused) {
				return err
			}
		}
		return nil
	}
}

func validateFeedbackPlan(calls []ToolCall, first bool) error {
	feedback, read := 0, false
	for _, call := range calls {
		if call.Name == FirstFeedbackToolName {
			feedback++
		}
		switch call.Name {
		case "find_tasks", "read_task", "memory_lookup", "scene_config_get":
			read = true
		}
	}
	if feedback == 0 {
		return nil
	}
	if !first || feedback != 1 || !read {
		return errors.New("first feedback requires the first same-round read plan")
	}
	for _, call := range calls {
		switch call.Name {
		case FirstFeedbackToolName, "find_tasks", "read_task", "memory_lookup", "scene_config_get":
		default:
			return errors.New("first feedback cannot accompany terminal or business effects")
		}
	}
	return nil
}
