package llm

import (
	"context"
	"errors"

	openai "github.com/openai/openai-go/v3"
)

// ChatStreamed aggregates the same one-request completion while exposing raw
// chunks to a bounded caller-owned observer. EOF alone is never success.
func (c *Client) ChatStreamed(ctx context.Context, params openai.ChatCompletionNewParams, observe func(openai.ChatCompletionChunk) error) (*openai.ChatCompletion, error) {
	stream, err := c.ChatStream(ctx, params)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	var acc openai.ChatCompletionAccumulator
	for stream.Next() {
		if err := ctx.Err(); err != nil {
			return &acc.ChatCompletion, err
		}
		chunk := stream.Current()
		if !acc.AddChunk(chunk) {
			return &acc.ChatCompletion, errors.New("inconsistent chat stream identity")
		}
		if observe != nil {
			if err := observe(chunk); err != nil {
				return &acc.ChatCompletion, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return &acc.ChatCompletion, err
	}
	if err := ctx.Err(); err != nil {
		return &acc.ChatCompletion, err
	}
	if len(acc.Choices) != 1 || acc.Choices[0].FinishReason == "" {
		return &acc.ChatCompletion, errors.New("chat stream ended without complete finish reason")
	}
	return &acc.ChatCompletion, nil
}
