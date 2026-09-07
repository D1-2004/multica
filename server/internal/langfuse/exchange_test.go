package langfuse

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseExchangeChatCompletionJSON(t *testing.T) {
	request := `{"model":"qwen3.7-plus","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"temperature":0.3,"max_completion_tokens":512,"tools":[{"type":"function","function":{"name":"assoc_recall"}}],"tool_choice":"required"}`
	response := `{"id":"c1","object":"chat.completion","model":"qwen3.7-plus-0903","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"assoc_recall","arguments":"{\"since\":\"48h\"}"}}]}}],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":100}}}`
	ex := ParseExchange([]byte(request), []byte(response))
	if ex.API != "chat.completions" || ex.Model != "qwen3.7-plus-0903" || ex.Streamed {
		t.Fatalf("exchange = %+v", ex)
	}
	if ex.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %s", ex.FinishReason)
	}
	if ex.Usage == nil || ex.Usage.Input != 120 || ex.Usage.Output != 30 || ex.Usage.Total != 150 || ex.Usage.CacheRead != 100 {
		t.Fatalf("usage = %+v", ex.Usage)
	}
	if got := mustJSON(t, ex.ModelParameters); !strings.Contains(got, `"temperature":0.3`) || !strings.Contains(got, `"tools":1`) || !strings.Contains(got, `"tool_names":["assoc_recall"]`) {
		t.Fatalf("model parameters = %s", got)
	}
	if got := mustJSON(t, ex.Input); !strings.Contains(got, `"role":"user"`) {
		t.Fatalf("input = %s", got)
	}
	if got := mustJSON(t, ex.Output); !strings.Contains(got, `"name":"assoc_recall"`) {
		t.Fatalf("output = %s", got)
	}
}

func TestParseExchangeChatCompletionSSE(t *testing.T) {
	request := `{"model":"gpt-5.6","messages":[{"role":"user","content":"hi"}],"stream":true}`
	response := strings.Join([]string{
		`data: {"object":"chat.completion.chunk","model":"gpt-5.6","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		``,
		`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		``,
		`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"fin","arguments":"{\"a\":"}}]}}]}`,
		``,
		`data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
		``,
		`data: {"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	ex := ParseExchange([]byte(request), []byte(response))
	if !ex.Streamed || ex.API != "chat.completions" || ex.FinishReason != "tool_calls" {
		t.Fatalf("exchange = %+v", ex)
	}
	out := mustJSON(t, ex.Output)
	if !strings.Contains(out, `"content":"Hello"`) || !strings.Contains(out, `"arguments":"{\"a\":1}"`) || !strings.Contains(out, `"id":"call_9"`) {
		t.Fatalf("output = %s", out)
	}
	if ex.Usage == nil || ex.Usage.Input != 7 || ex.Usage.Output != 3 {
		t.Fatalf("usage = %+v", ex.Usage)
	}
}

func TestParseExchangeResponsesSSE(t *testing.T) {
	request := `{"model":"gpt-5.6-codex","instructions":"be brief","input":[{"role":"user","content":"do it"}],"reasoning":{"effort":"medium"},"tools":[{"type":"function","name":"shell"}]}`
	response := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"par"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"r1","status":"completed","model":"gpt-5.6-codex","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial done"}]}],"usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":8}}}}`,
		``,
	}, "\n")
	ex := ParseExchange([]byte(request), []byte(response))
	if ex.API != "responses" || ex.Model != "gpt-5.6-codex" || ex.FinishReason != "completed" {
		t.Fatalf("exchange = %+v", ex)
	}
	if got := mustJSON(t, ex.Input); !strings.Contains(got, `"instructions":"be brief"`) {
		t.Fatalf("input = %s", got)
	}
	if got := mustJSON(t, ex.Output); !strings.Contains(got, `partial done`) {
		t.Fatalf("output = %s", got)
	}
	if ex.Usage == nil || ex.Usage.Input != 50 || ex.Usage.Output != 20 || ex.Usage.CacheRead != 40 || ex.Usage.Reasoning != 8 {
		t.Fatalf("usage = %+v", ex.Usage)
	}
	if got := mustJSON(t, ex.ModelParameters); !strings.Contains(got, `"tool_names":["shell"]`) || !strings.Contains(got, `"reasoning"`) {
		t.Fatalf("model parameters = %s", got)
	}
}

func TestParseExchangeAnthropicSSE(t *testing.T) {
	request := `{"model":"claude-sonnet-5","system":"sys","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`
	response := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":5}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there"}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"bash","input":{}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	}, "\n")
	ex := ParseExchange([]byte(request), []byte(response))
	if ex.API != "anthropic.messages" || ex.FinishReason != "tool_use" || !ex.Streamed {
		t.Fatalf("exchange = %+v", ex)
	}
	out := mustJSON(t, ex.Output)
	if !strings.Contains(out, `"content":"Hi there"`) || !strings.Contains(out, `"name":"bash"`) || !strings.Contains(out, `{\"cmd\":\"ls\"}`) {
		t.Fatalf("output = %s", out)
	}
	if ex.Usage == nil || ex.Usage.Input != 25 || ex.Usage.Output != 12 || ex.Usage.CacheRead != 5 {
		t.Fatalf("usage = %+v", ex.Usage)
	}
	if got := mustJSON(t, ex.Input); !strings.Contains(got, `"system":"sys"`) {
		t.Fatalf("input = %s", got)
	}
}

func TestParseExchangeAnthropicJSON(t *testing.T) {
	response := `{"id":"m2","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2,"cache_creation_input_tokens":1}}`
	ex := ParseExchange([]byte(`{"model":"claude-opus-5","system":"s","messages":[]}`), []byte(response))
	if ex.API != "anthropic.messages" || ex.FinishReason != "end_turn" || ex.Usage == nil || ex.Usage.CacheWrite != 1 {
		t.Fatalf("exchange = %+v usage=%+v", ex, ex.Usage)
	}
}

func TestParseExchangeErrorAndFallbacks(t *testing.T) {
	ex := ParseExchange([]byte(`{"model":"m","messages":[]}`), []byte(`{"error":{"message":"rate limited","type":"rate_limit"}}`))
	if ex.Error == nil || !strings.Contains(mustJSON(t, ex.Output), "rate limited") {
		t.Fatalf("error exchange = %+v", ex)
	}
	raw := ParseExchange([]byte("not json"), []byte("<html>bad gateway</html>"))
	if raw.API != "unknown" || raw.Input != "not json" || raw.Output != "<html>bad gateway</html>" {
		t.Fatalf("raw exchange = %+v", raw)
	}
	empty := ParseExchange(nil, nil)
	if empty.Input != nil || empty.Output != nil || empty.ModelParameters != nil {
		t.Fatalf("empty exchange = %+v", empty)
	}
	stream := ParseExchange([]byte(`{"model":"m","messages":[]}`), []byte("data: {\"error\":{\"message\":\"upstream closed\"}}\n\n"))
	if stream.Error == nil || !strings.Contains(mustJSON(t, stream.Output), "upstream closed") {
		t.Fatalf("stream error exchange = %+v", stream)
	}
}
