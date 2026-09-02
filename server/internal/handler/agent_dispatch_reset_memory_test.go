package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestIsInboundResetMemory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"/reset-memory", true},
		{"/Reset-Memory", true},
		{"/reset-memory 确认", true},
		{"@菲迪 /reset-memory", true},
		{"reset-memory", false},
		{"/reset", false},
		{"请 /reset-memory", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isInboundResetMemory(tc.in); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestExecuteAgentDispatchV2ResetMemoryClearsScene(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws, err := util.ParseUUID("22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	ag, err := util.ParseUUID("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	issue := "33333333-3333-3333-3333-333333333333"
	ctx := context.Background()
	cmd := DispatchCommand{
		Event: DispatchEvent{
			Domain: "channel",
			Type:   "message.created",
			Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: "cid-a"},
				Sender:       DispatchSender{OpenDingTalkID: "uid-a"},
				Messages:     []DispatchMessage{{OpenMsgID: "msg-in-a", Text: "7点"}},
			},
		},
	}
	dc := agentDispatchContext{WorkspaceID: ws, AgentID: ag}
	h.recordAssocInboundEvent(ctx, cmd, dc)
	h.associateDispatchIssue(ctx, cmd, dc, issue, "向冬翔确认今天吃什么", "44444444-4444-4444-4444-444444444444", "", inboundcoord.Decision{})

	before, rerr := h.Assoc.Recall(ctx, assoc.Query{
		WorkspaceID:    uuidToString(ws),
		AgentID:        uuidToString(ag),
		ConversationID: "cid-a",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(before.Items) != 1 {
		t.Fatalf("before items=%+v", before.Items)
	}

	reset := cmd
	reset.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "msg-reset", Text: "/reset-memory"}}
	reset.Continuation = &AgentDispatchContinuation{Kind: "issue", IssueID: issue}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/dispatch", nil)
	h.executeAgentDispatchV2(rec, req, reset, agentDispatchExecutionPlan{
		MaterializerType: protocol.DispatchSurfaceTypeIssue,
	}, dc)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	after, aerr := h.Assoc.Recall(ctx, assoc.Query{
		WorkspaceID:    uuidToString(ws),
		AgentID:        uuidToString(ag),
		ConversationID: "cid-a",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if aerr != nil {
		t.Fatal(aerr)
	}
	if len(after.Items) != 0 {
		t.Fatalf("after items=%+v", after.Items)
	}
	inbound, ierr := store.GetEventByEvidence(ctx, uuidToString(ws), uuidToString(ag), "msg-in-a")
	if ierr != nil {
		t.Fatal(ierr)
	}
	if inbound.TaskID != "" {
		t.Fatalf("inbound still linked to %q", inbound.TaskID)
	}
}
