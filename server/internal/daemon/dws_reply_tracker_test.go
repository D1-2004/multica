package daemon

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

type dwsReplyBackend struct{}

func (dwsReplyBackend) Execute(
	context.Context,
	string,
	agent.ExecOptions,
) (*agent.Session, error) {
	messages := make(chan agent.Message, 2)
	messages <- agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Bash",
		CallID: "reply-call",
		Input: map[string]any{
			"command": `dws chat message reply --text "最终回复正文" --format json`,
		},
	}
	messages <- agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "reply-call",
		Output: `{"success":true}`,
	}
	close(messages)

	results := make(chan agent.Result, 1)
	results <- agent.Result{Status: "completed", Output: "agent execution summary"}
	close(results)
	return &agent.Session{Messages: messages, Result: results}, nil
}

type dwsReplyThenIdleBackend struct{}

func (dwsReplyThenIdleBackend) Execute(
	context.Context,
	string,
	agent.ExecOptions,
) (*agent.Session, error) {
	messages := make(chan agent.Message, 2)
	messages <- agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Bash",
		CallID: "reply-before-idle",
		Input: map[string]any{
			"command": `dws chat message reply --text "已向用户说明任务阻塞" --format json`,
		},
	}
	messages <- agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "reply-before-idle",
		Output: `{"success":true}`,
	}
	return &agent.Session{
		Messages: messages,
		Result:   make(chan agent.Result),
	}, nil
}

func TestExtractDWSReplyText(t *testing.T) {
	tests := []struct {
		name    string
		message agent.Message
		want    string
		ok      bool
	}{
		{
			name: "bash command",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-1",
				Input: map[string]any{
					"command": `dws chat message reply --conversation-id cid --text "在的，有什么需要帮忙？" --format json`,
				},
			},
			want: "在的，有什么需要帮忙？",
			ok:   true,
		},
		{
			name: "exec command cmd alias",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "exec_command",
				CallID: "call-2",
				Input: map[string]any{
					"cmd": `dws chat message reply --text='第二条回复' --format=json`,
				},
			},
			want: "第二条回复",
			ok:   true,
		},
		{
			name: "lowercase shell",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "shell",
				CallID: "call-3",
				Input: map[string]any{
					"command": `dws chat message reply --format json --text "完成"`,
				},
			},
			want: "完成",
			ok:   true,
		},
		{
			name: "missing call id",
			message: agent.Message{
				Type: agent.MessageToolUse,
				Tool: "Bash",
				Input: map[string]any{
					"command": `dws chat message reply --text "完成" --format json`,
				},
			},
		},
		{
			name: "non shell tool",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "dws",
				CallID: "call-4",
				Input: map[string]any{
					"command": `dws chat message reply --text "完成" --format json`,
				},
			},
		},
		{
			name: "variable expansion",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-5",
				Input: map[string]any{
					"command": `dws chat message reply --text "$REPLY" --format json`,
				},
			},
		},
		{
			name: "command substitution",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-6",
				Input: map[string]any{
					"command": "dws chat message reply --text \"$(get-reply)\" --format json",
				},
			},
		},
		{
			name: "compound command and",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-7",
				Input: map[string]any{
					"command": `dws chat message reply --text "完成" --format json && echo done`,
				},
			},
		},
		{
			name: "compound command semicolon",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-7-semicolon",
				Input: map[string]any{
					"command": `dws chat message reply --text "完成" --format json; echo done`,
				},
			},
		},
		{
			name: "compound command pipe",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-7-pipe",
				Input: map[string]any{
					"command": `dws chat message reply --text "完成" --format json | tee result`,
				},
			},
		},
		{
			name: "compound command newline",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-7-newline",
				Input: map[string]any{
					"command": "dws chat message reply --text \"完成\" --format json\necho done",
				},
			},
		},
		{
			name: "wrong format",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-8",
				Input: map[string]any{
					"command": `dws chat message reply --text "完成" --format text`,
				},
			},
		},
		{
			name: "wrong command",
			message: agent.Message{
				Type:   agent.MessageToolUse,
				Tool:   "Bash",
				CallID: "call-9",
				Input: map[string]any{
					"command": `dws chat message send --text "完成" --format json`,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractDWSReplyText(tt.message)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("extractDWSReplyText() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDWSReplyTrackerRecordsOnlySuccessfulMatchingResult(t *testing.T) {
	tracker := newDWSReplyTracker()

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Bash",
		CallID: "failed",
		Input: map[string]any{
			"command": `dws chat message reply --text "失败回复" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "failed",
		Output: `{"success":false}`,
	})
	if got := tracker.ResultMessage(); got != "" {
		t.Fatalf("failed result recorded %q", got)
	}

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "bash",
		CallID: "first",
		Input: map[string]any{
			"command": `dws chat message reply --text "第一次成功" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "unmatched",
		Output: `{"success":true}`,
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "first",
		Output: `{"success":true,"result":[]}`,
	})
	if got := tracker.ResultMessage(); got != "第一次成功" {
		t.Fatalf("first successful result = %q", got)
	}

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Bash",
		CallID: "claude-wrapped",
		Input: map[string]any{
			"command": `dws chat message reply --text "Claude 成功回复" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "claude-wrapped",
		Output: `"{\"success\":true,\"result\":[]}"`,
	})
	if got := tracker.ResultMessage(); got != "Claude 成功回复" {
		t.Fatalf("Claude wrapped successful result = %q", got)
	}

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Bash",
		CallID: "claude-content-array",
		Input: map[string]any{
			"command": `dws chat message reply --text "Claude content 数组回复" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "claude-content-array",
		Output: `[{"type":"text","text":"{\"success\":true,\"result\":[]}"}]`,
	})
	if got := tracker.ResultMessage(); got != "Claude content 数组回复" {
		t.Fatalf("Claude content array successful result = %q", got)
	}

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Bash",
		CallID: "claude-content-object-failed",
		Input: map[string]any{
			"command": `dws chat message reply --text "不能记录的 Claude 回复" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "claude-content-object-failed",
		Output: `{"type":"text","text":"{\"success\":false}"}`,
	})
	if got := tracker.ResultMessage(); got != "Claude content 数组回复" {
		t.Fatalf("Claude success=false changed message to %q", got)
	}

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "Shell",
		CallID: "malformed",
		Input: map[string]any{
			"cmd": `dws chat message reply --text "不能记录" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "malformed",
		Output: `success=true`,
	})
	if got := tracker.ResultMessage(); got != "Claude content 数组回复" {
		t.Fatalf("malformed result changed message to %q", got)
	}

	tracker.Observe(agent.Message{
		Type:   agent.MessageToolUse,
		Tool:   "exec_command",
		CallID: "last",
		Input: map[string]any{
			"command": `dws chat message reply --text "最后一次成功" --format json`,
		},
	})
	tracker.Observe(agent.Message{
		Type:   agent.MessageToolResult,
		CallID: "last",
		Output: `{"success":true}`,
	})
	if got := tracker.ResultMessage(); got != "最后一次成功" {
		t.Fatalf("last successful result = %q", got)
	}
}

