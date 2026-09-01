package assoc

import (
	"context"
	"testing"
	"time"
)

func TestRecentPersonOutreachScene(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemory()
	svc := NewService(store)
	_, err := BindOutbound(ctx, store, BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        "ag",
		IssueID:        "issue-1",
		IssueTitle:     "向冬翔确认今晚想吃什么",
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		PersonID:       "0104644667680872",
		EvidenceID:     "msg-old",
		Kind:           "dm",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.RecentPersonOutreachScene(ctx, "ws", "ag", "0104644667680872")
	if err != nil {
		t.Fatal(err)
	}
	if got != "cid+bEFv7ngm9n79Q1vL9HYJw==" {
		t.Fatalf("got %q", got)
	}
	none, err := svc.RecentPersonOutreachScene(ctx, "ws", "ag", "unknown")
	if err != nil || none != "" {
		t.Fatalf("unknown person cid=%q err=%v", none, err)
	}
	_ = time.Now()
}
