package langfuse

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Exchange is one model request/response pair captured by a runtime proxy,
// normalized into the fields a Langfuse generation needs. The parser is
// tolerant: it understands OpenAI Chat Completions, the OpenAI Responses API,
// and Anthropic Messages, both as plain JSON and as server-sent event streams,
// and falls back to the raw (clipped) bodies for anything else.
type Exchange struct {
	// API names the recognized wire protocol: chat.completions, responses,
	// anthropic.messages, or unknown.
	API             string
	Model           string
	Input           any
	Output          any
	ModelParameters map[string]any
	Usage           *Usage
	Streamed        bool
	FinishReason    string
	// Error is the upstream error object when the provider rejected the call.
	Error any
}

// maxExchangeRawBytes bounds a raw fallback body kept on the observation.
const maxExchangeRawBytes = 32 << 10

// ParseExchange normalizes a request body and its response body.
func ParseExchange(request, response []byte) Exchange {
	ex := Exchange{API: "unknown", ModelParameters: map[string]any{}}
	ex.parseRequest(request)
	ex.parseResponse(response)
	if len(ex.ModelParameters) == 0 {
		ex.ModelParameters = nil
	}
	return ex
}

func (ex *Exchange) parseRequest(body []byte) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		ex.Input = clipString(string(body), maxExchangeRawBytes)
		return
	}
	if model, ok := req["model"].(string); ok {
		ex.Model = strings.TrimSpace(model)
	}
	for _, key := range []string{
		"temperature", "top_p", "max_tokens", "max_completion_tokens", "max_output_tokens",
		"reasoning_effort", "reasoning", "tool_choice", "parallel_tool_calls", "stream",
		"response_format", "stop", "presence_penalty", "frequency_penalty", "seed",
		"service_tier", "thinking", "enable_thinking",
	} {
		if value, ok := req[key]; ok && value != nil {
			ex.ModelParameters[key] = value
		}
	}
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		ex.ModelParameters["tools"] = len(tools)
		ex.ModelParameters["tool_names"] = toolNames(tools)
	}
	switch {
	case req["messages"] != nil && req["system"] != nil:
		ex.API = "anthropic.messages"
		ex.Input = map[string]any{"system": req["system"], "messages": req["messages"]}
	case req["messages"] != nil:
		ex.API = "chat.completions"
		ex.Input = req["messages"]
	case req["input"] != nil:
		ex.API = "responses"
		input := map[string]any{"input": req["input"]}
		if instructions, ok := req["instructions"]; ok && instructions != nil {
			input["instructions"] = instructions
		}
		ex.Input = input
	default:
		ex.Input = req
	}
}

func toolNames(tools []any) []string {
	names := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				names = append(names, name)
				continue
			}
		}
		if name, ok := tool["name"].(string); ok && name != "" {
			names = append(names, name)
			continue
		}
		if kind, ok := tool["type"].(string); ok && kind != "" {
			names = append(names, kind)
		}
	}
	if len(names) > 40 {
		names = names[:40]
	}
	return names
}

func (ex *Exchange) parseResponse(body []byte) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return
	}
	if looksLikeSSE(body) {
		ex.Streamed = true
		ex.parseSSE(body)
		return
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		ex.Output = clipString(string(body), maxExchangeRawBytes)
		return
	}
	ex.applyResponseObject(resp)
}

func looksLikeSSE(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.HasPrefix(head, []byte("data:")) || bytes.HasPrefix(head, []byte("event:")) ||
		bytes.Contains(head, []byte("\ndata:")) || bytes.Contains(head, []byte("\nevent:"))
}

