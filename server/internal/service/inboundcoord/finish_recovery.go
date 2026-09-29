package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/langfuse"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

var errFinishSerialization = errors.New("finish serialization recovery failed")

const loopStopFinishSerialization = "finish_serialization_exhausted"

func (c *Coordinator) finishRecoveryEnabled() bool {
	if c == nil {
		return false
	}
	if c.FinishRecoveryProvider != nil {
		return c.FinishRecoveryProvider()
	}
	return c.finishRecovery
}
func (c *Coordinator) completionBudget() int64 {
	if c.finishRecoveryEnabled() {
		return 4096
	}
	return maxCompletionTokens
}
func isDeepSeekFlash(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "deepseek") && strings.Contains(model, "flash")
}

func needsFinishSerializationRepair(completion *openai.ChatCompletion) bool {
	if completion == nil || len(completion.Choices) != 1 {
		return false
	}
	choice := completion.Choices[0]
	calls := functionToolCalls(choice.Message)
	if len(calls) == 1 && calls[0].Name == toolFinish {
		return !json.Valid([]byte(calls[0].Arguments))
	}
	return len(calls) == 0 && choice.FinishReason == "length"
}

// A repair advertises only finish, preserves the evidence snapshot, and never
// commits a proposal. The caller applies exactly the normal Host/review path.
func (c *Coordinator) repairFinishSerialization(ctx context.Context, turn Turn, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam, recalled bool, completion *openai.ChatCompletion, repairs, modelCalls *int) (*openai.ChatCompletion, error) {
	var finishTools []openai.ChatCompletionToolUnionParam
	for _, tool := range tools {
		if tool.OfFunction != nil && tool.OfFunction.Function.Name == toolFinish {
			finishTools = append(finishTools, tool)
		}
	}
	if len(finishTools) != 1 {
		return nil, fmt.Errorf("%w: finish serialization recovery has no unique finish tool", errFinishSerialization)
	}
	for needsFinishSerializationRepair(completion) {
		if *repairs >= 2 {
			return nil, fmt.Errorf("%w: finish serialization recovery exhausted", errFinishSerialization)
		}
		previous := completion.Choices[0].Message.Content
		if calls := functionToolCalls(completion.Choices[0].Message); len(calls) == 1 {
			previous = calls[0].Arguments
		}
		diagnostic, _ := json.Marshal(map[string]any{"previous_finish": clipRunes(previous, 6000), "error": "finish serialization incomplete; no action has been committed"})
		retry := append([]openai.ChatCompletionMessageParamUnion(nil), messages...)
		repairTurn := turn
		repairTurn.FinishProtocolRepair = true
		retry[0] = openai.SystemMessage(buildSystemPromptForStage(repairTurn, recalled))
		retry = append(retry, openai.UserMessage("Host serialization diagnostic (not new user evidence):\n"+string(diagnostic)))
		*repairs++
		*modelCalls++
		trace := langfuse.TraceFromContext(ctx)
		observation := traceRoundGeneration(trace, *modelCalls-1, retry, finishTools, c.configuredModel(), c.completionBudget(), true)
		params, err := c.wireParams(c.configuredModel(), retry, finishTools, c.completionBudget(), temperature, shared.ReasoningEffortNone)
		if err == nil {
			c.logFinishSchemaRequest(turn, "finish_repair", *modelCalls-1, params)
			completion, err = c.sendParams(ctx, params)
		}
		endRoundGeneration(observation, completion, err)
		if trace != nil {
			manifest := policyManifestForStage(repairTurn, recalled)
			trace.AddMetadata(map[string]any{"finish_serialization_repairs": *repairs, "finish_repair_prompt_hash": manifest.PromptHash, "finish_repair_modules": manifest.Modules})
		}
		if err != nil {
			return nil, err
		}
		if completion == nil || len(completion.Choices) != 1 {
			return nil, fmt.Errorf("%w: finish serialization recovery returned no unique choice", errFinishSerialization)
		}
		msg := completion.Choices[0].Message
		normalizeToolCallTypes(&msg)
		calls := functionToolCalls(msg)
		if len(calls) != 1 || calls[0].Name != toolFinish {
			return nil, fmt.Errorf("%w: finish serialization recovery must return only finish", errFinishSerialization)
		}
	}
	return completion, nil
}

// The shared Host field allow-list constrains the already expanded wire schema.
// Unused fields are not offered merely because another action kind needs them.
func pruneFinishSchemas(tools []openai.ChatCompletionToolUnionParam) {
	for _, tool := range tools {
		if tool.OfFunction == nil || tool.OfFunction.Function.Name != toolFinish {
			continue
		}
		var visit func(any)
		visit = func(value any) {
			switch v := value.(type) {
			case map[string]any:
				if props, ok := v["properties"].(map[string]any); ok {
					if kind, ok := props["kind"].(map[string]any); ok {
						if names, ok := kind["enum"].([]any); ok && len(names) == 1 {
							name, _ := names[0].(string)
							allowed, _ := coordinationAllowedFields(name, LoopInbound)
							if allowed != nil {
								for key := range props {
									if !allowed[key] {
										delete(props, key)
									}
								}
							}
						}
					}
				}
				for _, entry := range v {
					visit(entry)
				}
			case []any:
				for _, entry := range v {
					visit(entry)
				}
			}
		}
		visit(map[string]any(tool.OfFunction.Function.Parameters))
	}
}
