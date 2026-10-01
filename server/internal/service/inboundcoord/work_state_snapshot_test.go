package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	openai "github.com/openai/openai-go/v3"

	"github.com/multica-ai/multica/server/pkg/llm"
)

const snapshotIssueID = "43132f41-936b-427c-a0e5-2e22e91b6122"
const snapshotArguments = `{"issue_id":"` + snapshotIssueID + `"}`
const snapshotRecall = `{"conversation_id":"cid-current","status":"loaded","items":[{"issue_id":"` + snapshotIssueID + `","purpose":"返回这次请求的结果","on_this_scene":true}]}`
const snapshotStatusFinish = `{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["r2"],"reply":"最近一次执行已完成，送达情况仍未知。"}]}`
const snapshotClarifyFinish = `{"actions":[{"kind":"clarify","source_refs":["u1"],"missing_fields":["work_target"],"reply":"请说明需要查看哪项工作。"}]}`

func workStateSnapshotFixture(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"issue_id": snapshotIssueID, "status": "in_review", "status_source": coordinationIssueStatusSource,
		"scope": coordinationIssueScope, "title": "返回当前请求的结果", "original_goal": strings.Repeat("原始交付范围", 50),
		"latest_execution": map[string]any{"read_status": "loaded", "status_source": coordinationExecutionSource, "scope": coordinationExecutionScope,
			"task_id": "2cceddc6-1d27-42be-b598-8228c5837e3c", "status": "completed", "completed_at": "2026-09-10T05:58:08Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func workStateSnapshotTurn() Turn {
	return Turn{Source: SourceDigitalEmployee, SceneID: testSceneID("cid-current"), ConversationID: "cid-current", SenderName: "当前用户", Message: "这个任务执行得怎么样了？", HistoryStatus: "not_loaded"}
}

func TestWorkStateSnapshotReuseKeepsPartialEvidenceAndReadRef(t *testing.T) {
	reads := 0
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("w1", toolWorkState, snapshotArguments),
		assistantTool("w2", toolWorkState, snapshotArguments),
		assistantTool("w3", toolWorkState, snapshotArguments),
		assistantTool("finish", toolFinish, snapshotStatusFinish),
	}}
	tools := scenePrefetchToolFunc(func(_ context.Context, _ Turn, name, _ string) (string, error) {
		if name == toolAssocRecall {
			return snapshotRecall, nil
		}
		reads++
		return workStateSnapshotFixture(t), nil
	})
	d, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), workStateSnapshotTurn())
	if err != nil || d.Action != ActionReply || reads != 1 || chat.calls != 4 || chat.checkCalls != 1 || d.ToolRounds != 4 {
		t.Fatalf("reuse changed model accounting or failed: action=%s err=%v reads=%d main=%d review=%d", d.Action, err, reads, chat.calls, chat.checkCalls)
	}
	var first string
	reused := 0
	for _, step := range d.Steps {
		if step.Tool != toolWorkState || step.Type != "tool_result" {
			continue
		}
		if first == "" {
			first = step.Output
		}
		if step.Output != first || step.Error {
			t.Fatalf("reuse rewrote the original snapshot: %s", step.Output)
		}
		if step.Content == workStateSnapshotReuseReason {
			reused++
		}
	}
	if reused != 2 || !strings.Contains(first, `"read_ref":"r2"`) || !strings.Contains(first, `"complete":false`) || !strings.Contains(first, `"truncated":true`) || !strings.Contains(first, `"delivery_status":"not_loaded"`) {
		t.Fatalf("read identity/uncertainty lost: reused=%d output=%s", reused, first)
	}
	for _, request := range chat.params[1:] {
		raw, _ := json.Marshal(request.Messages)
		if !strings.Contains(string(raw), "snapshot_available") || !strings.Contains(string(raw), "not pagination") || strings.Contains(string(raw), `\"read_ref\":\"r3\"`) {
			t.Fatalf("model did not receive stable evidence and bounded-read guidance: %s", raw)
		}
	}
}

func TestWorkStateSnapshotFailuresRemainRetryable(t *testing.T) {
	for _, kind := range []string{"transport", "unavailable_payload", "unavailable_execution", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			reads := 0
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
				assistantTool("w1", toolWorkState, snapshotArguments), assistantTool("w2", toolWorkState, snapshotArguments),
				assistantTool("w3", toolWorkState, snapshotArguments), assistantTool("finish", toolFinish, strings.ReplaceAll(snapshotStatusFinish, "r2", "r3")),
			}}
			tools := scenePrefetchToolFunc(func(_ context.Context, _ Turn, name, _ string) (string, error) {
				if name == toolAssocRecall {
					return snapshotRecall, nil
				}
				reads++
				if reads > 1 {
					return workStateSnapshotFixture(t), nil
				}
				var raw map[string]any
				_ = json.Unmarshal([]byte(workStateSnapshotFixture(t)), &raw)
				switch kind {
				case "transport":
					return "", errors.New("read unavailable")
				case "malformed":
					return "not json", nil
				case "unavailable_payload":
					raw["status"] = "unavailable"
				case "unavailable_execution":
					raw["latest_execution"] = map[string]string{"read_status": "unavailable"}
				}
				body, _ := json.Marshal(raw)
				return string(body), nil
			})
			d, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), workStateSnapshotTurn())
			if err != nil || d.Action != ActionReply || reads != 2 {
				t.Fatalf("failed read was cached or success not reused: %v reads=%d", err, reads)
			}
			if body, _ := json.Marshal(chat.params[1].Messages); strings.Contains(string(body), "snapshot_available") {
				t.Fatal("unavailable evidence was described as reusable success")
			}
		})
	}
}

