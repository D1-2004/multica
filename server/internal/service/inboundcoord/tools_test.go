package inboundcoord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestAssocToolsRecallDefaultsConversationID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	if _, err := svc.BindOutbound(ctx, assoc.BindOutboundInput{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		IssueID:        "issue-eat",
		IssueTitle:     "向冬翔确认今天吃什么",
		Purpose:        "向冬翔确认今天吃什么",
		ConversationID: "cid-dongxiang",
		EvidenceID:     "msg-out-1",
		Kind:           "dm",
	}); err != nil {
		t.Fatal(err)
	}
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-dongxiang",
	}, toolAssocRecall, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var result assoc.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Issue != "issue-eat" {
		t.Fatalf("items=%+v", result.Items)
	}
	if result.Items[0].Purpose != "向冬翔确认今天吃什么" {
		t.Fatalf("purpose=%q", result.Items[0].Purpose)
	}
}

func TestAssocToolsBindAssociatesIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	svc := assoc.NewService(store)
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	tools := &AssocTools{Service: svc}
	raw, err := tools.Call(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-new",
		PersonID:       "123456",
		EvidenceID:     "msg-in-1",
		Kind:           "dm",
	}, toolAssocBind, `{"issue_id":"issue-eat","purpose":"向冬翔确认今天吃什么"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"linked":true`) {
		t.Fatalf("bind=%s", raw)
	}
	got, err := svc.Recall(ctx, assoc.Query{
		WorkspaceID:    "ws",
		AgentID:        agentID,
		ConversationID: "cid-new",
		Since:          mustSince48h(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Issue != "issue-eat" {
		t.Fatalf("recall=%+v", got.Items)
	}
}

func TestAssocToolsBindRequiresConversation(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{WorkspaceID: "ws", AgentID: testAgentID()}, toolAssocBind, `{"issue_id":"issue-eat"}`)
	if err == nil || !strings.Contains(err.Error(), "conversation_id") {
		t.Fatalf("err=%v", err)
	}
}

func TestAssocToolsUnknownName(t *testing.T) {
	t.Parallel()
	tools := &AssocTools{Service: assoc.NewService(assoc.NewMemory())}
	_, err := tools.Call(context.Background(), Turn{}, "dws_send", `{}`)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("err=%v", err)
	}
}

func mustSince48h(t *testing.T) time.Time {
	t.Helper()
	parsed, err := assoc.ParseSince("48h", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
