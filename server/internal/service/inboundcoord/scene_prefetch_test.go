package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
)

type scenePrefetchToolFunc func(context.Context, Turn, string, string) (string, error)

func (f scenePrefetchToolFunc) Call(ctx context.Context, turn Turn, name, args string) (string, error) {
	return f(ctx, turn, name, args)
}

func TestScenePrefetchFirstModelCanPlanWork(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "用户", Message: "请查证主持人和参会人开启听记会产生几份记录。"}
	reads := 0
	tools := scenePrefetchToolFunc(func(ctx context.Context, got Turn, name, args string) (string, error) {
		reads++
		var query map[string]any
		if err := json.Unmarshal([]byte(args), &query); err != nil {
			t.Fatal(err)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > scenePrefetchTimeout {
			t.Fatal("prefetch is not bounded")
		}
		if name != toolAssocRecall || got.ConversationID != turn.ConversationID || query["conversation_id"] != turn.ConversationID || query["since"] != "48h" || query["limit"] != float64(3) || len(query) != 3 {
			t.Fatalf("unexpected prefetch scope: %s %s", name, args)
		}
		return `{"conversation_id":"cid-current","items":[],"comments":["HIDDEN_BUSINESS_REPORT"]}`, nil
	})
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("f1", toolFinish, `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"查证主持人与参会人开启听记后生成的记录数量","intent":"lookup","reply":"我来查证听记生成规则。"}]}`)}}
	got, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), turn)
	if err != nil || got.Action != ActionIssue || len(got.Items) != 1 || reads != 1 || chat.calls != 1 || chat.checkCalls != 1 {
		t.Fatalf("first model request cannot plan: %#v err=%v reads=%d calls=%d checks=%d", got, err, reads, chat.calls, chat.checkCalls)
	}
	raw, _ := json.Marshal(chat.params[0].Messages)
	if !strings.Contains(string(raw), "r1") || strings.Contains(string(raw), "HIDDEN_BUSINESS_REPORT") {
		t.Fatalf("prefetch projection was not supplied correctly: %s", raw)
	}
	if len(got.Steps) < 2 || got.Steps[0].Content != "Host prefetch (read-only)" || got.Steps[0].Tool != toolAssocRecall {
		t.Fatalf("Host read must not be presented as a model call: %#v", got.Steps)
	}
	// system, Agent configuration, turn context, proposal: never the routing
	// conversation.
	if len(chat.checkParams[0].Messages) != 4 {
		t.Fatal("side reviewer context must remain isolated")
	}
}

func TestScenePrefetchFailureRemainsUnavailableAndRetryable(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		err        error
	}{
		{"transport", "", errors.New("database unavailable")},
		{"malformed", "invalid json", nil},
		{"malformed_status", `{"status":42,"conversation_id":"cid-other","items":[]}`, nil},
		{"malformed_scene", `{"conversation_id":42,"items":[]}`, nil},
		{"null_status", `{"status":null,"items":[]}`, nil},
		{"null_scene", `{"conversation_id":null,"items":[]}`, nil},
		{"unavailable_payload", `{"status":"unavailable","items":[]}`, nil},
		{"stale_payload", `{"status":"stale","items":[]}`, nil},
		{"other_scene", `{"conversation_id":"cid-other","items":[]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "用户", Message: "请查证听记的生成规则。"}
			readCount := 0
			tools := scenePrefetchToolFunc(func(_ context.Context, _ Turn, _ string, _ string) (string, error) {
				readCount++
				if readCount == 1 {
					return tc.body, tc.err
				}
				return `{"conversation_id":"cid-current","items":[]}`, nil
			})
			work := `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"查证主持人与参会人开启听记的生成规则","intent":"lookup","reply":"我来查证。"}]}`
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("f0", toolFinish, work), assistantTool("r1", toolAssocRecall, `{"conversation_id":"cid-current"}`), assistantTool("f1", toolFinish, work)}}
			d, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), turn)
			if err != nil || d.Action != ActionIssue || readCount != 2 || chat.calls != 3 || chat.checkCalls != 1 {
				t.Fatalf("failed read bypassed gate or prevented retry: %#v err=%v reads=%d calls=%d checks=%d", d, err, readCount, chat.calls, chat.checkCalls)
			}
			raw, _ := json.Marshal(chat.params[0].Messages)
			if !strings.Contains(string(raw), "unavailable") {
				t.Fatal("failure disappeared from model context")
			}
			if len(d.Steps) < 4 || !d.Steps[1].Error || !d.Steps[3].Error {
				t.Fatalf("failed prefetch/work must be rejected: %#v", d.Steps)
			}
		})
	}
}

func TestScenePrefetchFailureDoesNotBlockConversation(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "你好"}
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("f1", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好。"}]}`)}}
	tools := scenePrefetchToolFunc(func(context.Context, Turn, string, string) (string, error) { return "", errors.New("read unavailable") })
	d, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), turn)
	if err != nil || d.Action != ActionReply || chat.calls != 1 || chat.checkCalls != 1 {
		t.Fatalf("non-work should still be reviewed and returned: %#v %v", d, err)
	}
	raw, _ := json.Marshal(chat.params[0].Messages)
	if strings.Contains(string(raw), "Latest Host repair feedback") {
		t.Fatal("prefetch failure must not force unnecessary repair for greetings")
	}
}

func TestScenePrefetchHonorsShorterParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	turn := Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current"}
	seq := 0
	tools := scenePrefetchToolFunc(func(ctx context.Context, _ Turn, _ string, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	_, _, err := (&Coordinator{Tools: tools}).prefetchSceneRecall(ctx, &turn, &seq)
	if !errors.Is(err, context.DeadlineExceeded) || len(turn.CoordinationReads) != 1 || !turn.CoordinationReads[0].Failed {
		t.Fatalf("deadline must become unavailable evidence: %v %#v", err, turn.CoordinationReads)
	}
}

func TestScenePrefetchSkipsNoSceneAndCompletion(t *testing.T) {
	for _, turn := range []Turn{
		{Source: SourceWeb, Message: "你好"},
		{Loop: LoopTaskFinished, Source: SourceDigitalEmployee, ConversationID: "cid-current", Message: "任务完成", TaskResult: "结果已完成", IssueID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
	} {
		raw := `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"你好。"}]}`
		if turn.Loop == LoopTaskFinished {
			raw = completionFinishJSON(turn, "结果已完成。")
		}
		reads := 0
		tools := scenePrefetchToolFunc(func(context.Context, Turn, string, string) (string, error) { reads++; return `{"items":[]}`, nil })
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("f1", toolFinish, raw)}}
		_, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), turn)
		if err != nil || reads != 0 || chat.calls != 1 || chat.checkCalls != 1 {
			t.Fatalf("unexpected prefetch for loop=%s: %v reads=%d", turn.Loop, err, reads)
		}
	}
}

func TestObservedGroupAssessesParticipationBeforeReadingOldWork(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, ConversationID: "group", Message: "同事在吗"}
	if shouldPrefetchSceneRecall(turn) {
		t.Fatal("observation must not preload unrelated old work")
	}
	turn.Addressed = true
	if !shouldPrefetchSceneRecall(turn) {
		t.Fatal("explicitly addressed turns retain prefetch")
	}
	turn.Addressed = false
	turn.ChatType = "p2p"
	if !shouldPrefetchSceneRecall(turn) {
		t.Fatal("DM prefetch changed")
	}
}
