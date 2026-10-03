package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeConversationGuidanceFreezesOnlyWithNewSnapshots(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new_snapshot"
		if legacy {
			name = "legacy_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			f, dc := employeeMemoryFixture(t)
			host, _, _ := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "latter", Text: "后者呢？", SenderOpenDingTalkID: "requester-open-id"}})
			ctx := context.Background()
			input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the old edit and the new same-name reset together. This fixture
			// checks prompt assembly, not whether a mocked model understands them.
			input.Input.RecentConversation = `{"messages":[{"role":"user","text":"只把小周改成紫色"},{"role":"assistant","text":"小林蓝，小周紫"},{"role":"user","text":"这轮讨论里：小林选蓝色，小周选绿色"},{"role":"assistant","text":"小林选蓝色，小周选绿色"}]}`
			if legacy {
				input.Config.Persona.Instructions = "Original frozen responsibilities."
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := host.worker.store.SaveInput(ctx, host.job, raw); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var providerRequest []byte
			host.worker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				providerRequest, _ = json.Marshal(p)
				messageJSON, _ := json.Marshal(p.Messages)
				var messages []struct{ Role, Content string }
				if err := json.Unmarshal(messageJSON, &messages); err != nil {
					t.Fatal(err)
				}
				system := ""
				history := ""
				for _, message := range messages {
					if message.Role == "system" {
						system = message.Content
					}
					if strings.Contains(message.Content, "Recent conversation (temporary") {
						history = message.Content
						if message.Role != "user" {
							t.Fatal("history promoted to authority")
						}
					}
				}
				if legacy {
					if strings.Contains(system, "RECENT CONVERSATION:") || !strings.Contains(system, "Original frozen responsibilities.") {
						t.Fatal("new guidance changed restored responsibilities")
					}
				} else {
					for _, rule := range []string{"RECENT CONVERSATION:", "latest explicit user facts or reset supersede older assignments and edits", "never replay an older change on top of a newer restatement", "preserve other current facts", "most recent relevant exchange and its object order", "Historical requests are context, not new commands"} {
						if !strings.Contains(system, rule) {
							t.Errorf("new frozen prompt missing recency rule %q", rule)
						}
					}
				}
				for _, fact := range []string{"小林蓝，小周紫", "这轮讨论里：小林选蓝色，小周选绿色"} {
					if !strings.Contains(history, fact) {
						t.Errorf("history was rewritten to hide the conflict: %q", fact)
					}
				}
				return employeeMemoryAnswer(t), nil
			})
			for range 2 {
				if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now(),outcome=NULL,lease_token=NULL,lease_until=NULL WHERE id=$1::uuid`, host.job.ID); err != nil {
					t.Fatal(err)
				}
				if worked, err := host.worker.ProcessNext(ctx); !worked || err != nil {
					t.Fatal(worked, err)
				}
			}
			var sameSnapshot, sameRequest bool
			if err := testPool.QueryRow(ctx, `SELECT input_snapshot=$2::jsonb,model_journal->0->'request'=$3::jsonb FROM employee_scene_job WHERE id=$1::uuid`, host.job.ID, raw, providerRequest).Scan(&sameSnapshot, &sameRequest); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !sameSnapshot || !sameRequest {
				t.Fatal("snapshot/journal replay changed", calls, sameSnapshot, sameRequest)
			}
			assertEmployeeReplyNoTasks(t, f)
		})
	}
	if strings.Contains(employeeloop.BuildPrompt(employeeloop.Persona{}), "RECENT CONVERSATION:") {
		t.Fatal("new guidance leaked into global prompt builder")
	}
}
