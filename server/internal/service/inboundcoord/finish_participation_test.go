package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
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

func TestParticipationHistoryAttemptClearsOnlyReadFeedback(t *testing.T) {
	for _, status := range []string{"loaded", "empty", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			ignore := `{"actions":[{"kind":"ignore","source_refs":["u1"],"reason":"No grounded respondent"}]}`
			chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
				assistantTool("before", toolFinish, ignore),
				assistantTool("read", toolContextRead, `{"kind":"history"}`),
				assistantTool("after", toolFinish, ignore),
			}}
			history := &terminalRecoveryHistory{}
			if status == "loaded" {
				history.lines = []HistoryLine{{Role: "user", Content: "Someone else's discussion"}}
			} else if status == "unavailable" {
				history.err = errors.New("history unavailable")
			}
			turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, ConversationID: "cid-current", HistoryStatus: "not_loaded", Message: "Can you check this?"}
			d, err := (&Coordinator{Chat: chat, DWSHistory: history, Tools: &stubTools{}}).runLoop(context.Background(), turn)
			if err != nil || d.Action != ActionSilence || history.calls != 1 || chat.checkCalls != 2 {
				t.Fatalf("history attempt was not re-evaluated: decision=%s err=%v reads=%d reviews=%d", d.Action, err, history.calls, chat.checkCalls)
			}
			last, _ := json.Marshal(chat.params[len(chat.params)-1].Messages)
			if strings.Contains(string(last), "Source u1 has no grounded respondent") || !strings.Contains(string(last), "do not repeat the read") || !strings.Contains(string(last), "does not prove absence") {
				t.Fatal("completed attempt retained the read instruction or lost uncertainty")
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

func TestIgnoreNeedsDialogueEvidenceWhenRecipientIsUnresolved(t *testing.T) {
	for _, tc := range []struct{ name, status, basis, quote, want string }{
		{"unread pronoun followup", "not_loaded", "unknown", "", "revise"},
		{"other without evidence", "not_loaded", "other", "", "revise"},
		{"explicit different respondent", "not_loaded", "other", "Riley", "allow"},
		{"loaded history evaluated", "loaded", "unknown", "", "allow"},
		{"empty history evaluated", "empty", "unknown", "", "allow"},
		{"unavailable must not reread forever", "unavailable", "unknown", "", "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, HistoryStatus: tc.status, ConversationID: "cid-test", Message: "Riley, can you check this?"}
			result := finishCheckResult{Verdict: "allow", ParticipationChecks: []finishParticipationCheck{{SourceRefs: []string{"u1"}, Basis: tc.basis, RecipientQuote: tc.quote, Disposition: "ignore"}}}
			decision := Decision{CoordinationActions: []CoordinationAction{{Kind: "ignore", SourceRefs: []string{"u1"}}}}
			if err := validateFinishParticipationChecks(&result, turn, decision); err != nil || result.Verdict != tc.want {
				t.Fatalf("verdict=%s err=%v", result.Verdict, err)
			}
		})
	}
}

func TestFormalOtherMentionCannotBecomeSameNameDirectEvidence(t *testing.T) {
	for _, tc := range []struct{ name, text, quote, uid, want string }{
		{"same display name", "@同名(别称) 你好", "@同名(别称)", "other", "protocol_error"},
		{"at prefix stripped", "@同名(别称) 你好", "同名", "other", "protocol_error"},
		{"alias substring", "@同名(别称) 你好", "别称", "other", "protocol_error"},
		{"spaced alias substring", "@同名(Other Name) 你好", "Name", "other", "protocol_error"},
		{"square bracket alias substring", "@[同名] 你好", "同名", "other", "protocol_error"},
		{"wide bracket alias substring", "@【同名】 你好", "同名", "other", "protocol_error"},
		{"unbounded multiword name", "@Alex Smith 你好", "Smith", "other", "protocol_error"},
		{"natural name after bracketed mention", "@[其他人] 同名请答一下", "同名", "other", "allow"},
		{"angle UID notation", "<@other> 你好", "other", "other", "protocol_error"},
		{"angle display notation", "<@同名> 你好", "同名", "other", "protocol_error"},
		{"independent same name occurrence", "@同名 请先等一下。同名，你也说说", "同名", "other", "allow"},
		{"other beneficiary mention", "同名，帮一下 @受益人", "同名", "other", "allow"},
		{"independent natural address", "@别人 先等等，同名请答一下", "同名", "other", "allow"},
		{"self UID despite new name", "@新昵称 你好", "", "self", "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, proactive := range []bool{false, true} {
				turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: proactive, DWSUID: "self", EmployeeAccountName: "同名", Utterances: []WindowUtterance{{Text: tc.text, Mentions: []MessageMention{{UID: tc.uid}}}}}
				d := Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", AckKind: "greeting", SourceRefs: []string{"u1"}, Reply: "你好"}}}
				r := finishCheckResult{Verdict: "allow", ParticipationChecks: []finishParticipationCheck{{SourceRefs: []string{"u1"}, Basis: "direct", RecipientQuote: tc.quote, Disposition: "coordinate"}}}
				if err := validateFinishParticipationChecks(&r, turn, d); (tc.want == "protocol_error" && err == nil) || (tc.want != "protocol_error" && (err != nil || r.Verdict != tc.want)) {
					t.Fatalf("proactive=%t result=%+v err=%v", proactive, r, err)
				}
			}
		})
	}
}

