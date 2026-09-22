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
		{"work receipt is not standalone reply", "reply", Decision{Action: ActionReply, UserText: "我这就新建工作", CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "receipt", Reply: "我这就新建工作"}}}, false},
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
	if !strings.Contains(prompt, "[policy:user_decision@10]") {
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
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("choices", "propose_choices", proposal), assistantTool("review", "review_choices", `{"allowed":true,"labels_match_targets":true,"reason":"supported alternatives"}`)}}
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
	if d.UserDecision.ProposalPromptHash != policyHash(chat.params[0].Messages[0].OfSystem.Content.OfString.Value) || d.UserDecision.ProposalReviewPromptHash != policyHash(userDecisionPolicy()) {
		t.Fatal("proposal prompt provenance differs from actual request")
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
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("invalid", "propose_choices", `{"question":"怎么处理？","options":[]}`), assistantTool("valid", "propose_choices", valid), assistantTool("review", "review_choices", `{"allowed":true,"labels_match_targets":true,"reason":"supported alternatives"}`)}}
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
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("interpret", "interpret_submission", tt.output)}}
			c := &Coordinator{Chat: chat}
			s := UserDecisionSnapshot{Turn: Turn{Source: SourceRobot, Addressed: true, Message: "整理报告"}, Proposal: userdecision.Proposal{Options: []userdecision.Option{{ID: "new", Kind: "start_work", Plan: json.RawMessage(newPlan)}, {ID: "reply", Kind: "reply", Label: "原来的问候", Plan: json.RawMessage(replyPlan)}}}}
			commits := 0
			ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
			d, audit, err := c.ResolveUserDecisionWithAudit(ctx, s, userdecision.Submission{OptionID: tt.option, Custom: tt.custom})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if err == nil {
				if len(chat.checkParams) == 0 {
					t.Fatal("submission skipped final review")
				}
				body, _ := json.Marshal(chat.checkParams[0].Messages[0])
				if !strings.Contains(string(body), "[policy:user_decision@10]") {
					t.Fatal("actual final-review request lost submission policy")
				}
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

func TestUserDecisionSemanticReviewRepairsMislabeledReceipt(t *testing.T) {
	turn := Turn{Source: SourceRobot, Addressed: true, Message: "整理报告", UserDecisionEnabled: true}
	bad := `{"question":"怎么处理？","recommended_id":"new","options":[{"id":"new","label":"新建工作","kind":"start_work","plan":{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"整理报告"}]}},{"id":"reply","label":"回复","kind":"reply","plan":{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"conversation","reply":"我将继续修改报告。"}]}}]}`
	good := strings.ReplaceAll(bad, "我将继续修改报告。", "当前材料是一份报告。")
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("bad", "propose_choices", bad),
		assistantTool("reject", "review_choices", `{"allowed":false,"labels_match_targets":true,"reason":"reply promises execution despite conversation label"}`),
		assistantTool("good", "propose_choices", good),
		assistantTool("allow", "review_choices", `{"allowed":true,"labels_match_targets":true,"reason":"reply is standalone"}`),
	}}
	commits := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
	d, err := (&Coordinator{Chat: chat}).proposeUserDecision(ctx, turn, nil, nil, nil, Decision{Action: ActionIssue})
	if err != nil {
		t.Fatal(err)
	}
	if commits != 0 || d.UserDecision == nil || len(d.UserDecision.ProposalReviews) != 2 || len(d.UserDecision.ProposalAttempts) != 2 {
		t.Fatal("review evidence missing or premature execution")
	}
	if strings.Contains(d.UserDecision.Proposal.Options[1].Label, "继续修改") {
		t.Fatal("rejected reply shown")
	}
}

func TestUserDecisionInterpretationRepairIsBoundedAndAudited(t *testing.T) {
	const invalid = `{"executable":true,"reason":"copied invalid reference","plan":{"actions":[{"kind":"continue_work","source_refs":["u1"],"issue_id":"bad-id","purpose":"整理一份完整可转发报告","basis":"change"}]}}`
	const valid = `{"executable":true,"reason":"use requested new work","plan":{"actions":[{"kind":"start_work","source_refs":["u1"],"purpose":"整理一份完整可转发报告"}]}}`
	for _, exhausted := range []bool{false, true} {
		rounds := []openai.ChatCompletion{assistantTool("invalid", "interpret_submission", invalid), assistantTool("valid", "interpret_submission", valid)}
		if exhausted {
			rounds = []openai.ChatCompletion{assistantTool("bad1", "interpret_submission", invalid), assistantTool("bad2", "interpret_submission", invalid), assistantTool("bad3", "interpret_submission", invalid)}
		}
		c := &Coordinator{Chat: &scriptedCompleter{rounds: rounds}}
		s := UserDecisionSnapshot{Turn: Turn{Source: SourceRobot, Addressed: true, Message: "新建一份报告"}}
		commits := 0
		ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
		d, audit, err := c.ResolveUserDecisionWithAudit(ctx, s, userdecision.Submission{Custom: "新建一份完整报告"})
		if (err != nil) != exhausted || commits != 0 || len(audit.InterpretationAttempts) != len(rounds) {
			t.Fatalf("exhausted=%v attempts=%d commits=%d error=%v", exhausted, len(audit.InterpretationAttempts), commits, err)
		}
		if !exhausted && d.Action != ActionIssue {
			t.Fatal("valid repaired work missing")
		}
	}
}