func TestDWSToolResultSucceeded(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "top level DWS object",
			output: `{"success":true,"result":[]}`,
			want:   true,
		},
		{
			name:   "top level explicit failure",
			output: `{"success":false}`,
		},
		{
			name:   "Claude string content",
			output: `"{\"success\":true}"`,
			want:   true,
		},
		{
			name:   "Claude text object",
			output: `{"type":"text","text":"{\"success\":true}"}`,
			want:   true,
		},
		{
			name:   "Claude content array",
			output: `[{"type":"text","text":"{\"success\":true}"}]`,
			want:   true,
		},
		{
			name:   "ordinary text",
			output: `"command completed"`,
		},
		{
			name:   "ordinary text wrapper",
			output: `{"type":"text","text":"command completed"}`,
		},
		{
			name:   "malformed JSON",
			output: `{"success":`,
		},
		{
			name:   "non boolean success",
			output: `{"success":"true"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dwsToolResultSucceeded(tt.output); got != tt.want {
				t.Fatalf("dwsToolResultSucceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExecuteAndDrainReturnsSuccessfulDWSReplyText(t *testing.T) {
	d := newTestDaemon(t)
	result, _, err := d.executeAndDrain(
		context.Background(),
		dwsReplyBackend{},
		"prompt",
		agent.ExecOptions{},
		slog.Default(),
		"task-dws-reply",
		new(atomic.Int32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "agent execution summary" {
		t.Fatalf("output = %q", result.Output)
	}
	if result.ResultMessage != "最终回复正文" {
		t.Fatalf("result message = %q", result.ResultMessage)
	}
}

func TestExecuteAndDrainIdleWatchdogPreservesSuccessfulDWSReplyText(t *testing.T) {
	d := newTestDaemon(t)
	d.cfg.AgentIdleWatchdog = 50 * time.Millisecond

	result, _, err := d.executeAndDrain(
		context.Background(),
		dwsReplyThenIdleBackend{},
		"prompt",
		agent.ExecOptions{},
		slog.Default(),
		"task-dws-reply-before-idle",
		new(atomic.Int32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "idle_watchdog" {
		t.Fatalf("status = %q", result.Status)
	}
	if result.ResultMessage != "已向用户说明任务阻塞" {
		t.Fatalf("result message = %q", result.ResultMessage)
	}
}
