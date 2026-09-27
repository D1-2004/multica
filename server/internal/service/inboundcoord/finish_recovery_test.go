package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	openai "github.com/openai/openai-go/v3"
)

func TestFinishRecoveryBudgetAndDeepSeekWireOnOff(t *testing.T) {
	for _, enabled := range []bool{true, false, true} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("f", toolFinish, `{"actions":[]}`)}}
		c := &Coordinator{Chat: chat, model: "bailian/deepseek-v4.1-flash", finishRecovery: enabled}
		if _, err := c.complete(context.Background(), []openai.ChatCompletionMessageParamUnion{openai.UserMessage("test")}, []openai.ChatCompletionToolUnionParam{windowPlanTool(true)}); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(chat.params[0])
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		want := float64(1536)
		if enabled {
			want = 4096
		}
		if wire["max_completion_tokens"] != want {
			t.Fatalf("budget=%v want=%v", wire["max_completion_tokens"], want)
		}
		if enabled {
			if _, exists := wire["reasoning_effort"]; exists {
				t.Fatal("DeepSeek disabled thinking must omit reasoning_effort")
			}
			thinking, ok := wire["thinking"].(map[string]any)
			if !ok || thinking["type"] != "disabled" {
				t.Fatal("DeepSeek thinking not disabled")
			}
		} else if _, exists := wire["thinking"]; exists {
			t.Fatal("off retained new wire override")
		}
	}
}

func TestFinishRecoveryRolloutKeepsOtherAgentOnLegacyBudget(t *testing.T) {
	selected := pgtype.UUID{Bytes: [16]byte{11}, Valid: true}
	other := pgtype.UUID{Bytes: [16]byte{12}, Valid: true}
	for _, tc := range []struct {
		name  string
		agent pgtype.UUID
		want  int64
	}{
		{"selected", selected, 4096},
		{"other", other, 1536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("f", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好"}]}`)}}
			c := &Coordinator{Chat: chat, FinishRecoveryAgentProvider: func(id pgtype.UUID) bool { return id == selected }}
			got := c.Decide(context.Background(), Turn{Source: SourceWeb, Message: "你好", Addressed: true, AgentID: tc.agent})
			if got.Action != ActionReply || len(chat.params) == 0 || chat.params[0].MaxCompletionTokens.Value != tc.want {
				t.Fatalf("decision=%#v budget=%v want=%d", got, chat.params, tc.want)
			}
		})
	}
}

func TestFinishSerializationRepairOnlyCallsFinish(t *testing.T) {
	valid := `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好"}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("fixed", toolFinish, valid)}}
	c := &Coordinator{Chat: chat, finishRecovery: true}
	broken := assistantTool("broken", toolFinish, `{"actions":[`)
	repairs, calls := 0, 1
	got, err := c.repairFinishSerialization(context.Background(), Turn{Source: SourceWeb, Message: "你好"}, []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("original"), openai.UserMessage("unchanged evidence")}, toolsForDisclosure(Turn{Source: SourceWeb}, 0, false), false, &broken, &repairs, &calls)
	if err != nil {
		t.Fatal(err)
	}
	if needsFinishSerializationRepair(got) || repairs != 1 || calls != 2 {
		t.Fatal("repair accounting failed")
	}
	if names := toolParamNames(chat.params[0].Tools); len(names) != 1 || names[0] != toolFinish {
		t.Fatalf("repair offered reads or writes: %v", names)
	}
	raw, _ := json.Marshal(chat.params[0].Messages)
	if !strings.Contains(string(raw), "unchanged evidence") || !strings.Contains(string(raw), "policy:finish_repair") {
		t.Fatal("repair lost evidence/provenance")
	}
	if _, err := parseValidatedWindowPlan(`{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"perform scoped synthetic task","state_refs":["invented"]}]}`, Turn{Source: SourceWeb, Message: "do work"}, nil, nil); err == nil {
		t.Fatal("Host validation was weakened")
	}
}

func TestFinishWireSchemaUsesHostFieldAllowList(t *testing.T) {
	p, err := coordinatorWireParams(openai.ChatCompletionNewParams{Tools: []openai.ChatCompletionToolUnionParam{windowPlanTool(true)}})
	if err != nil {
		t.Fatal(err)
	}
	pruneFinishSchemas(p.Tools)
	raw, _ := json.Marshal(p.Tools[0].OfFunction.Function.Parameters)
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	branches := schema["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)["oneOf"].([]any)
	for _, entry := range branches {
		props := entry.(map[string]any)["properties"].(map[string]any)
		kind := props["kind"].(map[string]any)["enum"].([]any)[0].(string)
		allowed, _ := coordinationAllowedFields(kind, LoopInbound)
		for name := range props {
			if !allowed[name] {
				t.Fatalf("%s advertises forbidden %s", kind, name)
			}
		}
		if kind == "report_status" {
			if _, ok := props["state_refs"]; !ok {
				t.Fatal("status lost its evidence field")
			}
		}
	}
}

func TestFinishSerializationRepairIsBounded(t *testing.T) {
	broken := assistantTool("f", toolFinish, `{"actions":[`)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{broken, broken}}
	c := &Coordinator{Chat: chat, finishRecovery: true}
	repairs, calls := 0, 1
	_, err := c.repairFinishSerialization(context.Background(), Turn{Source: SourceWeb}, []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("test")}, []openai.ChatCompletionToolUnionParam{windowPlanTool(false)}, false, &broken, &repairs, &calls)
	if err == nil || repairs != 2 || len(chat.params) != 2 {
		t.Fatalf("unbounded or missing repair: %v %d", err, repairs)
	}
}

func TestFinishRecoveryThroughDecideKeepsHostReview(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("bad", toolFinish, `{"actions":[`),
		assistantTool("good", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好"}]}`),
	}}
	sampled := 0
	c := &Coordinator{Chat: chat, FinishRecoveryProvider: func() bool { sampled++; return true }}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Message: "你好", Addressed: true})
	if got.Action != ActionReply || got.ToolRounds != 2 {
		t.Fatalf("unexpected decision: %#v", got)
	}
	if sampled != 1 || len(chat.params) != 2 || len(chat.checkParams) == 0 {
		t.Fatalf("snapshot/main/review=%d/%d/%d", sampled, len(chat.params), len(chat.checkParams))
	}
	for _, p := range chat.params {
		if p.MaxCompletionTokens.Value != 4096 {
			t.Fatal("budget was not frozen")
		}
	}
}

func TestFinishRecoveryExhaustionDoesNotReplayWholeDecision(t *testing.T) {
	broken := assistantTool("f", toolFinish, `{"actions":[`)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{broken, broken, broken}}
	c := &Coordinator{Chat: chat, finishRecovery: true}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Message: "你好", Addressed: true})
	if got.Action != ActionReply || !got.LoopStopFallback() || got.Reason != loopStopFinishSerialization || len(got.CoordinationActions) != 0 {
		t.Fatalf("exhaustion must checkpoint a no-work fallback: %#v", got)
	}
	if len(chat.params) != 3 || got.ToolRounds != 3 {
		t.Fatal("repair budget/accounting changed")
	}
}
