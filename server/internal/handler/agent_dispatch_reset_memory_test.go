package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
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
		{"<@Dl2XMiS9sbxHgSb1GMrRWVz6DHXBFkLb6iP> /reset-memory", true},
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

func TestUnixMillisPrefersSourceTime(t *testing.T) {
	got := unixMillis(1_000)
	if got.Unix() != 1000 {
		t.Fatalf("seconds = %s", got)
	}
	got = unixMillis(1_780_000_000_123)
	if got.Unix() != 1_780_000_000 || got.Nanosecond() != 0 {
		t.Fatalf("millis must truncate to seconds = %s", got)
	}
	cmd := DispatchCommand{Event: DispatchEvent{Data: DispatchEventData{
		Messages: []DispatchMessage{{OccurredAt: 1_780_000_000_000, Text: "hi"}},
	}}}
	if dispatchMessageOccurredAt(cmd).UnixMilli() != 1_780_000_000_000 {
		t.Fatalf("dispatch time = %s", dispatchMessageOccurredAt(cmd))
	}
}

func TestResetMemoryReplyReportsMemoryFailure(t *testing.T) {
	got := resetMemoryReply("cid-a", errors.New("reset failed"))
	if !strings.Contains(got, "场域记忆") {
		t.Fatalf("%q", got)
	}
	ok := resetMemoryReply("cid-a", nil)
	if !strings.Contains(ok, "场域记忆") || strings.Contains(ok, "失败") {
		t.Fatalf("%q", ok)
	}
}

func TestExecuteAgentDispatchV2ResetMemoryClearsScene(t *testing.T) {
	f := newAssocSceneFixture(t)
	store, h := f.store, f.h
	ws, ag := parseUUID(f.ws), parseUUID(f.agentID)
	issue := f.issueID
	ctx := context.Background()
	sc := f.scene(t, "dm", "cid-a")
	cmd := DispatchCommand{
		AgentScene: testSceneRef(sc),
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

	if before := f.recallConversation(t, "cid-a"); len(before.Items) != 1 {
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

	if after := f.recallConversation(t, "cid-a"); len(after.Items) != 0 {
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