// applyResponseObject handles a complete (non-streamed) provider response.
func (ex *Exchange) applyResponseObject(resp map[string]any) {
	if errObj, ok := resp["error"]; ok && errObj != nil {
		ex.Error = errObj
		ex.Output = map[string]any{"error": errObj}
	}
	if model, ok := resp["model"].(string); ok && strings.TrimSpace(model) != "" {
		ex.Model = strings.TrimSpace(model)
	}
	switch {
	case resp["choices"] != nil:
		if ex.API == "unknown" {
			ex.API = "chat.completions"
		}
		if choices, ok := resp["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				if msg, ok := choice["message"]; ok {
					ex.Output = msg
				}
				if reason, ok := choice["finish_reason"].(string); ok {
					ex.FinishReason = reason
				}
			}
		}
		ex.Usage = openAIUsage(resp["usage"])
	case resp["output"] != nil:
		if ex.API == "unknown" {
			ex.API = "responses"
		}
		ex.Output = resp["output"]
		if status, ok := resp["status"].(string); ok {
			ex.FinishReason = status
		}
		ex.Usage = openAIUsage(resp["usage"])
	case resp["content"] != nil && resp["role"] != nil:
		if ex.API == "unknown" {
			ex.API = "anthropic.messages"
		}
		ex.Output = map[string]any{"role": resp["role"], "content": resp["content"]}
		if reason, ok := resp["stop_reason"].(string); ok {
			ex.FinishReason = reason
		}
		ex.Usage = anthropicUsage(resp["usage"])
	default:
		if ex.Output == nil {
			ex.Output = resp
		}
	}
}

// sseAccumulator folds streamed deltas back into one assistant message.
type sseAccumulator struct {
	content   strings.Builder
	reasoning strings.Builder
	toolCalls []map[string]any
	toolIndex map[int]int
	role      string
}

func (a *sseAccumulator) addToolCall(index int, delta map[string]any) {
	if a.toolIndex == nil {
		a.toolIndex = map[int]int{}
	}
	pos, ok := a.toolIndex[index]
	if !ok {
		a.toolCalls = append(a.toolCalls, map[string]any{
			"index": index, "type": "function",
			"function": map[string]any{"name": "", "arguments": ""},
		})
		pos = len(a.toolCalls) - 1
		a.toolIndex[index] = pos
	}
	call := a.toolCalls[pos]
	if id, ok := delta["id"].(string); ok && id != "" {
		call["id"] = id
	}
	fn, _ := call["function"].(map[string]any)
	if deltaFn, ok := delta["function"].(map[string]any); ok && fn != nil {
		if name, ok := deltaFn["name"].(string); ok && name != "" {
			fn["name"] = fn["name"].(string) + name
		}
		if args, ok := deltaFn["arguments"].(string); ok && args != "" {
			fn["arguments"] = fn["arguments"].(string) + args
		}
	}
}

func (a *sseAccumulator) message() map[string]any {
	msg := map[string]any{"role": "assistant"}
	if a.role != "" {
		msg["role"] = a.role
	}
	if a.content.Len() > 0 {
		msg["content"] = a.content.String()
	}
	if a.reasoning.Len() > 0 {
		msg["reasoning_content"] = a.reasoning.String()
	}
	if len(a.toolCalls) > 0 {
		msg["tool_calls"] = a.toolCalls
	}
	return msg
}

