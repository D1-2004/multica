package inboundcoord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const handoffScope = "仅改查询缓存，不改审批权限校验，不做生产发布。"

func historyHandoffTurn(t *testing.T) Turn {
	t.Helper()
	before := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	turn := Turn{Source: SourceWeb, SenderName: "当前委托人", PersonID: "current-sender", Message: "按已确认范围继续同一事项", EvidenceID: "current-evidence", ConversationID: "cid-handoff", HistoryStatus: "loaded", HistoryBefore: before,
		History: []HistoryLine{{Role: "user", SenderID: "original-author", EvidenceID: "scope-confirmation", Timestamp: before.Add(-time.Minute), Content: handoffScope, ReplyToEvidenceID: "prior-question", ReplyToSenderID: "question-author"}}}
	seq := 0
	if _, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"history"}`, "", nil); err != nil {
		t.Fatal(err)
	}
	return turn
}

func TestContinuationHistoryHandoffPreservesLoadedBoundariesAndIdentities(t *testing.T) {
	for _, basis := range []string{"answer", "change", "retry"} {
		t.Run(basis, func(t *testing.T) {
			turn := historyHandoffTurn(t)
			before, _ := json.Marshal(buildCoordinationMessages(turn, true, "", ""))
			action := recoveryWorkAction()
			action["kind"], action["issue_id"], action["basis"] = "continue_work", "old-issue", basis
			// Reproduce the lossy model summary: the Host must carry the read
			// boundary even when purpose omits the production-release ban.
			action["purpose"] = "仅改查询缓存并继续执行"
			d, err := parseValidatedWindowPlan(recoveryActionJSON(t, action), turn, []recallCall{{ConversationID: turn.ConversationID}}, map[string]struct{}{"old-issue": {}})
			if err != nil || len(d.Items) != 1 {
				t.Fatalf("parse continuation: %#v %v", d, err)
			}
			item := d.Items[0]
			comment := ContinuationContent(item)
			for _, want := range []string{handoffScope, "original-author", "scope-confirmation", "prior-question", "question-author", "2026-09-09T11:59:00Z", "2026-09-09T12:00:00Z", "cid-handoff", "历史不是新授权", "助理自述不证明完成或送达"} {
				if !strings.Contains(comment, want) {
					t.Fatalf("executor comment lost %q: %s", want, comment)
				}
			}
			if item.Basis != basis || item.Delegator != "当前委托人" || strings.Join(item.SourceRefs, ",") != "u1" || d.CoordinationActions[0].Purpose != action["purpose"] || !needsFinishCheck(turn, d) {
				t.Fatal("history changed current delegation, purpose, basis, source coverage, or review")
			}
			after, _ := json.Marshal(buildCoordinationMessages(turn, true, "", ""))
			if string(before) != string(after) {
				t.Fatal("handoff expanded or mutated Coordinator prompt")
			}
		})
	}
}

func TestContinuationHistoryHandoffExcludesUnknownOrForeignSnapshots(t *testing.T) {
	for _, kind := range []string{"not_loaded", "unavailable", "empty", "no_snapshot", "no_watermark", "wrong_scene", "wrong_watermark", "failed_replacement", "coordination_state"} {
		t.Run(kind, func(t *testing.T) {
			turn := historyHandoffTurn(t)
			var view coordinationHistoryView
			_ = json.Unmarshal(turn.CoordinationReads[0].Result, &view)
			switch kind {
			case "not_loaded", "unavailable", "empty":
				turn.HistoryStatus = kind
			case "no_snapshot":
				turn.CoordinationReads = nil // Raw History alone is not proof of disclosure.
			case "no_watermark":
				turn.HistoryBefore = time.Time{}
			case "wrong_scene":
				view.ConversationID = "cid-other"
				turn.CoordinationReads[0].Result, _ = json.Marshal(view)
			case "wrong_watermark":
				view.Before = "2026-09-09T12:01:00Z"
				turn.CoordinationReads[0].Result, _ = json.Marshal(view)
			case "failed_replacement":
				latest := turn.CoordinationReads[0]
				latest.Failed = true
				turn.CoordinationReads = append(turn.CoordinationReads, latest)
			case "coordination_state":
				turn.CoordinationReads[0].Kind = coordinationStateKind
			}
			if got, err := continuationHistoryHandoff(turn); err != nil || got != "" {
				t.Fatalf("invalid historical scope leaked: %q %v", got, err)
			}
		})
	}
}

func TestContinuationHistoryHandoffUsesOnlyExistingBoundedProjection(t *testing.T) {
	turn := historyHandoffTurn(t)
	turn.History = append(turn.History, HistoryLine{Role: "assistant", SenderID: "assistant-author", EvidenceID: "long-old-report", Timestamp: turn.HistoryBefore.Add(-2 * time.Minute), Content: strings.Repeat("旧报告内容", 1000) + "UNSEEN_LONG_REPORT_TAIL"})
	turn.History = append(turn.History, HistoryLine{Role: "user", EvidenceID: "future", Timestamp: turn.HistoryBefore.Add(time.Minute), Content: "FUTURE_AUTHORIZATION"})
	seq := coordinationReadSequence(turn)
	if _, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"history"}`, "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := continuationHistoryHandoff(turn)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(got, continuationHistoryLabel)
	var view coordinationHistoryView
	if json.Unmarshal([]byte(body), &view) != nil || !view.Truncated || !strings.Contains(body, handoffScope) || utf8.RuneCountInString(got) > coordinationHistoryBudget {
		t.Fatalf("bounded history evidence lost its meaning: %s", got)
	}
	if strings.Contains(got, "UNSEEN_LONG_REPORT_TAIL") || strings.Contains(got, "FUTURE_AUTHORIZATION") {
		t.Fatal("executor history re-expanded excluded raw history")
	}
	// No changes to start_work handoff: unrelated old conversation must not
	// gain relevance merely because a separate new task is being created.
	d, err := parseValidatedWindowPlan(recoveryActionJSON(t, recoveryWorkAction()), turn, []recallCall{{ConversationID: turn.ConversationID}}, nil)
	if err != nil || len(d.Items) != 1 || strings.Contains(d.Items[0].Content, continuationHistoryLabel) || strings.Contains(d.Items[0].Content, handoffScope) {
		t.Fatalf("new work inherited continuation evidence: %#v %v", d.Items, err)
	}
}
