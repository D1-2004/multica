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
)

func TestCoordinationContextReplacesReadAndPreservesFailure(t *testing.T) {
	turn := Turn{ConversationID: "cid-a"}
	seq := 0
	first, err := rememberCoordinationRead(&turn, &seq, toolAssocRecall, `{"conversation_id":"cid-a"}`, `{"items":[]}`, nil)
	if err != nil || !strings.Contains(first, `"read_ref":"r1"`) {
		t.Fatalf("first=%s err=%v", first, err)
	}
	second, err := rememberCoordinationRead(&turn, &seq, toolAssocRecall, `{"conversation_id":"cid-a","since":"48h","limit":5}`, `{"items":[]}`, nil)
	if err != nil || len(turn.CoordinationReads) != 1 || turn.CoordinationReads[0].ReadRef != "r2" || strings.Contains(second, `"r1"`) {
		t.Fatalf("repeat accumulated obsolete snapshot: %+v err=%v", turn.CoordinationReads, err)
	}
	_, err = rememberCoordinationRead(&turn, &seq, toolAssocRecall, `{"conversation_id":"cid-a"}`, "", errors.New("database unavailable"))
	if err == nil || len(turn.CoordinationReads) != 1 || !turn.CoordinationReads[0].Failed || turn.CoordinationReads[0].ReadRef != "r3" || !strings.Contains(string(turn.CoordinationReads[0].Result), `"status":"unavailable"`) {
		t.Fatalf("failure retained old success: %+v err=%v", turn.CoordinationReads, err)
	}
	if _, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"job_policy"}`, `{"text":"FULL_SOP"}`, nil); err == nil {
		t.Fatal("full SOP crossed managed context boundary")
	}
}

func TestCoordinationContextBoundsAllSnapshotsAndExcludesHiddenRows(t *testing.T) {
	turn := Turn{ConversationID: "cid-a"}
	seq := 0
	for n := 0; n < 6; n++ {
		items := make([]map[string]any, 10)
		for i := range items {
			items[i] = map[string]any{"issue_id": fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", n*10+i), "purpose": strings.Repeat("目标", 100), "on_this_scene": true, "last_comment": "HIDDEN_BUSINESS_RESULT"}
		}
		payload, _ := json.Marshal(map[string]any{"items": items, "events": []map[string]string{{"text": "HIDDEN_BUSINESS_RESULT"}}})
		raw, err := rememberCoordinationRead(&turn, &seq, toolAssocRecall, fmt.Sprintf(`{"conversation_id":"cid-a","q":"%d"}`, n), string(payload), nil)
		if err != nil {
			t.Fatal(err)
		}
		issues, cont := map[string]struct{}{}, map[string]struct{}{}
		collectRecalledIssues(issues, cont, "cid-a", raw)
		if len(issues) != 3 || len(cont) != 3 {
			t.Fatalf("hidden rows became legal targets: %v", issues)
		}
	}
	raw := coordinationReadsJSON(turn)
	if utf8.RuneCountInString(raw) > coordinationReadsBudget || !turn.CoordinationReadsTruncated || len(turn.CoordinationReads) >= 6 || strings.Contains(raw, "HIDDEN_BUSINESS_RESULT") {
		t.Fatalf("unbounded or unprojected read context: %s", raw)
	}
	if turn.CoordinationReads[len(turn.CoordinationReads)-1].ReadRef != "r6" {
		t.Fatalf("latest snapshot lost: %+v", turn.CoordinationReads)
	}
	for _, read := range turn.CoordinationReads {
		if read.ReadRef == "r1" {
			t.Fatal("evicted ref must no longer be an available state ref")
		}
	}
}

func TestCoordinationMessagesPreserveFullCurrentWindowWithoutRawHistory(t *testing.T) {
	current := strings.Repeat("只起草，不要发送。", 1500) + "当前限制的尾部必须完整"
	turn := Turn{Source: SourceDigitalEmployee, Message: current, HistoryStatus: "loaded", HistoryBefore: time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC), ConversationID: "cid-a", RelatedTasks: "UNBOUNDED_OLD_BACKGROUND"}
	for i := 0; i < 20; i++ {
		turn.DingTalkHistory = append(turn.DingTalkHistory, HistoryLine{Role: "colleague", Content: fmt.Sprintf("message-%02d ", i) + strings.Repeat("历史", 1000), EvidenceID: fmt.Sprintf("evidence-%d", i), Timestamp: turn.HistoryBefore.Add(-time.Duration(20-i) * time.Minute), SenderID: "actual-sender", ReplyToEvidenceID: "actual-parent"})
	}
	seq := 0
	_, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"history"}`, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var history coordinationHistoryView
	if err := json.Unmarshal(turn.CoordinationReads[0].Result, &history); err != nil {
		t.Fatal(err)
	}
	if history.Supplied != 20 || history.Shown >= 20 || !history.Truncated || history.Complete || history.Before == "" || history.Messages[0].EvidenceID != "evidence-19" || history.Messages[0].SenderID != "actual-sender" || history.Messages[0].ReplyToEvidenceID != "actual-parent" {
		t.Fatalf("history provenance or budget lost: %+v", history)
	}
	if utf8.RuneCount(turn.CoordinationReads[0].Result) > coordinationHistoryBudget+30 {
		t.Fatal("history snapshot exceeds budget")
	}
	messages := buildCoordinationMessages(turn, true, coordinationRepairFeedback(toolFinish, errors.New("LATEST_REPAIR")), "")
	raw, _ := json.Marshal(messages)
	if !strings.Contains(string(raw), current) || strings.Count(string(raw), "当前限制的尾部必须完整") != 1 || strings.Contains(string(raw), "UNBOUNDED_OLD_BACKGROUND") || !strings.Contains(string(raw), "LATEST_REPAIR") {
		t.Fatalf("current input clipped/duplicated or old context leaked")
	}
	if len(messages) != 4 || strings.Contains(string(raw), `"role":"assistant"`) || strings.Contains(string(raw), `"role":"tool"`) {
		t.Fatalf("old transcript retained: %d messages", len(messages))
	}
}

