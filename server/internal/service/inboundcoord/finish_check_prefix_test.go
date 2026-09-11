package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
)

func reviewMessageContents(t *testing.T, params openai.ChatCompletionNewParams) []string {
	t.Helper()
	out := make([]string, 0, len(params.Messages))
	for _, m := range params.Messages {
		raw, _ := json.Marshal(m)
		var message struct{ Content string }
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatal(err)
		}
		out = append(out, message.Content)
	}
	return out
}

// Two turns of the same Agent in different conversations, with different
// history, reads and scene memory, must send byte-identical system and
// configuration segments so the provider's prompt cache serves that prefix.
// Production evidence: trace 39427c33 finish_check.2 cached only the shared
// core module (1152 tokens) although the same 6k-character job policy had
// been sent two minutes earlier; conversation_id and history_before sorted
// ahead of job_policy in one JSON body and broke the prefix.
func TestFinishCheckConfigurationSegmentIsStableAcrossTurns(t *testing.T) {
	base := Turn{Source: SourceDigitalEmployee, ChatType: "p2p", Addressed: true, AgentName: "菲迪", EmployeeAccountName: "菲迪", DWSUID: "6753994909",
		Instructions: "岗位说明：评比类问题一律不答。", Persona: "FDE教练", ReplyTone: "口语",
		Skills: []SkillSnapshot{{Name: "fde-coach", Description: "coach"}}, SkillsStatus: "loaded"}
	turns := []Turn{base, base}
	turns[0].ConversationID, turns[0].Message, turns[0].HistoryBefore = "cid-a", "在吗", time.Date(2026, 9, 11, 5, 47, 0, 0, time.UTC)
	turns[0].SceneMemory, turns[0].SceneMemoryRevision, turns[0].SceneMemoryStatus, turns[0].HistoryStatus = "## 场域定位\n冬翔", 24, "loaded", "loaded"
	turns[1].ConversationID, turns[1].Message, turns[1].HistoryBefore = "cid-b", "Hi", time.Date(2026, 9, 11, 6, 2, 0, 0, time.UTC)
	turns[1].SceneMemory, turns[1].SceneMemoryRevision, turns[1].SceneMemoryStatus, turns[1].HistoryStatus = "## 场域定位\n须莫", 3, "loaded", "not_loaded"
	turns[1].CoordinationReads = []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall, Result: json.RawMessage(`{"status":"empty"}`)}}
	// Receiving identity and the contract read state come from the event and
	// this turn's reads; they must not sit in the cached configuration.
	turns[1].DWSUID, turns[1].EmployeeAccountName = "6753994910", "菲迪(备)"
	turns[1].CoordinatorContractState = "unavailable"

	var contents [][]string
	for _, turn := range turns {
		chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "Greeting.")}}
		decision := Decision{Action: ActionReply, UserText: "在的。", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", SourceRefs: []string{"u1"}, AckKind: "greeting", Reply: "在的。"}}}
		if _, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, decision, nil, 0, nil); err != nil {
			t.Fatal(err)
		}
		if len(chat.checkParams) != 1 {
			t.Fatalf("review calls = %d", len(chat.checkParams))
		}
		contents = append(contents, reviewMessageContents(t, chat.checkParams[0]))
	}
	if len(contents[0]) != 4 || len(contents[1]) != 4 {
		t.Fatalf("review must be system + configuration + turn context + proposal: %d/%d", len(contents[0]), len(contents[1]))
	}
	if contents[0][0] != contents[1][0] {
		t.Fatal("system prompt differs between two reply reviews of the same Agent")
	}
	if contents[0][1] != contents[1][1] {
		t.Fatalf("configuration segment differs between turns:\n%s\n---\n%s", contents[0][1], contents[1][1])
	}
	if contents[0][2] == contents[1][2] {
		t.Fatal("turn context segment must carry the per-turn fields")
	}
	configuration := contents[0][1]
	for _, key := range []string{`"job_policy"`, `"skills"`, `"persona"`, `"reply_tone"`, `"configured_context_scope"`} {
		if !strings.Contains(configuration, key) {
			t.Fatalf("configuration segment lacks %s", key)
		}
	}
	for _, key := range []string{`"conversation_id"`, `"history_before"`, `"history_status"`, `"scene_memory"`, `"read_evidence"`, `"addressed"`, `"employee_uid"`, `"employee_account_name"`, `"agent_name"`, `"coordinator_contract"`, `"receiving_identity_status"`} {
		if strings.Contains(configuration, key) {
			t.Fatalf("per-turn field %s leaked into the cached configuration segment", key)
		}
		if !strings.Contains(contents[0][2], key) {
			t.Fatalf("turn context segment lacks %s", key)
		}
	}
	if !strings.Contains(contents[0][3], `"candidate"`) || !strings.Contains(contents[0][3], `"current_window"`) {
		t.Fatal("proposal segment must stay last")
	}
}
