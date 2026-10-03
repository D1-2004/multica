package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
	openai "github.com/openai/openai-go/v3"
)

// This opt-in quality experiment reuses the existing regression suite's LLM
// configuration and client. It sends only model requests: no Host tools, DB,
// agent CLI or IM effects. Its raw artifacts require a separate semantic review;
// successful transport is not a quality verdict or proof of delivery.
func TestEmployeeOutputModelReplay(t *testing.T) {
	if os.Getenv("MULTICA_RUN_EMPLOYEE_OUTPUT_REPLAY") != "1" {
		t.Skip("explicit real-model replay opt-in required")
	}
	tracePath, outputDir := os.Getenv("MULTICA_EMPLOYEE_OUTPUT_TRACE"), os.Getenv("MULTICA_EMPLOYEE_OUTPUT_ARTIFACTS")
	if tracePath == "" || outputDir == "" || os.Getenv("MULTICA_LLM_API_KEY") == "" || os.Getenv("MULTICA_LLM_BASE_URL") == "" {
		t.Fatal("replay requires explicit trace, private output directory and existing LLM configuration")
	}
	raw, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal("cannot read source trace")
	}
	var trace struct {
		Data []struct {
			ID           string `json:"id"`
			Observations []struct {
				Name     string          `json:"name"`
				Type     string          `json:"type"`
				Input    json.RawMessage `json:"input"`
				Metadata struct {
					InputTruncated bool `json:"input_truncated"`
				} `json:"metadata"`
			} `json:"observations"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &trace) != nil || len(trace.Data) != 1 {
		t.Fatal("expected one complete captured trace")
	}
	var original map[string]json.RawMessage
	count := 0
	for _, observation := range trace.Data[0].Observations {
		if observation.Name == "employee_model" && observation.Type == "GENERATION" {
			count++
			if observation.Metadata.InputTruncated || json.Unmarshal(observation.Input, &original) != nil {
				t.Fatal("source generation is truncated or malformed")
			}
		}
	}
	if count != 1 {
		t.Fatal("expected exactly one source generation")
	}
	var model string
	if json.Unmarshal(original["model"], &model) != nil || model == "" {
		t.Fatal("source model missing; no fallback model selection")
	}
	if configured := os.Getenv("MULTICA_LLM_DEFAULT_MODEL"); configured != "" && configured != model {
		t.Fatal("configured model differs from the captured model")
	}
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		t.Fatal("cannot create artifact directory")
	}
	if err := os.Chmod(outputDir, 0700); err != nil {
		t.Fatal("cannot protect artifact directory")
	}
	client := llm.New(llm.Config{APIKey: os.Getenv("MULTICA_LLM_API_KEY"), BaseURL: os.Getenv("MULTICA_LLM_BASE_URL"), DefaultModel: model, MaxRetries: -1})
	cases := []struct {
		ID, CurrentRequest string
		AddContract        bool
		Repeats            int
	}{
		{"ds09_baseline", "", false, 1},
		{"ds09_contract", "", true, 1},
		{"identifier_variant", "本轮候选编号为 LM4、ZT8、PQ2，回执显示 LM4 和 PQ2 已收到。漏了哪项？只回编号，不解释。", true, 1},
		{"json_only", "本轮候选编号为 AX6、KV5、RN1，回执显示 AX6 和 RN1 已收到。只返回一个 JSON 对象，以 missing 为键、漏回执编号为字符串值，不要 Markdown 或解释。", true, 1},
		{"number_only", "有 12 箱，每箱 8 件，一共多少件？只回数字。", true, 1},
		{"evidence_in_hand", "发送侧日志：09:00 请求发出，返回 HTTP 200。对方收到了吗？依据这些日志直接判断，一句话回答。", true, 1},
		{"material_ambiguity", "发送侧记录 09:00，接收侧记录 09:08，时限 10 分钟，现在 09:15。超时了吗？", true, 1},
		{"unknown_fact", "我没有提供文件或页数。这份日报有多少页？只回页数。", true, 1},
		{"explanation_requested", "候选编号为 AA1、BB2、CC3，回执只有 AA1 和 CC3。漏了哪项？说明判断依据。", true, 1},
	}
	sum := sha256.Sum256(raw)
	report := map[string]any{"source_trace_id": trace.Data[0].ID, "source_sha256": hex.EncodeToString(sum[:]), "model": model, "semantic_assessment": "not_evaluated", "host_effects": "none"}
	var artifacts []string
	abort := false
	for _, item := range cases {
		for repeat := 0; repeat < item.Repeats; repeat++ {
			var request map[string]json.RawMessage
			originalJSON, _ := json.Marshal(original)
			_ = json.Unmarshal(originalJSON, &request)
			var sourceMessages []map[string]json.RawMessage
			if json.Unmarshal(request["messages"], &sourceMessages) != nil {
				t.Fatal("source messages malformed")
			}
			for _, message := range sourceMessages {
				if len(message) != 2 || message["role"] == nil || message["content"] == nil {
					t.Fatal("replay requires plain role/content messages; never drop captured message fields")
				}
			}
			var messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if json.Unmarshal(request["messages"], &messages) != nil || len(messages) < 2 || messages[0].Role != "system" {
				t.Fatal("source requires a text system prompt and current window")
			}
			if item.AddContract {
				index := strings.LastIndex(messages[0].Content, "\n\nAMBIGUITY:\n")
				if index < 0 {
					index = strings.LastIndex(messages[0].Content, "\n\nLANGUAGE:\n")
				}
				if index < 0 || strings.Contains(messages[0].Content, "REPLY CONTRACT:\n") {
					t.Fatal("source persona insertion point missing or already modified")
				}
				messages[0].Content = messages[0].Content[:index] + "\n\n" + employeePersonaReplyContract + messages[0].Content[index:]
			}
			if item.CurrentRequest != "" {
				if messages[len(messages)-1].Role != "user" {
					t.Fatal("source current window must be the last user message")
				}
				messages[len(messages)-1].Content = item.CurrentRequest
			}
			request["messages"], _ = json.Marshal(messages)
			requestJSON, _ := json.Marshal(request)
			var params openai.ChatCompletionNewParams
			if err := json.Unmarshal(requestJSON, &params); err != nil {
				t.Fatal("cannot deserialize captured provider request")
			}
			extra := map[string]any{}
			for _, field := range []string{"enable_thinking", "thinking"} {
				if value, exists := request[field]; exists {
					var decoded any
					if err := json.Unmarshal(value, &decoded); err != nil {
						t.Fatal("invalid captured request profile")
					}
					extra[field] = decoded
				}
			}
			params.SetExtraFields(extra)
			actualJSON, _ := json.Marshal(params)
			// Preserve exact request semantics; unknown captured fields must not
			// silently disappear through the typed SDK adapter.
			var actual map[string]json.RawMessage
			_ = json.Unmarshal(actualJSON, &actual)
			for key, value := range request {
				var before, after any
				_ = json.Unmarshal(value, &before)
				_ = json.Unmarshal(actual[key], &after)
				left, _ := json.Marshal(before)
				right, _ := json.Marshal(after)
				if string(left) != string(right) {
					t.Fatalf("SDK changed captured field %s", key)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			started := time.Now()
			response, callErr := client.Chat(ctx, params)
			cancel()
			artifact := map[string]any{"case_id": item.ID, "repeat": repeat + 1, "synthetic_current_request": item.CurrentRequest != "", "contract_added": item.AddContract, "request": json.RawMessage(actualJSON), "started_at": started, "duration_ms": time.Since(started).Milliseconds()}
			if callErr != nil {
				artifact["transport_error"] = true
				var apiErr *openai.Error
				if errors.As(callErr, &apiErr) {
					artifact["provider_error"] = map[string]any{"status": apiErr.StatusCode, "code": apiErr.Code, "type": apiErr.Type, "message": strings.ReplaceAll(apiErr.Message, os.Getenv("MULTICA_LLM_API_KEY"), "[redacted]")}
				}
				abort = true
				t.Errorf("%s repeat %d: provider request failed; no quality verdict", item.ID, repeat+1)
			} else {
				artifact["response"] = json.RawMessage(response.RawJSON())
			}
			name := item.ID + "-" + strconv.Itoa(repeat+1) + ".json"
			artifacts = append(artifacts, name)
			data, _ := json.MarshalIndent(artifact, "", "  ")
			if err := os.WriteFile(filepath.Join(outputDir, name), data, 0600); err != nil {
				t.Fatal("cannot save private request/response artifact")
			}
			t.Logf("recorded %s repeat %d", item.ID, repeat+1)
			if abort {
				break
			}
		}
		if abort {
			break
		}
	}
	report["artifacts"] = artifacts
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(outputDir, "report.json"), data, 0600); err != nil {
		t.Fatal("cannot save report")
	}
}
