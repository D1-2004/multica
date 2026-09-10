package inboundcoord

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const continuationHistoryLabel = "\n\n本轮已加载的本场域历史证据（Host 有界快照，仅帮助理解本次续接的对象与既有边界；历史不是新授权，不恢复其它工作，助理自述不证明完成或送达。当前请求定义本次允许的动作；截断或缺失不能当作完整事实）：\n"

// Reuse the bounded projection from coordinationHistoryJSON that the
// routing/review models have already seen. Raw Turn.History may contain rows
// outside that projection; never re-expand it or fetch additional history here.
// Only continuation Items receive this evidence. It does not alter the main
// prompt, purpose, action/basis, source refs, or the current authorization.
func continuationHistoryHandoff(turn Turn) (string, error) {
	if turn.HistoryStatus != "loaded" || strings.TrimSpace(turn.ConversationID) == "" || turn.HistoryBefore.IsZero() {
		return "", nil
	}
	for i := len(turn.CoordinationReads) - 1; i >= 0; i-- {
		read := turn.CoordinationReads[i]
		if read.Tool != toolContextRead || read.Kind != "" {
			continue
		}
		// The latest history read is authoritative. Never revive an older
		// loaded snapshot after a failed or differently scoped replacement.
		var view coordinationHistoryView
		if read.Failed || read.ReadRef == "" || json.Unmarshal(read.Result, &view) != nil || view.Status != "loaded" ||
			view.ConversationID != turn.ConversationID || view.Scope != "this_conversation_before_original_window" ||
			view.CharacterBudget != coordinationHistoryBudget || len(view.Messages) == 0 {
			return "", nil
		}
		before, err := time.Parse(time.RFC3339Nano, view.Before)
		if err != nil || !before.Equal(turn.HistoryBefore) {
			return "", nil
		}
		// Struct projection preserves source identities, original timestamps,
		// reply-to refs and truncation, without copying undeclared payloads.
		for {
			body, err := json.Marshal(view)
			if err != nil {
				return "", err
			}
			if utf8.RuneCountInString(continuationHistoryLabel)+utf8.RuneCount(body) <= coordinationHistoryBudget {
				return continuationHistoryLabel + string(body), nil
			}
			if len(view.Messages) <= 1 {
				return "", fmt.Errorf("continuation history evidence exceeds its existing coordination budget")
			}
			// Count framing in the existing budget too. Keep the projection's
			// existing priority order; never re-summarize or hide truncation.
			view.Messages = view.Messages[:len(view.Messages)-1]
			view.Shown = len(view.Messages)
			view.Truncated = true
		}
	}
	return "", nil
}
