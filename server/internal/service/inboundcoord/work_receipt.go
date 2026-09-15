package inboundcoord

import (
	"fmt"
	"strings"
	"unicode"
)

func workCoordinationKind(kind string) bool {
	return kind == "start_work" || kind == "continue_work"
}

// Work receipt language is a closed presentation choice, not free-form text.
// The source script provides a small fallback for older saved plans; no work
// purpose, artifact, model reply or hidden instruction is copied into receipts.
func workReceiptLanguage(turn Turn, action CoordinationAction) string {
	if hint := strings.ToLower(strings.TrimSpace(action.ReceiptLanguage)); oneOf(hint, "zh", "en", "ja", "ko") {
		return hint
	}
	var source strings.Builder
	for i, u := range windowUtterances(turn) {
		for _, ref := range action.SourceRefs {
			if ref == fmt.Sprintf("u%d", i+1) {
				source.WriteString(u.Text)
				source.WriteByte('\n')
			}
		}
	}
	if source.Len() == 0 {
		source.WriteString(stripInboundDisplay(turn.Message))
	}
	text := source.String()
	hasHan := false
	for _, r := range text {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana) {
			return "ja"
		}
		if unicode.Is(unicode.Hangul, r) {
			return "ko"
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if hasHan || (strings.TrimSpace(text) == "" && turn.Source == SourceDigitalEmployee) {
		return "zh"
	}
	return "en"
}

func hostWorkReceipt(kind, language string) string {
	continuation := kind == "continue_work"
	switch language {
	case "zh":
		if continuation {
			return "收到，我按这次要求继续处理。"
		}
		return "收到，我来处理。"
	case "ja":
		if continuation {
			return "承知しました。今回の内容に沿って引き続き対応します。"
		}
		return "承知しました。対応します。"
	case "ko":
		if continuation {
			return "확인했습니다. 이번 요청에 맞춰 이어서 처리하겠습니다."
		}
		return "확인했습니다. 처리하겠습니다."
	default:
		if continuation {
			return "Got it. I'll continue with these instructions."
		}
		return "Got it. I'll handle this."
	}
}

// ComposeDecisionReplies preserves non-work replies and their original order.
// Repeated Host work receipts are emitted only at their first occurrence.
func ComposeDecisionReplies(actions []CoordinationAction) string {
	seenWork := map[string]bool{}
	var replies []string
	for _, action := range actions {
		if action.Kind == "ignore" || strings.TrimSpace(action.Reply) == "" {
			continue
		}
		if workCoordinationKind(action.Kind) {
			if seenWork[action.Reply] {
				continue
			}
			seenWork[action.Reply] = true
		}
		replies = append(replies, action.Reply)
	}
	return strings.Join(replies, "\n\n")
}

// NormalizeWorkReceipts is presentation-only. It never changes work content,
// work/action identity, committed effects, pending keys or the send boundary.
// Callers still deliver the aggregate only after all work is durably committed.
func NormalizeWorkReceipts(turn Turn, decision *Decision) {
	if decision == nil || turn.Loop == LoopTaskFinished {
		return
	}
	hasWork := decision.Action == ActionIssue || len(decision.Items) > 0
	for _, action := range decision.CoordinationActions {
		hasWork = hasWork || workCoordinationKind(action.Kind)
	}
	if !hasWork {
		return
	}
	decision.CoordinationActions = append([]CoordinationAction(nil), decision.CoordinationActions...)
	decision.Items = append([]WindowItem(nil), decision.Items...)
	itemIndex := 0
	for i := range decision.CoordinationActions {
		action := &decision.CoordinationActions[i]
		if !workCoordinationKind(action.Kind) {
			continue
		}
		action.ReceiptLanguage = workReceiptLanguage(turn, *action)
		action.Reply = hostWorkReceipt(action.Kind, action.ReceiptLanguage)
		if itemIndex < len(decision.Items) {
			decision.Items[itemIndex].Reply = action.Reply
		}
		itemIndex++
	}
	// Older window-plan-v1 checkpoints may have items but no structured
	// actions. Their opaque UserText cannot safely separate ACK from output.
	var legacyReceipts []CoordinationAction
	for i := itemIndex; i < len(decision.Items); i++ {
		item := &decision.Items[i]
		kind := "start_work"
		if item.IssueID != "" || oneOf(item.Basis, "answer", "change", "retry") {
			kind = "continue_work"
		}
		action := CoordinationAction{Kind: kind, SourceRefs: item.SourceRefs}
		action.Reply = hostWorkReceipt(kind, workReceiptLanguage(turn, action))
		item.Reply = action.Reply
		legacyReceipts = append(legacyReceipts, action)
	}
	if itemIndex == 0 && len(legacyReceipts) == 0 {
		kind := "start_work"
		if decision.IssueID != "" {
			kind = "continue_work"
		}
		legacyReceipts = append(legacyReceipts, CoordinationAction{Kind: kind, Reply: hostWorkReceipt(kind, workReceiptLanguage(turn, CoordinationAction{}))})
	}
	if len(legacyReceipts) > 0 {
		combined := append(legacyReceipts, decision.CoordinationActions...)
		decision.UserText = ComposeDecisionReplies(combined)
	} else {
		decision.UserText = ComposeDecisionReplies(decision.CoordinationActions)
	}
}