func TestUserDecisionReviewReceivesWorkAlternatives(t *testing.T) {
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("review", "review_choices", `{"allowed":true,"reason":"standalone reply"}`), assistantTool("labels", "review_task_labels", `{"allowed":true,"labels_match_targets":true,"reason":"all labels grounded"}`)}}
	proposal := userdecision.Proposal{Question: "如何处理？", Options: []userdecision.Option{
		{ID: "continue", Kind: "continue_work", Label: "客户周报", Plan: json.RawMessage(`{"actions":[{"kind":"continue_work","issue_id":"actual-task"}]}`)},
		{ID: "reply", Kind: "reply", Label: "直接回复：你好", Plan: json.RawMessage(`{"actions":[{"kind":"acknowledge","reply":"你好"}]}`)},
	}}
	_, err := (&Coordinator{Chat: chat}).reviewUserDecisionProposal(context.Background(), Turn{}, []byte(`{"recalled_tasks":[]}`), proposal)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Options []userdecision.Option `json:"continuation_options"`
	}
	if json.Unmarshal([]byte(chat.params[1].Messages[1].OfUser.Content.OfString.Value), &input) != nil || len(input.Options) != 1 || input.Options[0].Label != "客户周报" || string(input.Options[0].Plan) != string(proposal.Options[0].Plan) {
		t.Fatal("work label and frozen target absent from semantic review")
	}
}

func TestUserDecisionLabelGroundingCannotBeOverriddenByAllowed(t *testing.T) {
	for _, raw := range []string{`{"allowed":true,"labels_match_targets":false,"reason":"wrong task identity"}`, `{"allowed":true,"reason":"missing grounding verdict"}`} {
		chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("review", "review_task_labels", raw)}}
		_, err := (&Coordinator{Chat: chat}).reviewUserDecisionProposalPart(context.Background(), []byte(`{}`), userdecision.Proposal{Options: []userdecision.Option{{ID: "reply", Kind: "reply", Plan: json.RawMessage(`{}`)}}}, true)
		if err == nil {
			t.Fatal("ungrounded labels passed despite separate verdict")
		}
	}
}

func TestUserDecisionTrustedCustomIsVerbatimReviewEvidence(t *testing.T) {
	turn := Turn{Message: "旧回复", UserDecisionSubmission: &userdecision.Submission{Custom: "改为只回复：新的回复"}}
	if !finishConstraintQuoteValid("新的回复", turn) || finishConstraintQuoteValid("另一个回复", turn) {
		t.Fatal("trusted custom was not validated verbatim")
	}
	turn.UserDecisionSubmission = nil
	if finishConstraintQuoteValid("新的回复", turn) {
		t.Fatal("absent submission became trusted evidence")
	}
}

func TestUserDecisionSemanticRepairUsesLatestSubmissionOnce(t *testing.T) {
	old := `{"executable":true,"reason":"old wording","plan":{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"conversation","reply":"旧回复"}]}}`
	latest := strings.ReplaceAll(old, "旧回复", "新回复")
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{assistantTool("old", "interpret_submission", old), assistantTool("new", "interpret_submission", latest)}, checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("revise", "Follow latest submission: 新回复"), scriptedFinishVerdict("allow", "Latest submission followed")}}
	commits := 0
	ctx := ContextWithPlanCheckpoint(context.Background(), nil, func(Decision) error { commits++; return nil })
	d, a, err := (&Coordinator{Chat: chat}).ResolveUserDecisionWithAudit(ctx, UserDecisionSnapshot{Turn: Turn{Source: SourceRobot, Addressed: true, Message: "请只回复旧回复"}}, userdecision.Submission{Custom: "改为只回复新回复"})
	if err != nil || d.UserText != "新回复" || commits != 0 || len(a.InterpretationAttempts) != 2 || len(a.ReviewAttempts) != 2 {
		t.Fatalf("bounded semantic repair lost: %v text=%s attempts=%d reviews=%d commits=%d", err, d.UserText, len(a.InterpretationAttempts), len(a.ReviewAttempts), commits)
	}
	if a.InterpretationPolicy == nil || len(a.InterpretationPolicy.Modules) != 1 || a.InterpretationPolicy.Modules[0].ID != "user_decision" {
		t.Fatal("automatic routing policy leaked into submission interpretation")
	}
	if a.InterpretationPolicy == nil || a.InterpretationPolicy.PromptHash != policyHash(chat.params[0].Messages[0].OfSystem.Content.OfString.Value) {
		t.Fatal("interpretation policy not captured from actual prompt")
	}
	review := a.Review.(finishCheckResult)
	if review.Policy == nil || review.Policy.PromptHash != policyHash(chat.checkParams[1].Messages[0].OfSystem.Content.OfString.Value) {
		t.Fatal("review policy not captured from actual prompt")
	}
	last := chat.params[0].Messages[len(chat.params[0].Messages)-1].OfUser.Content.OfString.Value
	if !strings.Contains(last, "user_decision_submission") || !strings.Contains(last, "改为只回复新回复") {
		t.Fatal("trusted latest submission not emphasized after frozen evidence")
	}
}