func TestCoordinationHistoryWatermarkAndUnparsedTimeRemainExplicit(t *testing.T) {
	before := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	turn := Turn{ConversationID: "cid-a", HistoryStatus: "loaded", HistoryBefore: before, DingTalkHistory: []HistoryLine{
		{Role: "a", Content: "PAST", Timestamp: before.Add(-time.Minute), EvidenceID: "old"},
		{Role: "b", Content: "UNKNOWN_ORDER", TimestampRaw: "昨天晚上", EvidenceID: "unparsed", ContentTruncated: true},
		{Role: "c", Content: "LATER_AUTHORIZATION", Timestamp: before.Add(time.Minute), EvidenceID: "later"},
	}}
	raw, err := coordinationHistoryJSON(turn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "LATER_AUTHORIZATION") || !strings.Contains(raw, "UNKNOWN_ORDER") || !strings.Contains(raw, "昨天晚上") || !strings.Contains(raw, `"truncated":true`) || !strings.Contains(raw, "PAST") {
		t.Fatalf("watermark or unknown time discarded: %s", raw)
	}
}

func TestFinishReadEvidenceUsesOnlyCurrentHostSnapshots(t *testing.T) {
	turn := Turn{Message: "当前请求", History: []HistoryLine{{Content: "RAW_HISTORY_NOT_EVIDENCE"}}, RelatedTasks: "OLD_CARD_NOT_EVIDENCE"}
	seq := 0
	_, err := rememberCoordinationRead(&turn, &seq, toolAssocRecall, `{}`, `{"items":[],"events":[{"text":"UNVERIFIED_RESULT"}]}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	evidence, _ := json.Marshal(finishReadEvidence(turn))
	if strings.Contains(string(evidence), "RAW_HISTORY_NOT_EVIDENCE") || strings.Contains(string(evidence), "UNVERIFIED_RESULT") || strings.Contains(string(evidence), "OLD_CARD_NOT_EVIDENCE") || !strings.Contains(string(evidence), `"read_ref":"r1"`) {
		t.Fatalf("invalid evidence source: %s", evidence)
	}
	feedback := coordinationRepairFeedback(strings.Repeat("tool", 100), errors.New(strings.Repeat("\x01", 10000)))
	if utf8.RuneCountInString(feedback) > coordinationFeedbackBudget {
		t.Fatalf("feedback budget exceeded: %d", utf8.RuneCountInString(feedback))
	}
}

func TestCoordinationHistoryEvictionReopensProjectionWithoutReload(t *testing.T) {
	for _, source := range []Source{SourceDigitalEmployee, SourceWeb} {
		t.Run(string(source), func(t *testing.T) {
			turn := Turn{Source: source, ConversationID: "cid-a", HistoryStatus: "loaded", History: []HistoryLine{{Role: "user", Content: "PREVIOUS_QUESTION", EvidenceID: "old-message"}}}
			seq := 0
			if _, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"history"}`, "", nil); err != nil {
				t.Fatal(err)
			}
			if containsString(toolParamNames(toolsForDisclosure(turn, 1, true)), toolContextRead) {
				t.Fatal("unchanged visible history needs no repeated read")
			}
			for i := 0; i < 6; i++ {
				items := make([]map[string]any, 3)
				for j := range items {
					items[j] = map[string]any{"issue_id": fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", i*10+j), "purpose": strings.Repeat("目标", 100), "on_this_scene": true}
				}
				raw, _ := json.Marshal(map[string]any{"items": items})
				if _, err := rememberCoordinationRead(&turn, &seq, toolAssocRecall, fmt.Sprintf(`{"q":"%d"}`, i), string(raw), nil); err != nil {
					t.Fatal(err)
				}
			}
			if hasCoordinationHistorySnapshot(turn) || !turn.CoordinationReadsTruncated {
				t.Fatal("fixture did not evict old history")
			}
			if !strings.Contains(coordinationUserPrompt(turn), "history_status: not_loaded") || turn.HistoryStatus != "loaded" {
				t.Fatal("visible status must differ from Host-held cached history")
			}
			if !containsString(toolParamNames(toolsForDisclosure(turn, 2, true)), toolContextRead) {
				t.Fatal("evicted history cannot be read again")
			}
			if containsString(toolParamNames(toolsForDisclosure(turn, maxLoopRounds-2, true)), toolContextRead) {
				t.Fatal("late-round tool budget was bypassed")
			}
			// No DWS loader is configured: a cache replay must not access one.
			raw, err := (&Coordinator{}).readHistoryContext(context.Background(), &turn, `{"kind":"history"}`)
			if err != nil || turn.HistoryStatus != "loaded" {
				t.Fatalf("cached history tried to reload: %s %v", raw, err)
			}
			if _, err = rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"history"}`, raw, nil); err != nil {
				t.Fatal(err)
			}
			if !hasCoordinationHistorySnapshot(turn) || !strings.Contains(coordinationReadsJSON(turn), "PREVIOUS_QUESTION") || !strings.Contains(coordinationUserPrompt(turn), "history_status: loaded") {
				t.Fatal("cached history was not restored to visible context")
			}
		})
	}
}
