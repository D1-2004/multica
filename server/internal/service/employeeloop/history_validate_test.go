package employeeloop

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRecentConversationMatchesReplayBounds(t *testing.T) {
	turn := func(i int, text string) map[string]any {
		return map[string]any{"role": "user", "text": text, "observed_at": "2026-10-03T01:00:00Z", "message_id": "m"}
	}
	snapshot := func(turns []map[string]any) string {
		raw, _ := json.Marshal(map[string]any{"coverage": "admitted_user_text_and_verified_host_replies", "truncated": false, "messages": turns})
		return string(raw)
	}
	ok := snapshot([]map[string]any{turn(0, "本场候选编号有 K6、X3、Z2。")})
	if err := ValidateRecentConversation(HistoryPresentationConversationTurnsV1, ok); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	if err := ValidateRecentConversation(HistoryPresentationConversationTurnsV1, RecentConversationUnavailable); err != nil {
		t.Fatalf("unavailable marker rejected: %v", err)
	}
	many := []map[string]any{}
	for i := 0; i < 21; i++ {
		many = append(many, turn(i, "x"))
	}
	if err := ValidateRecentConversation(HistoryPresentationConversationTurnsV1, snapshot(many)); err == nil {
		t.Fatal("21 turns must be rejected")
	}
	// Under the raw 16 KiB bound but over it once each turn is labeled and
	// rendered: only the replay check can catch this.
	rendered := []map[string]any{}
	for i := 0; i < 20; i++ {
		rendered = append(rendered, turn(i, strings.Repeat("a", 720)))
	}
	raw := snapshot(rendered)
	if len(raw) > 16<<10 {
		t.Fatalf("fixture must stay under the raw bound: %d", len(raw))
	}
	if err := ValidateRecentConversation(HistoryPresentationConversationTurnsV1, raw); err == nil {
		t.Fatal("a snapshot whose rendered form exceeds 16 KiB must be rejected")
	}
	if err := ValidateRecentConversation("unknown", ok); err == nil {
		t.Fatal("unknown presentation must be rejected")
	}
}
