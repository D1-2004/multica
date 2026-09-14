package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
)

func TestFinishWorkTargetJudgmentCannotBeReplacedByGeneralPermission(t *testing.T) {
	for _, tc := range []struct {
		kind, match, want string
		invalid           bool
	}{
		{"continue_work", "same_deliverable", "allow", false},
		{"continue_work", "different_deliverable", "revise", false},
		{"continue_work", "new_work", "revise", false},
		{"continue_work", "unknown", "revise", false},
		{"continue_work", "no_advancement", "revise", false},
		{"continue_work", "", "", true},
		{"continue_work", "authorized", "", true},
		{"start_work", "new_work", "allow", false},
		{"start_work", "same_deliverable", "revise", false},
	} {
		t.Run(tc.kind+"/"+tc.match, func(t *testing.T) {
			decision := Decision{CoordinationActions: []CoordinationAction{{Kind: tc.kind}}}
			result := finishCheckResult{Verdict: "allow", Reason: "The requested work is within the employee's role.", WorkChecks: []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: tc.match}}}
			err := validateFinishWorkChecks(&result, decision)
			if (err != nil) != tc.invalid || (!tc.invalid && result.Verdict != tc.want) {
				t.Fatalf("verdict=%s err=%v", result.Verdict, err)
			}
			if tc.match == "no_advancement" && !strings.Contains(result.Reason, "report_status") {
				t.Fatalf("no_advancement must name report_status, got %q", result.Reason)
			}
		})
	}
}

func TestValidateFinishWorkChecksRewritesReviewerNoAdvancement(t *testing.T) {
	result := finishCheckResult{
		Verdict: "revise", Reason: "Status ping on existing work; no substantive new input or requested execution change.",
		WorkChecks: []finishWorkCheck{{ActionRef: "a1", Deliverables: "single", TargetMatch: "no_advancement"}},
	}
	decision := Decision{CoordinationActions: []CoordinationAction{{Kind: "continue_work"}}}
	if err := validateFinishWorkChecks(&result, decision); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "revise" || !strings.Contains(result.Reason, "report_status") || strings.Contains(result.Reason, "Status ping on existing work") {
		t.Fatalf("Host must name report_status instead of keeping the canned ping reason: %#v", result)
	}
}

func TestFinishRevisionHintNamesKindChangeForStatusPing(t *testing.T) {
	hint := finishRevisionHint(finishCheckResult{Reason: noAdvancementRepairReason("a1"), MissingSourceRefs: []string{}})
	if !strings.Contains(hint, "Change the action kind") || strings.Contains(hint, "Repair the diagnosed action/field") {
		t.Fatalf("status-ping repair must not tell the model to reword the same kind: %s", hint)
	}
	if !finishRevisionRequiresKindChange(noAdvancementRepairReason("a1")) {
		t.Fatal("report_status repair must count as a kind change")
	}
}

func TestFinishWorkTargetCannotAllowAnUnloadedOriginalGoal(t *testing.T) {
	turn := Turn{Source: SourceWeb, Message: "Add the new failure sample to your investigation."}
	action := CoordinationAction{Kind: "continue_work", SourceRefs: []string{"u1"}, IssueID: "recalled-target", Basis: "change", Purpose: "Add the new failure sample", Reply: "I will add it."}
	candidate := Decision{Action: ActionIssue, UserText: action.Reply, CoordinationActions: []CoordinationAction{action}, Items: []WindowItem{{Purpose: action.Purpose, Reply: action.Reply}}}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{scriptedFinishVerdict("allow", "The new sample advances the original investigation.")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, candidate, nil, 0, nil)
	if err != nil || result.Verdict != "revise" {
		t.Fatalf("unloaded target accepted: %#v err=%v", result, err)
	}
}

func TestFinishWorkTargetProjectsExactEvidenceAndPreservesUnknown(t *testing.T) {
	const target = "recalled-target"
	recall, _ := json.Marshal(coordinatorRecallView{Status: "loaded", Items: []coordinatorRecallItem{{IssueID: target, Purpose: "Investigate failed uploads"}, {IssueID: "other", Purpose: "Write a user guide"}}})
	state, _ := json.Marshal(coordinationWorkState{IssueID: target, OriginalGoal: "Investigate failed uploads on the desktop client", Truncated: true})
	turn := Turn{CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolAssocRecall, Result: recall}, {ReadRef: "r2", Tool: toolWorkState, Result: state}}}
	got := existingWorkForFinish(turn, target)
	if got.ReadRef != "r2" || got.OriginalGoal != "Investigate failed uploads on the desktop client" || !got.Truncated {
		t.Fatalf("lost exact original goal/provenance: %#v", got)
	}
	turn.CoordinationReads[1].Failed = true
	if got = existingWorkForFinish(turn, target); got.ReadRef != "r1" || got.OriginalGoal != "Investigate failed uploads" {
		t.Fatalf("failed read replaced valid recall: %#v", got)
	}
	if got = existingWorkForFinish(turn, "unseen-target"); got.ReadStatus != "unavailable" || got.OriginalGoal != "" {
		t.Fatalf("invented target evidence: %#v", got)
	}
}
