package inboundcoord

import (
	"fmt"
	"testing"
)

func TestParticipationJudgmentMustAgreeWithCandidateAndGroundItsEvidence(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, Message: "Morgan, are you available?", EmployeeAccountName: "Morgan"}
	for _, tc := range []struct {
		name, kind, basis, quote, disposition, want string
	}{
		{"named greeting", "acknowledge", "direct", "Morgan", "coordinate", "allow"},
		{"false ignore", "ignore", "direct", "Morgan", "coordinate", "revise"},
		{"uninvited reply", "acknowledge", "unknown", "", "ignore", "revise"},
		{"invented recipient quote", "acknowledge", "direct", "Riley", "coordinate", "revise"},
		{"reminder cannot start work", "continue_work", "direct", "Morgan", "coordinate", "revise"},
		{"unknown dialogue", "acknowledge", "dialogue", "", "coordinate", "revise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decision{CoordinationActions: []CoordinationAction{{Kind: tc.kind, SourceRefs: []string{"u1"}}}}
			r := finishCheckResult{Verdict: "allow", ParticipationChecks: []finishParticipationCheck{{SourceRefs: []string{"u1"}, Basis: tc.basis, RecipientQuote: tc.quote, Disposition: tc.disposition}}}
			if err := validateFinishParticipationChecks(&r, turn, d); err != nil || r.Verdict != tc.want {
				t.Fatalf("verdict=%s err=%v", r.Verdict, err)
			}
		})
	}
}

func TestParticipationCanGroupBurstButCannotOmitOrRepeatSources(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true}
	refs := []string{}
	for i := 0; i < 100; i++ {
		turn.Utterances = append(turn.Utterances, WindowUtterance{Text: "Unrelated discussion"})
		refs = append(refs, fmt.Sprintf("u%d", i+1))
	}
	d := Decision{CoordinationActions: []CoordinationAction{{Kind: "ignore", SourceRefs: refs}}}
	r := finishCheckResult{Verdict: "allow", ParticipationChecks: []finishParticipationCheck{{SourceRefs: refs, Basis: "other", Disposition: "ignore"}}}
	if err := validateFinishParticipationChecks(&r, turn, d); err != nil || r.Verdict != "allow" {
		t.Fatalf("compact burst judgment failed: %v", err)
	}
	r.ParticipationChecks[0].SourceRefs = refs[:99]
	if err := validateFinishParticipationChecks(&r, turn, d); err == nil {
		t.Fatal("omitted source accepted")
	}
	r.ParticipationChecks[0].SourceRefs = append(append([]string{}, refs...), "u1")
	if err := validateFinishParticipationChecks(&r, turn, d); err == nil {
		t.Fatal("duplicate source accepted")
	}
}

func TestProactiveAddressFlagDescribesOnlyMentionMetadata(t *testing.T) {
	turn := Turn{ProactiveConversation: true, ChatType: "group"}
	if modelAddressingField(turn) != "has_explicit_employee_mention" {
		t.Fatal("router mention hint became a semantic addressing verdict")
	}
	turn.ProactiveConversation = false
	if modelAddressingField(turn) != "addressed" {
		t.Fatal("ordinary inbound addressing contract changed")
	}
}
