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