func TestOtherMentionsStillPermitIndependentInvitationAndDialogue(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "self", HistoryStatus: "loaded", Utterances: []WindowUtterance{{Text: "@其他人 先等等，大家谁能帮忙看一下", Mentions: []MessageMention{{UID: "other"}}}}, CoordinationReads: []CoordinationRead{{ReadRef: "r1", Tool: toolContextRead, Kind: "history", Result: json.RawMessage(`{"status":"loaded"}`)}}}
	d := Decision{CoordinationActions: []CoordinationAction{{Kind: "acknowledge", SourceRefs: []string{"u1"}, Reply: "我在"}}}
	for _, check := range []finishParticipationCheck{
		{SourceRefs: []string{"u1"}, Basis: "open_call", RecipientQuote: "大家", Disposition: "coordinate"},
		{SourceRefs: []string{"u1"}, Basis: "dialogue", EvidenceRef: "r1", Disposition: "coordinate"},
	} {
		r := finishCheckResult{Verdict: "allow", ParticipationChecks: []finishParticipationCheck{check}}
		if err := validateFinishParticipationChecks(&r, turn, d); err != nil || r.Verdict != "allow" {
			t.Fatalf("independent basis rejected: %+v err=%v", r, err)
		}
	}
}

func TestParticipationReviewAppliesToOrdinaryDigitalEmployeeMentions(t *testing.T) {
	for _, tc := range []struct {
		name string
		turn Turn
		want bool
	}{
		{"ordinary explicit mentions", Turn{Source: SourceDigitalEmployee, ChatType: "group", Utterances: []WindowUtterance{{Text: "你好", Mentions: []MessageMention{{UID: "other"}}}}}, true},
		{"ordinary legacy no metadata", Turn{Source: SourceDigitalEmployee, ChatType: "group", Message: "你好", Addressed: true}, false},
		{"ordinary explicit empty metadata", Turn{Source: SourceDigitalEmployee, ChatType: "group", Utterances: []WindowUtterance{{Text: "你好", Mentions: []MessageMention{}}}}, false},
		{"proactive unchanged", Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, Message: "你好"}, true},
		{"completion exempt", Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, Loop: LoopTaskFinished}, false},
		{"DM unchanged", Turn{Source: SourceDigitalEmployee, ChatType: "single", Utterances: []WindowUtterance{{Text: "你好", Mentions: []MessageMention{{UID: "other"}}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requiresParticipationCheck(tc.turn); got != tc.want {
				t.Fatalf("got %t want %t", got, tc.want)
			}
		})
	}
}

func TestOtherUIDContradictoryReviewIsProtocolErrorForBothVerdicts(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "self", Utterances: []WindowUtterance{{Text: "@同名 你好", Mentions: []MessageMention{{UID: "other"}}}}}
	for _, verdict := range []string{"allow", "revise"} {
		r := finishCheckResult{Verdict: verdict, Reason: "Untrusted recipient judgment", ParticipationChecks: []finishParticipationCheck{{SourceRefs: []string{"u1"}, Basis: "direct", RecipientQuote: "同名", Disposition: "coordinate"}}}
		d := Decision{Action: ActionSilence, CoordinationActions: []CoordinationAction{{Kind: "ignore", SourceRefs: []string{"u1"}, Reason: "another UID"}}}
		err := validateFinishParticipationChecks(&r, turn, d)
		if err == nil || !strings.Contains(err.Error(), "other_only") || !strings.Contains(err.Error(), "receiving UID") || !strings.Contains(err.Error(), "Correct participation_checks") || r.Verdict != verdict {
			t.Fatalf("invalid reviewer evidence must be repaired, not allowed or passed to routing: %+v err=%v", r, err)
		}
	}
}

func TestOtherUIDWrongReviseUsesExistingReviewerProtocolRepair(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, ChatType: "group", DWSUID: "self", Message: "@同名 你好", Utterances: []WindowUtterance{{Text: "@同名 你好", Mentions: []MessageMention{{UID: "other"}}}}}
	d := Decision{Action: ActionSilence, CoordinationActions: []CoordinationAction{{Kind: "ignore", SourceRefs: []string{"u1"}, Reason: "explicit other UID"}}}
	makeReview := func(verdict, basis, disposition string) openai.ChatCompletion {
		raw, _ := json.Marshal(map[string]any{"verdict": verdict, "reason": "recipient evidence checked", "missing_source_refs": []string{}, "request_quote_ref": scriptedRequestQuoteRef, "candidate_quote_ref": scriptedCandidateQuoteRef, "work_checks": []any{}, "constraint_quote": "", "participation_checks": []finishParticipationCheck{{SourceRefs: []string{"u1"}, Basis: basis, RecipientQuote: "@同名", Disposition: disposition}}})
		return assistantTool("review", toolFinishCheck, string(raw))
	}
	chat := &scriptedCompleter{checkRounds: []openai.ChatCompletion{makeReview("revise", "direct", "coordinate"), makeReview("allow", "other", "ignore")}}
	result, err := (&Coordinator{Chat: chat}).checkFinish(context.Background(), turn, d, nil, 0, nil)
	if err != nil || result.Verdict != "allow" || chat.checkCalls != 2 {
		t.Fatalf("review repair did not recover ordinary ignore: result=%+v checks=%d err=%v", result, chat.checkCalls, err)
	}
}
