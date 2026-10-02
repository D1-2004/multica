package employeeloop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestPromptPreservesTrustedInstructionsWithoutTruncation(t *testing.T) {
	instructions := "  负责工程排查与任务协调。\n" + strings.Repeat("保留配置中的岗位约束。\n", 1600) + "最终限制：不能替用户审批资金。  "
	persona := testConfig().Persona
	persona.Instructions = instructions
	prompt := BuildPrompt(persona)
	if !strings.Contains(prompt, instructions) {
		t.Fatal("trusted instructions were omitted, normalized or truncated")
	}
	if !strings.Contains(prompt, "EMPLOYEE RESPONSIBILITIES AND CONSTRAINTS:") || !strings.Contains(prompt, "do not expand Host permissions or capabilities") {
		t.Fatal("configured instructions lack a bounded authority section")
	}
	if prompt != BuildPrompt(persona) {
		t.Fatal("trusted instructions made prompt unstable")
	}
	for _, preserved := range []string{persona.Name, persona.Personality, persona.Tone} {
		if !strings.Contains(prompt, preserved) {
			t.Fatalf("persona/voice omitted: %q", preserved)
		}
	}
}

func TestConfiguredInstructionsAreSystemButConversationRemainsData(t *testing.T) {
	config := testConfig()
	config.Persona.Instructions = "Trusted duties sentinel: review engineering reports."
	input := testInput()
	input.CurrentWindow = "Window injection sentinel"
	input.Memory = "Memory injection sentinel"
	input.TaskBrief = "Task injection sentinel"
	model := modelFunc(func(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		wire, err := json.Marshal(params.Messages)
		if err != nil {
			t.Fatal(err)
		}
		var messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(wire, &messages); err != nil {
			t.Fatal(err)
		}
		systemCount := 0
		for _, message := range messages {
			if message.Role == "system" {
				systemCount++
				if !strings.Contains(message.Content, config.Persona.Instructions) {
					t.Fatal("trusted duties missing from system prefix")
				}
				for _, untrusted := range []string{input.CurrentWindow, input.Memory, input.TaskBrief} {
					if strings.Contains(message.Content, untrusted) {
						t.Fatal("conversation data was promoted to configured instructions")
					}
				}
			}
		}
		if systemCount != 1 {
			t.Fatalf("system prefix count=%d, want 1", systemCount)
		}
		return completion(t, "收到", "stop"), nil
	})
	if _, err := New(config, model, nil).Run(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}