func TestWorkStateSnapshotScopeGuardPrecedesReuse(t *testing.T) {
	turn := workStateSnapshotTurn()
	seq := 0
	if _, err := rememberCoordinationRead(&turn, &seq, toolWorkState, snapshotArguments, workStateSnapshotFixture(t), nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := reusableWorkStateSnapshot(turn, snapshotArguments, 0); !ok {
		t.Fatal("fixture lacks a valid snapshot to guard")
	}
	reads := 0
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("w", toolWorkState, snapshotArguments), assistantTool("finish", toolFinish, snapshotClarifyFinish)}}
	tools := scenePrefetchToolFunc(func(_ context.Context, _ Turn, name, _ string) (string, error) {
		if name == toolAssocRecall {
			// A different Issue is recalled, so work_state is disclosed with
			// that id only; the retained snapshot's id was never recalled.
			return `{"conversation_id":"cid-current","status":"loaded","items":[{"issue_id":"dddddddd-dddd-dddd-dddd-dddddddddddd","purpose":"另一件事","on_this_scene":true}]}`, nil
		}
		reads++
		return workStateSnapshotFixture(t), nil
	})
	d, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), turn)
	if err != nil || reads != 0 {
		t.Fatalf("unrecalled issue reached its reader: %v reads=%d", err, reads)
	}
	rejected := false
	for _, step := range d.Steps {
		if step.Tool == toolWorkState && step.Type == "tool_result" {
			rejected = step.Error && strings.Contains(step.Output, "must be copied exactly from assoc_recall") && step.Content != workStateSnapshotReuseReason
		}
	}
	if !rejected {
		t.Fatal("snapshot bypassed current recalled-ID authorization")
	}
}

