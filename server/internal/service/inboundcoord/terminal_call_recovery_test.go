package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
)

const recoveryIssueID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
const recoveryRecall = `{"conversation_id":"cid-current","items":[{"issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","purpose":"确认审批导出范围并起草说明","on_this_scene":true}]}`
const recoveredWorkArgs = `{"source_refs":"[\"u1\"]","purpose":"查证审批数据导出范围及限制","intent":"lookup","reply":"我来查证导出范围。"}`
const recoveredAnswerArgs = `{"kind":"continue_work","source_refs":"[\"u1\"]","issue_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","basis":"answer","purpose":"按当前确认的范围起草审批导出说明","intent":"lookup","reply":"我来按确认范围起草说明。"}`

func TestTerminalCallRecoveryPreservesFieldsAndDecodesOneReferenceLayer(t *testing.T) {
	reply := "我来核对原文\n保留 \\\"literal\\\" 与 <mediaId>。"
	raw, _ := json.Marshal(map[string]any{"source_refs": `["u1"]`, "purpose": "查证审批数据导出范围", "intent": "lookup", "reply": reply, "context": "原引用及限制不改写"})
	original := functionCall{ID: "raw-call", Name: "start_work", Arguments: string(raw)}
	recovered, matched, err := recoverTerminalActionCall(original)
	if err != nil || !matched || recovered.ID != original.ID || recovered.Name != toolFinish || original.Arguments != string(raw) {
		t.Fatalf("recovery failed: %#v matched=%t err=%v", recovered, matched, err)
	}
	var input struct{ Actions []CoordinationAction }
	if err := json.Unmarshal([]byte(recovered.Arguments), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Actions) != 1 || input.Actions[0].Kind != "start_work" || input.Actions[0].Reply != reply || input.Actions[0].Context != "原引用及限制不改写" || !reflect.DeepEqual(input.Actions[0].SourceRefs, []string{"u1"}) {
		t.Fatalf("recovery changed action content: %#v", input.Actions)
	}
}

