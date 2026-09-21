package inboundcoord

import (
	"context"
	"encoding/json"
	openai "github.com/openai/openai-go/v3"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/userdecision"
)

func TestUserDecisionCandidateDirections(t *testing.T) {
	tests := []struct {
		name, kind string
		d          Decision
		valid      bool
	}{
		{"continue", "continue_work", Decision{CoordinationActions: []CoordinationAction{{Kind: "continue_work", IssueID: "existing"}}}, true},
		{"new", "start_work", Decision{CoordinationActions: []CoordinationAction{{Kind: "start_work"}}}, true},
		{"switch target kind", "continue_work", Decision{CoordinationActions: []CoordinationAction{{Kind: "start_work"}}}, false},
		{"multiple targets", "continue_work", Decision{CoordinationActions: []CoordinationAction{{Kind: "continue_work", IssueID: "a"}, {Kind: "continue_work", IssueID: "b"}}}, false},
		{"concrete reply", "reply", Decision{Action: ActionReply, UserText: "你好", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", Reply: "你好"}}}, true},
		{"reply hides work", "reply", Decision{Action: ActionReply, UserText: "完成", CoordinationActions: []CoordinationAction{{Kind: "start_work"}}}, false},
		{"empty reply", "reply", Decision{Action: ActionReply}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateChoiceDirection(userdecision.Option{Kind: tt.kind, Label: tt.d.UserText}, tt.d)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}
func TestUnknownChoiceCannotCommitPlan(t *testing.T) {
	commits := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
	_, err := (&Coordinator{}).ResolveUserDecision(ctx, UserDecisionSnapshot{}, userdecision.Submission{OptionID: "forged"})
	if err == nil || commits != 0 {
		t.Fatal("unknown choice reached commit")
	}
}
func TestUserDecisionReviewPolicyIsSelected(t *testing.T) {
	turn := Turn{Loop: LoopFinishCheck, UserDecisionSubmission: &userdecision.Submission{OptionID: "o1"}}
	prompt := buildSystemPrompt(turn)
	if !strings.Contains(prompt, "[policy:user_decision@2]") {
		t.Fatal("review did not receive locked-choice policy")
	}
}
func TestFrozenQuestionSerialization(t *testing.T) {
	s := UserDecisionSnapshot{Proposal: userdecision.Proposal{Question: "怎么处理？", Options: []userdecision.Option{{ID: "o1", Plan: json.RawMessage(`{"actions":[]}`)}}}, RecalledIDs: []string{"original-task"}}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored UserDecisionSnapshot
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Proposal.Options[0].ID != "o1" || restored.RecalledIDs[0] != "original-task" {
		t.Fatal("snapshot changed")
	}
}

func TestUserDecisionCandidatesNeverSaveExecutionPlan(t *testing.T) {
	commits := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
	for _, d := range []Decision{{Action: ActionAwaitUser}, {Action: ActionIssue, UserDecision: &UserDecisionSnapshot{}}} {
		if SavePlan(ctx, d) == nil {
			t.Fatal("candidate became a committed plan")
		}
	}
	if commits != 0 {
		t.Fatal("candidate checkpoint callback invoked")
	}
}

func TestUserDecisionProposalFreezesChoicesBeforeCheckpoint(t *testing.T) {
	turn := Turn{Source: SourceRobot, Addressed: true, Message: "帮我整理一份报告", UserDecisionEnabled: true}
	proposal := `{"question":"希望如何处理？","recommended_id":"new","options":[{"id":"new","label":"新建工作：整理报告","kind":"start_work","plan":{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"整理一份报告","intent":"other"}]}},{"id":"reply","label":"直接回复：我可以帮你整理报告。","kind":"reply","plan":{"actions":[{"kind":"describe_capabilities","source_refs":["u1"],"reply":"我可以帮你整理报告。"}]}}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("choices", "propose_choices", proposal)}}
	c := &Coordinator{Chat: chat}
	commits := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
	d, err := c.proposeUserDecision(ctx, turn, nil, nil, nil, Decision{Action: ActionIssue})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionAwaitUser || d.UserDecision == nil || commits != 0 {
		t.Fatal("proposal executed or was not frozen")
	}
	if d.UserDecision.Proposal.Options[0].ID != "new" || d.UserDecision.ProposalPolicy.ID != "user_decision" {
		t.Fatal("candidate or policy provenance missing")
	}
	raw, err := json.Marshal(d.UserDecision)
	if err != nil {
		t.Fatal(err)
	}
	var restored UserDecisionSnapshot
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	resolved, err := c.ResolveUserDecision(ctx, restored, userdecision.Submission{OptionID: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Action != ActionIssue || workTarget(resolved) != "start_work:" || commits != 0 {
		t.Fatalf("changed direction or premature commit: %+v", resolved)
	}
}

func TestUserDecisionProposalRepairsBeforeShowingOneQuestion(t *testing.T) {
	turn := Turn{Source: SourceRobot, Addressed: true, Message: "整理报告", UserDecisionEnabled: true}
	valid := `{"question":"怎么处理？","recommended_id":"new","options":[{"id":"new","label":"新建工作","kind":"start_work","plan":{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"整理一份完整可转发报告"}]}},{"id":"reply","label":"模型摘要","kind":"reply","plan":{"actions":[{"kind":"acknowledge","source_refs":["u1"],"reply":"你好","ack_kind":"greeting"}]}}]}`
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("invalid", "propose_choices", `{"question":"怎么处理？","options":[]}`), assistantTool("valid", "propose_choices", valid)}}
	c := &Coordinator{Chat: chat}
	commits := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
	d, err := c.proposeUserDecision(ctx, turn, nil, nil, nil, Decision{Action: ActionIssue})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionAwaitUser || len(d.UserDecision.ProposalAttempts) != 2 || commits != 0 {
		t.Fatal("repair did not retain attempts or committed early")
	}
	if d.UserDecision.Proposal.Options[1].Label != "直接回复：“你好”" {
		t.Fatal("visible choice is not exact model reply")
	}
}

func TestUserDecisionSubmissionLocksDirection(t *testing.T) {
	const newPlan = `{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"整理一份完整可转发报告"}]}`
	const replyPlan = `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"reply":"你好","ack_kind":"greeting"}]}`
	for _, tt := range []struct {
		name, option, custom, output string
		wantErr                      bool
		action                       Action
	}{
		{"custom only", "", "请整理报告", `{"executable":true,"reason":"明确整理要求","plan":` + newPlan + `}`, false, ActionIssue},
		{"choice with refinement", "new", "整理成一页", `{"executable":true,"reason":"格式细化","plan":` + newPlan + `}`, false, ActionIssue},
		{"contradictory instructions", "new", "不要整理，直接结束", `{"executable":false,"reason":"补充与新建工作矛盾","plan":null}`, true, ""},
		{"model switches direction", "new", "整理成一页", `{"executable":true,"reason":"改成回复","plan":` + replyPlan + `}`, true, ""},
		{"reply refined", "reply", "用你好回复", `{"executable":true,"reason":"修改措辞","plan":` + replyPlan + `}`, false, ActionReply},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &Coordinator{Chat: &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("interpret", "interpret_submission", tt.output)}}}
			s := UserDecisionSnapshot{Turn: Turn{Source: SourceRobot, Addressed: true, Message: "整理报告"}, Proposal: userdecision.Proposal{Options: []userdecision.Option{{ID: "new", Kind: "start_work", Plan: json.RawMessage(newPlan)}, {ID: "reply", Kind: "reply", Label: "原来的问候", Plan: json.RawMessage(replyPlan)}}}}
			commits := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
			d, audit, err := c.ResolveUserDecisionWithAudit(ctx, s, userdecision.Submission{OptionID: tt.option, Custom: tt.custom})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err == nil && d.Action != tt.action {
				t.Fatalf("action=%s", d.Action)
			}
			if audit.RawInterpretation != tt.output || commits != 0 {
				t.Fatal("missing interpretation or premature commit")
			}
		})
	}
}