func TestWorkStateSnapshotDoesNotCrossRunsOrArguments(t *testing.T) {
	turn := workStateSnapshotTurn()
	seq := 0
	if _, err := rememberCoordinationRead(&turn, &seq, toolWorkState, snapshotArguments, workStateSnapshotFixture(t), nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, arguments string
		initial         int
	}{
		{"new-run", snapshotArguments, seq},
		{"other-issue", `{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`, 0},
		{"extra-argument", strings.TrimSuffix(snapshotArguments, "}") + `,"limit":5}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := reusableWorkStateSnapshot(turn, tc.arguments, tc.initial); ok {
				t.Fatal("snapshot crossed the current-run/argument boundary")
			}
		})
	}
	for _, change := range []string{"scope", "issue_id", "read_ref"} {
		other := turn
		other.CoordinationReads = append([]CoordinationRead(nil), turn.CoordinationReads...)
		var payload map[string]any
		_ = json.Unmarshal(other.CoordinationReads[0].Result, &payload)
		payload[change] = "different"
		other.CoordinationReads[0].Result, _ = json.Marshal(payload)
		if _, ok := reusableWorkStateSnapshot(other, snapshotArguments, 0); ok {
			t.Fatalf("snapshot reused a mismatched %s", change)
		}
	}
	// Unsupported original parameters also cannot become a canonical cache hit.
	extra := strings.TrimSuffix(snapshotArguments, "}") + `,"limit":5}`
	if _, err := rememberCoordinationRead(&turn, &seq, toolWorkState, extra, workStateSnapshotFixture(t), nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := reusableWorkStateSnapshot(turn, snapshotArguments, 0); ok {
		t.Fatal("noncanonical original request was treated as identical")
	}
}

func TestWorkStateSnapshotPreservesPendingFeedback(t *testing.T) {
	for _, pending := range []string{"review", "history"} {
		t.Run(pending, func(t *testing.T) {
			reads := 0
			turn := workStateSnapshotTurn()
			chat := &scriptedCompleter{}
			if pending == "review" {
				repaired := strings.Replace(snapshotStatusFinish, "最近一次执行已完成，送达情况仍未知。", "已读到执行完成状态；送达仍未确认。", 1)
				chat.rounds = []openai.ChatCompletion{assistantTool("w1", toolWorkState, snapshotArguments), assistantTool("f1", toolFinish, snapshotStatusFinish), assistantTool("w2", toolWorkState, snapshotArguments), assistantTool("f2", toolFinish, repaired)}
				chat.checkRounds = []openai.ChatCompletion{scriptedFinishVerdict("revise", "UNRESOLVED_REVIEW_BOUNDARY"), scriptedFinishVerdict("allow", "Original evidence remains the scope.")}
			} else {
				answer := fmt.Sprintf(`{"actions":[{"kind":"continue_work","source_refs":["u1"],"reply":"我来处理","purpose":"继续已确认的原任务范围","issue_id":%q,"basis":"answer"}]}`, snapshotIssueID)
				chat.rounds = []openai.ChatCompletion{assistantTool("f0", toolFinish, answer), assistantTool("w1", toolWorkState, snapshotArguments), assistantTool("w2", toolWorkState, snapshotArguments), assistantTool("f1", toolFinish, snapshotClarifyFinish)}
			}
			tools := scenePrefetchToolFunc(func(_ context.Context, _ Turn, name, _ string) (string, error) {
				if name == toolAssocRecall {
					return snapshotRecall, nil
				}
				reads++
				return workStateSnapshotFixture(t), nil
			})
			if _, err := (&Coordinator{Chat: chat, Tools: tools}).runLoop(context.Background(), turn); err != nil || reads != 1 {
				t.Fatalf("pending-feedback scenario failed: %v reads=%d", err, reads)
			}
			raw, _ := json.Marshal(chat.params[len(chat.params)-1].Messages)
			want := "UNRESOLVED_REVIEW_BOUNDARY"
			if pending == "history" {
				want = "requires original question evidence"
			}
			if !strings.Contains(string(raw), want) || strings.Contains(string(raw), "snapshot_available") {
				t.Fatalf("snapshot advice displaced pending %s feedback: %s", pending, raw)
			}
		})
	}
}

func TestWorkStateSnapshotFeedbackKeepsBudgetsAndUnknownFields(t *testing.T) {
	turn := workStateSnapshotTurn()
	seq := 0
	var payload map[string]any
	_ = json.Unmarshal([]byte(workStateSnapshotFixture(t)), &payload)
	payload["latest_execution"] = map[string]string{"read_status": "not_loaded"}
	raw, _ := json.Marshal(payload)
	_, err := rememberCoordinationRead(&turn, &seq, toolWorkState, snapshotArguments, string(raw), nil)
	if err != nil {
		t.Fatal(err)
	}
	before := coordinationReadsJSON(turn)
	feedback := workStateSnapshotFeedback(turn, 0)
	if feedback == "" || utf8.RuneCountInString(feedback) > coordinationFeedbackBudget || utf8.RuneCountInString(before) > coordinationReadsBudget || coordinationReadsJSON(turn) != before {
		t.Fatal("feedback expanded or mutated the bounded evidence")
	}
	if !strings.Contains(before, `"read_status":"not_loaded"`) || !strings.Contains(before, `"delivery_status":"not_loaded"`) || !strings.Contains(before, `"complete":false`) || strings.Contains(before, "arguments") {
		t.Fatalf("unknown state or private argument provenance changed: %s", before)
	}
	turn.CoordinationReads = nil
	if workStateSnapshotFeedback(turn, 0) != "" {
		t.Fatal("feedback retained an evicted read reference")
	}
}

func TestWorkStateSnapshotTraceCoversActualReadAndMarksReuse(t *testing.T) {
	client, exporter := langfuseTestClient(t)
	var entered, returned time.Time
	tools := scenePrefetchToolFunc(func(_ context.Context, _ Turn, name, _ string) (string, error) {
		if name == toolAssocRecall {
			return snapshotRecall, nil
		}
		entered = time.Now()
		body := workStateSnapshotFixture(t)
		returned = time.Now()
		return body, nil
	})
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("w1", toolWorkState, snapshotArguments), assistantTool("w2", toolWorkState, snapshotArguments), assistantTool("f", toolFinish, snapshotStatusFinish)}}
	d := (&Coordinator{LLM: llm.New(llm.Config{APIKey: "test-key"}), Chat: chat, Tools: tools, Langfuse: client}).Decide(context.Background(), workStateSnapshotTurn())
	if d.Action != ActionReply {
		t.Fatalf("trace fixture failed: %s", d.Action)
	}
	spans := spansNamed(exporter.GetSpans(), toolWorkState)
	if len(spans) != 2 {
		t.Fatalf("trace omitted actual call or reuse: %d", len(spans))
	}
	for _, span := range spans {
		call, _ := spanAttr(span, "langfuse.observation.metadata.tool_call_id")
		if call.AsString() == "w1" && (span.StartTime.After(entered) || span.EndTime.Before(returned)) {
			t.Fatal("read span was created after actual I/O")
		}
		if call.AsString() == "w2" {
			reason, _ := spanAttr(span, "langfuse.observation.metadata.reason")
			output, _ := spanAttr(span, "langfuse.observation.output")
			if reason.AsString() != workStateSnapshotReuseReason || !strings.Contains(output.AsString(), `"read_ref":"r2"`) {
				t.Fatal("reuse was logged as a new read or lost its reference")
			}
		}
	}
}