func TestTerminalCallRecoveryRejectsAmbiguityAndNeverAddsGenericReply(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"continue_work","source_refs":["u1"]}`,
		`{"kind":null}`,
		`{"kind":"start_work","kind":"start_work"}`,
		`{"source_refs":["u1"],"source_refs":["u2"]}`,
		`[{"kind":"start_work"}]`, `null`, `{}` + `{}`,
		`{"source_refs":"u1,u2"}`,
		`{"source_refs":"\"[\\\"u1\\\"]\""}`,
		`{"source_refs":"[1]"}`, `{"source_refs":"[null]"}`, `{"source_refs":"null"}`,
	} {
		if _, matched, err := recoverTerminalActionCall(functionCall{Name: "start_work", Arguments: raw}); !matched || err == nil {
			t.Fatalf("ambiguous action recovered: %s matched=%t err=%v", raw, matched, err)
		}
	}
	for _, name := range []string{"reply", "issue", "finish", "issue_comment_add", "delete_issue", "work_state"} {
		original := functionCall{ID: "raw", Name: name, Arguments: `{"text":"not authorized"}`}
		got, matched, err := recoverTerminalActionCall(original)
		if matched || err != nil || got != original {
			t.Fatalf("unadvertised alias was recovered: %s", name)
		}
	}
	// Unknown fields must reach the strict parser, never disappear in recovery.
	got, _, err := recoverTerminalActionCall(functionCall{Name: "acknowledge", Arguments: `{"source_refs":["u1"],"ack_kind":"greeting","reply":"你好","hidden_write":"send"}`})
	if err != nil || !strings.Contains(got.Arguments, "hidden_write") {
		t.Fatal("unknown field was removed")
	}
	if _, err := parseValidatedWindowPlan(got.Arguments, Turn{Source: SourceWeb, Message: "你好"}, nil, nil); err == nil {
		t.Fatal("recovery bypassed strict action fields")
	}
}

func TestTerminalCallRecoveryMatchesExistingActionCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range []openai.ChatCompletionToolUnionParam{windowPlanTool(true), coordinatorTaskFinishedFinishTool()} {
		raw, _ := json.Marshal(def.GetFunction().Parameters)
		var schema struct {
			Properties struct {
				Actions struct {
					Items struct {
						Properties struct {
							Kind struct{ Enum []string }
						}
					}
				}
			}
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, name := range schema.Properties.Actions.Items.Properties.Kind.Enum {
			seen[name] = true
			if !isRecoverableTerminalAction(name) {
				t.Fatalf("existing action cannot be recovered: %s", name)
			}
		}
	}
	if len(seen) != 10 {
		t.Fatalf("action catalog changed: %#v", seen)
	}
}

func TestTerminalCallRecoveryUsesSameReviewAndPersistenceGate(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("original-call", "start_work", recoveredWorkArgs)}}
	saved := []Decision{}
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(d Decision) error { saved = append(saved, d); return nil })
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "用户", Message: "查证审批数据导出范围"})
	if err != nil || d.Action != ActionIssue || len(saved) != 1 || len(d.Items) != 1 || chat.calls != 1 || chat.checkCalls != 1 {
		t.Fatalf("recovered proposal did not use the standard path: %#v err=%v saves=%d checks=%d", d, err, len(saved), chat.checkCalls)
	}
	if !reflect.DeepEqual(d.ToolsUsed, []string{toolAssocRecall, "start_work"}) || d.ToolRounds != 1 {
		t.Fatalf("actual raw calls missing: %#v", d)
	}
	if len(d.Steps) < 4 || d.Steps[2].Tool != "start_work" || d.Steps[2].Input != recoveredWorkArgs || !strings.Contains(d.Steps[3].Content, "not yet validated") || !strings.Contains(d.Steps[3].Output, `"actions"`) {
		t.Fatalf("raw model call or normalization provenance lost: %#v", d.Steps)
	}
}

func TestTerminalCallRecoveryReviewFailureRetainsAttemptCounters(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("raw", "start_work", recoveredWorkArgs)}, checkError: errors.New("review unavailable")}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
	d, err := (&Coordinator{Chat: chat}).runLoop(ctx, Turn{Source: SourceWeb, SenderName: "用户", Message: "查证审批数据导出范围"})
	if err == nil || d.Action != ActionDeferred || saves != 0 || d.ToolRounds != 1 || !reflect.DeepEqual(d.ToolsUsed, []string{"start_work"}) || chat.checkCalls != 1 {
		t.Fatalf("failed review lost the attempted terminal call: %#v err=%v saves=%d", d, err, saves)
	}
}

func TestTerminalCallRecoveryCannotBypassRecallCoverageOrReview(t *testing.T) {
	for _, tc := range []struct {
		name, arguments string
		tools           *stubTools
		rejectReview    bool
	}{
		{"recall unavailable", recoveredWorkArgs, &stubTools{errors: map[string]error{toolAssocRecall: errors.New("unavailable")}}, false},
		{"source unknown", strings.Replace(recoveredWorkArgs, "u1", "u99", 1), &stubTools{}, false},
		{"review rejects", recoveredWorkArgs, &stubTools{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := &scriptedCompleter{}
			for range maxLoopRounds {
				chat.rounds = append(chat.rounds, assistantTool("raw", "start_work", tc.arguments))
			}
			if tc.rejectReview {
				chat.checkRounds = []openai.ChatCompletion{scriptedFinishVerdict("revise", "The proposed work is not authorized.", "u1")}
			}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat, Tools: tc.tools}).runLoop(ctx, Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "用户", Message: "查证审批数据导出范围"})
			if err == nil || d.Action != ActionDeferred || saves != 0 || d.ToolRounds != maxLoopRounds || len(d.ToolsUsed) != maxLoopRounds+1 {
				t.Fatalf("guard bypass or lost failure counters: %#v err=%v saves=%d", d, err, saves)
			}
			if (!tc.rejectReview && chat.checkCalls != 0) || (tc.rejectReview && chat.checkCalls != 1) {
				t.Fatalf("unexpected review calls: %d", chat.checkCalls)
			}
		})
	}
}

func TestTerminalCallRecoveryCannotFinishAlongsideUnreadEvidence(t *testing.T) {
	combined := assistantTool("raw", "start_work", recoveredWorkArgs)
	read := assistantTool("read", toolAssocRecall, `{}`)
	combined.Choices[0].Message.ToolCalls = append(combined.Choices[0].Message.ToolCalls, read.Choices[0].Message.ToolCalls...)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{combined, assistantTool("alone", "start_work", recoveredWorkArgs)}}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{}}).runLoop(ctx, Turn{Source: SourceWeb, SenderName: "用户", Message: "查证审批数据导出范围"})
	if err != nil || d.ToolRounds != 2 || saves != 1 || chat.checkCalls != 1 {
		t.Fatalf("combined terminal/read response bypassed next-round evidence: %#v %v", d, err)
	}
}

type terminalRecoveryHistory struct {
	lines []HistoryLine
	err   error
	calls int
}

func (h *terminalRecoveryHistory) Load(context.Context, Turn) ([]HistoryLine, error) {
	h.calls++
	return h.lines, h.err
}

func recoveryTurn() Turn {
	return Turn{Source: SourceDigitalEmployee, ConversationID: "cid-current", SenderName: "用户", Message: "按确认的范围起草说明，不要发送。", HistoryStatus: "not_loaded", HistoryBefore: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func TestTerminalCallRecoveryHistoryMarkerComesFromParser(t *testing.T) {
	call, _, err := recoverTerminalActionCall(functionCall{Name: "continue_work", Arguments: recoveredAnswerArgs})
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseValidatedWindowPlan(call.Arguments, recoveryTurn(), []recallCall{{ConversationID: "cid-current"}}, map[string]struct{}{recoveryIssueID: {}})
	if err == nil || !isHistoryPrerequisiteError(err) || !isHistoryPrerequisiteError(errors.Join(errors.New("outer context"), err)) {
		t.Fatalf("parser did not return a stable history prerequisite: %T %v", err, err)
	}
	if isHistoryPrerequisiteError(errors.New(err.Error())) || isHistoryPrerequisiteError(hintErr(err.Error(), "a reviewer repeated the same words")) {
		t.Fatal("free-form text impersonated a Host read prerequisite")
	}
}

func TestTerminalCallRecoveryRefreshesOnlySatisfiedHistoryPrerequisite(t *testing.T) {
	for _, keepReview := range []bool{false, true} {
		t.Run(map[bool]string{false: "read prerequisite", true: "unresolved review survives"}[keepReview], func(t *testing.T) {
			chat := &scriptedCompleter{}
			if keepReview {
				chat.rounds = append(chat.rounds, assistantTool("overreach", "start_work", recoveredWorkArgs))
				chat.checkRounds = []openai.ChatCompletion{scriptedFinishVerdict("revise", "UNRESOLVED_REVIEW: draft only; no sending.", "u1"), scriptedFinishVerdict("allow", "The corrected draft is authorized.")}
			}
			chat.rounds = append(chat.rounds,
				assistantTool("answer-before-read", "continue_work", recoveredAnswerArgs),
				assistantTool("history", toolContextRead, `{"kind":"history"}`),
				assistantTool("answer-after-read", "continue_work", recoveredAnswerArgs))
			history := &terminalRecoveryHistory{lines: []HistoryLine{{Role: "assistant", Content: "是否按确认的范围起草审批导出说明，暂不发送？"}}}
			saves := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
			d, err := (&Coordinator{Chat: chat, Tools: &stubTools{recall: recoveryRecall}, DWSHistory: history}).runLoop(ctx, recoveryTurn())
			if err != nil || d.Action != ActionIssue || len(d.Items) != 1 || d.Items[0].IssueID != recoveryIssueID || saves != 1 || history.calls != 1 {
				t.Fatalf("history recovery failed: %#v %v saves=%d history=%d", d, err, saves, history.calls)
			}
			last, _ := json.Marshal(chat.params[len(chat.params)-1].Messages)
			if strings.Contains(string(last), "requires original question evidence") {
				t.Fatal("satisfied read prerequisite remained in repair feedback")
			}
			if keepReview {
				if !strings.Contains(string(last), "UNRESOLVED_REVIEW") || chat.checkCalls != 2 {
					t.Fatal("history success erased a semantic review restriction")
				}
			} else if !strings.Contains(string(last), "read_prerequisite_satisfied") || !strings.Contains(string(last), "does not prove") || chat.checkCalls != 1 {
				t.Fatal("read success was not distinguished from matching-question authorization")
			}
		})
	}
}

func TestTerminalCallRecoveryDoesNotTreatEmptyOrFailedHistoryAsConsent(t *testing.T) {
	for _, failure := range []error{nil, errors.New("history unavailable")} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
			assistantTool("before", "continue_work", recoveredAnswerArgs), assistantTool("read", toolContextRead, `{"kind":"history"}`), assistantTool("after", "continue_work", recoveredAnswerArgs),
		}}
		history := &terminalRecoveryHistory{err: failure}
		d, err := (&Coordinator{Chat: chat, Tools: &stubTools{recall: recoveryRecall}, DWSHistory: history}).runLoop(context.Background(), recoveryTurn())
		if err == nil || d.Action != ActionDeferred || chat.checkCalls != 0 || len(d.Items) != 0 {
			t.Fatalf("missing history authorized an answer: %#v %v", d, err)
		}
		last, _ := json.Marshal(chat.params[2].Messages)
		if strings.Contains(string(last), "read_prerequisite_satisfied") {
			t.Fatal("unavailable/empty history was described as a satisfied prerequisite")
		}
	}
}

func TestTerminalCallRecoveryLoadedHistoryStillNeedsSemanticApproval(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("before", "continue_work", recoveredAnswerArgs),
		assistantTool("read", toolContextRead, `{"kind":"history"}`),
	}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "The loaded question belongs to another conversation participant, not this employee.", "u1")}}
	for range maxLoopRounds - 2 {
		chat.rounds = append(chat.rounds, assistantTool("after", "continue_work", recoveredAnswerArgs))
	}
	history := &terminalRecoveryHistory{lines: []HistoryLine{{Role: "user", SenderID: "other-person", Content: "小王，你准备起草说明吗？"}}}
	saves := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { saves++; return nil })
	d, err := (&Coordinator{Chat: chat, Tools: &stubTools{recall: recoveryRecall}, DWSHistory: history}).runLoop(ctx, recoveryTurn())
	if err == nil || d.Action != ActionDeferred || saves != 0 || history.calls != 1 || chat.checkCalls != 1 {
		t.Fatalf("loaded history bypassed semantic review: %#v err=%v saves=%d checks=%d", d, err, saves, chat.checkCalls)
	}
}