func (ex *Exchange) parseSSE(body []byte) {
	acc := &sseAccumulator{}
	sawChatChunk := false
	var anthropicUsageIn, anthropicUsageOut, anthropicCacheRead, anthropicCacheWrite int64
	sawAnthropic := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}
		if errObj, ok := event["error"]; ok && errObj != nil {
			ex.Error = errObj
		}
		eventType, _ := event["type"].(string)
		switch {
		case event["choices"] != nil:
			sawChatChunk = true
			if ex.API == "unknown" {
				ex.API = "chat.completions"
			}
			if model, ok := event["model"].(string); ok && model != "" {
				ex.Model = model
			}
			if usage := openAIUsage(event["usage"]); usage != nil {
				ex.Usage = usage
			}
			choices, _ := event["choices"].([]any)
			for _, raw := range choices {
				choice, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
					ex.FinishReason = reason
				}
				delta, _ := choice["delta"].(map[string]any)
				if delta == nil {
					continue
				}
				if role, ok := delta["role"].(string); ok && role != "" {
					acc.role = role
				}
				if content, ok := delta["content"].(string); ok {
					acc.content.WriteString(content)
				}
				if reasoning, ok := delta["reasoning_content"].(string); ok {
					acc.reasoning.WriteString(reasoning)
				}
				if calls, ok := delta["tool_calls"].([]any); ok {
					for _, rawCall := range calls {
						call, ok := rawCall.(map[string]any)
						if !ok {
							continue
						}
						index := 0
						if f, ok := call["index"].(float64); ok {
							index = int(f)
						}
						acc.addToolCall(index, call)
					}
				}
			}
		case strings.HasPrefix(eventType, "response."):
			if ex.API == "unknown" {
				ex.API = "responses"
			}
			switch eventType {
			case "response.completed", "response.incomplete", "response.failed", "response.done":
				if resp, ok := event["response"].(map[string]any); ok {
					ex.applyResponseObject(resp)
				}
			}
		case eventType == "message_start" || eventType == "content_block_delta" ||
			eventType == "message_delta" || eventType == "content_block_start":
			sawAnthropic = true
			if ex.API == "unknown" {
				ex.API = "anthropic.messages"
			}
			switch eventType {
			case "message_start":
				if msg, ok := event["message"].(map[string]any); ok {
					if model, ok := msg["model"].(string); ok && model != "" {
						ex.Model = model
					}
					if usage := anthropicUsage(msg["usage"]); usage != nil {
						anthropicUsageIn, anthropicCacheRead, anthropicCacheWrite = usage.Input, usage.CacheRead, usage.CacheWrite
					}
				}
			case "content_block_start":
				if block, ok := event["content_block"].(map[string]any); ok {
					if block["type"] == "tool_use" {
						acc.toolCalls = append(acc.toolCalls, map[string]any{
							"type": "tool_use", "id": block["id"], "name": block["name"], "input": "",
						})
					}
				}
			case "content_block_delta":
				if delta, ok := event["delta"].(map[string]any); ok {
					if text, ok := delta["text"].(string); ok {
						acc.content.WriteString(text)
					}
					if thinking, ok := delta["thinking"].(string); ok {
						acc.reasoning.WriteString(thinking)
					}
					if partial, ok := delta["partial_json"].(string); ok && len(acc.toolCalls) > 0 {
						last := acc.toolCalls[len(acc.toolCalls)-1]
						if existing, ok := last["input"].(string); ok {
							last["input"] = existing + partial
						}
					}
				}
			case "message_delta":
				if delta, ok := event["delta"].(map[string]any); ok {
					if reason, ok := delta["stop_reason"].(string); ok {
						ex.FinishReason = reason
					}
				}
				if usage := anthropicUsage(event["usage"]); usage != nil {
					anthropicUsageOut = usage.Output
					if usage.Input > 0 {
						anthropicUsageIn = usage.Input
					}
				}
			}
		}
	}
	switch {
	case sawChatChunk:
		ex.Output = acc.message()
	case sawAnthropic:
		ex.Output = acc.message()
		ex.Usage = &Usage{Input: anthropicUsageIn, Output: anthropicUsageOut, CacheRead: anthropicCacheRead, CacheWrite: anthropicCacheWrite}
		if ex.Usage.empty() {
			ex.Usage = nil
		}
	case ex.Output == nil:
		if ex.Error != nil {
			ex.Output = map[string]any{"error": ex.Error}
		} else {
			ex.Output = clipString(string(body), maxExchangeRawBytes)
		}
	}
}

func openAIUsage(raw any) *Usage {
	usage, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := &Usage{
		Input:  firstInt(usage, "prompt_tokens", "input_tokens"),
		Output: firstInt(usage, "completion_tokens", "output_tokens"),
		Total:  firstInt(usage, "total_tokens"),
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		out.CacheRead = firstInt(details, "cached_tokens")
	}
	if details, ok := usage["input_tokens_details"].(map[string]any); ok {
		out.CacheRead = firstInt(details, "cached_tokens")
	}
	if details, ok := usage["completion_tokens_details"].(map[string]any); ok {
		out.Reasoning = firstInt(details, "reasoning_tokens")
	}
	if details, ok := usage["output_tokens_details"].(map[string]any); ok {
		out.Reasoning = firstInt(details, "reasoning_tokens")
	}
	if out.empty() {
		return nil
	}
	return out
}

func anthropicUsage(raw any) *Usage {
	usage, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := &Usage{
		Input:      firstInt(usage, "input_tokens"),
		Output:     firstInt(usage, "output_tokens"),
		CacheRead:  firstInt(usage, "cache_read_input_tokens"),
		CacheWrite: firstInt(usage, "cache_creation_input_tokens"),
	}
	if out.empty() {
		return nil
	}
	return out
}

func firstInt(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		}
	}
	return 0
}
