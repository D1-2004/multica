package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRecallAssocRequiresSince(t *testing.T) {
	h := &Handler{Assoc: assoc.NewService(assoc.NewMemory())}
	req := httptest.NewRequest(http.MethodGet, "/api/assoc/recall?conversation_id=cid-a", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", "11111111-1111-1111-1111-111111111111")
	req = req.WithContext(middleware.SetMemberContext(context.Background(), "22222222-2222-2222-2222-222222222222", db.Member{}))
	rec := httptest.NewRecorder()
	h.RecallAssoc(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecallAssocByConversation(t *testing.T) {
	store := assoc.NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()
	ws := "22222222-2222-2222-2222-222222222222"
	ag := "11111111-1111-1111-1111-111111111111"
	issue := "33333333-3333-3333-3333-333333333333"
	task, err := store.InsertTask(ctx, assoc.Task{
		WorkspaceID:   ws,
		AgentID:       ag,
		IssueID:       issue,
		Purpose:       "预约A与B本周五下午30分钟",
		LastTouchedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, assoc.Edge{
		WorkspaceID: ws,
		AgentID:     ag,
		SrcType:     assoc.NodeTask,
		SrcID:       task.ID,
		DstType:     assoc.NodeScene,
		DstID:       "cid-a",
		Rel:         assoc.RelOutreach,
	}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{Assoc: assoc.NewService(store)}
	req := httptest.NewRequest(http.MethodGet, "/api/assoc/recall?conversation_id=cid-a&since=48h", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", ag)
	req = req.WithContext(middleware.SetMemberContext(ctx, ws, db.Member{}))
	rec := httptest.NewRecorder()
	h.RecallAssoc(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out assoc.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].TaskID != task.ID {
		t.Fatalf("items = %+v", out.Items)
	}
}

func TestBindAssocOutboundFromToolLinksReceipt(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := parseUUID("11111111-1111-1111-1111-111111111111")
	issue := parseUUID("33333333-3333-3333-3333-333333333333")
	taskID := parseUUID("44444444-4444-4444-4444-444444444444")
	task := db.AgentTaskQueue{
		ID:      taskID,
		AgentID: ag,
		IssueID: issue,
	}
	h.bindAssocOutboundFromTool(context.Background(), task, ws, TaskMessageRequest{
		Type:    "tool",
		Tool:    "Bash",
		Content: "dws chat message send --conversation-id cid-flag --content 今晚吃什么",
		Output:  `{"openConversationId":"cid+bEFv7ngm9n79Q1vL9HYJw==","openMsgId":"msg-live"}`,
	})
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID:    ws,
		AgentID:        uuidToString(ag),
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Issue != uuidToString(issue) {
		t.Fatalf("recall=%+v", got.Items)
	}
	ev, err := store.GetEventByEvidence(context.Background(), ws, uuidToString(ag), "msg-live")
	if err != nil {
		t.Fatal(err)
	}
	if ev.SceneKey != "cid+bEFv7ngm9n79Q1vL9HYJw==" {
		t.Fatalf("event=%+v", ev)
	}
}

func TestBindAssocOutboundFromToolIgnoresCommandTextAsCID(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := parseUUID("11111111-1111-1111-1111-111111111111")
	issue := parseUUID("33333333-3333-3333-3333-333333333333")
	task := db.AgentTaskQueue{
		ID:      parseUUID("44444444-4444-4444-4444-444444444444"),
		AgentID: ag,
		IssueID: issue,
	}
	h.bindAssocOutboundFromTool(context.Background(), task, ws, TaskMessageRequest{
		Type:    "tool",
		Tool:    "Bash",
		Content: "dws chat message send --user 103262 --content 今晚吃什么",
		Input:   map[string]any{"command": "dws chat message send --user 103262 --content 今晚吃什么"},
		Output:  "ok",
	})
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID: ws,
		AgentID:     uuidToString(ag),
		IssueID:     uuidToString(issue),
		Since:       time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("command text must not bind: %+v", got.Items)
	}
}

func TestBindAssocOutboundFromUserSendAndQuerySendStatus(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := parseUUID("11111111-1111-1111-1111-111111111111")
	issue := parseUUID("33333333-3333-3333-3333-333333333333")
	task := db.AgentTaskQueue{
		ID:      parseUUID("44444444-4444-4444-4444-444444444444"),
		AgentID: ag,
		IssueID: issue,
	}
	h.bindAssocOutboundFromTools(context.Background(), task, ws, []TaskMessageRequest{
		{
			Type:    "tool",
			Tool:    "Bash",
			Content: `dws chat message send --user 0104644667680872 --content "冬翔，今天下午想喝茶还是咖啡？" --format json --yes`,
			Output:  `{"result":{"openTaskId":"task-1"},"success":true}`,
		},
		{
			Type:    "tool",
			Tool:    "Bash",
			Content: `dws chat message query-send-status --open-task-id task-1 --format json`,
			Output:  `{"openConversationId":"cid+bEFv7ngm9n79Q1vL9HYJw==","openMessageId":"msg-live"}`,
		},
	})
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID:    ws,
		AgentID:        uuidToString(ag),
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Issue != uuidToString(issue) {
		t.Fatalf("recall=%+v", got.Items)
	}
}

func TestBindAssocOutboundFromUserSendReusesPersonScene(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := parseUUID("11111111-1111-1111-1111-111111111111")
	_, err := h.Assoc.BindOutbound(context.Background(), assoc.BindOutboundInput{
		WorkspaceID:    ws,
		AgentID:        uuidToString(ag),
		IssueID:        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		IssueTitle:     "向冬翔确认今晚想吃什么",
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		PersonID:       "0104644667680872",
		EvidenceID:     "msg-old",
		Kind:           "dm",
	})
	if err != nil {
		t.Fatal(err)
	}
	issue := parseUUID("33333333-3333-3333-3333-333333333333")
	task := db.AgentTaskQueue{
		ID:      parseUUID("44444444-4444-4444-4444-444444444444"),
		AgentID: ag,
		IssueID: issue,
	}
	h.bindAssocOutboundFromTool(context.Background(), task, ws, TaskMessageRequest{
		Type:    "tool",
		Tool:    "Bash",
		Content: `dws chat message send --user 0104644667680872 --content hi --format json --yes`,
		Output:  `{"result":{"openTaskId":"task-only"},"success":true}`,
	})
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID:    ws,
		AgentID:        uuidToString(ag),
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range got.Items {
		if item.Issue == uuidToString(issue) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected new issue bound via person scene, recall=%+v", got.Items)
	}
}

func TestBindAssocOutboundFromReply(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := parseUUID("11111111-1111-1111-1111-111111111111")
	issue := parseUUID("33333333-3333-3333-3333-333333333333")
	task := db.AgentTaskQueue{
		ID:      parseUUID("44444444-4444-4444-4444-444444444444"),
		AgentID: ag,
		IssueID: issue,
	}
	h.bindAssocOutboundFromTool(context.Background(), task, ws, TaskMessageRequest{
		Type:    "tool",
		Tool:    "Bash",
		Content: `dws chat message reply --conversation-id cid+reply== --content 收到`,
		Output:  `{"openConversationId":"cid+reply==","openMsgId":"msg-reply"}`,
	})
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID:    ws,
		AgentID:        uuidToString(ag),
		ConversationID: "cid+reply==",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Issue != uuidToString(issue) {
		t.Fatalf("recall=%+v", got.Items)
	}
}

func TestBindAssocOutboundFromToolIgnoresList(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := parseUUID("11111111-1111-1111-1111-111111111111")
	issue := parseUUID("33333333-3333-3333-3333-333333333333")
	task := db.AgentTaskQueue{
		ID:      parseUUID("44444444-4444-4444-4444-444444444444"),
		AgentID: ag,
		IssueID: issue,
	}
	h.bindAssocOutboundFromTool(context.Background(), task, ws, TaskMessageRequest{
		Type:    "tool",
		Tool:    "Bash",
		Content: "dws chat message list --conversation-id cid+bEFv7ngm9n79Q1vL9HYJw==",
		Output:  `{"openConversationId":"cid+bEFv7ngm9n79Q1vL9HYJw==","openMsgId":"msg-list"}`,
	})
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID:    ws,
		AgentID:        uuidToString(ag),
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Fatalf("list must not bind: %+v", got.Items)
	}
}

func TestBindAssocOutboundRequiresTaskToken(t *testing.T) {
	h := &Handler{Assoc: assoc.NewService(assoc.NewMemory())}
	req := httptest.NewRequest(http.MethodPost, "/api/assoc/bind-outbound", strings.NewReader(`{"conversation_id":"cid-a"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.BindAssocOutbound(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBindAssocOutboundWithoutIssueRecordsUnlinked(t *testing.T) {
	h := &Handler{Assoc: assoc.NewService(assoc.NewMemory())}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := "11111111-1111-1111-1111-111111111111"
	req := httptest.NewRequest(http.MethodPost, "/api/assoc/bind-outbound", strings.NewReader(`{"conversation_id":"cid-a","evidence_id":"msg-1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", ag)
	req.Header.Set("X-Task-ID", "44444444-4444-4444-4444-444444444444")
	req.Header.Set("X-Workspace-ID", ws)
	req = req.WithContext(middleware.SetMemberContext(context.Background(), ws, db.Member{}))
	rec := httptest.NewRecorder()
	h.BindAssocOutbound(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out assoc.BindOutboundResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Linked || out.ConversationID != "cid-a" {
		t.Fatalf("got %+v", out)
	}
}

func TestBindAssocOutboundMemberWithIssue(t *testing.T) {
	store := assoc.NewMemory()
	h := &Handler{Assoc: assoc.NewService(store)}
	ws := "22222222-2222-2222-2222-222222222222"
	ag := "11111111-1111-1111-1111-111111111111"
	issue := "33333333-3333-3333-3333-333333333333"
	body := `{"conversation_id":"cid+bEFv7ngm9n79Q1vL9HYJw==","evidence_id":"msg-live","issue_id":"` + issue + `","agent_id":"` + ag + `","purpose":"向冬翔确认今天下午喝茶还是咖啡","person_id":"0104644667680872"}`
	req := httptest.NewRequest(http.MethodPost, "/api/assoc/bind-outbound", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(middleware.SetMemberContext(context.Background(), ws, db.Member{}))
	rec := httptest.NewRecorder()
	h.BindAssocOutbound(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := h.Assoc.Recall(context.Background(), assoc.Query{
		WorkspaceID:    ws,
		AgentID:        ag,
		ConversationID: "cid+bEFv7ngm9n79Q1vL9HYJw==",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Issue != issue {
		t.Fatalf("recall=%+v", got.Items)
	}
}

func TestRecordAssocInboundThenAssociate(t *testing.T) {
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
	cmd := DispatchCommand{
		Event: DispatchEvent{
			Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: "cid-a"},
				Sender:       DispatchSender{OpenDingTalkID: "uid-a"},
				Messages:     []DispatchMessage{{OpenMsgID: "msg-in-a"}},
			},
		},
	}
	dc := agentDispatchContext{WorkspaceID: ws, AgentID: ag}
	ctx := context.Background()
	h.recordAssocInboundEvent(ctx, cmd, dc)
	inbound, ierr := store.GetEventByEvidence(ctx, uuidToString(ws), uuidToString(ag), "msg-in-a")
	if ierr != nil {
		t.Fatal(ierr)
	}
	if inbound.TaskID != "" {
		t.Fatalf("ACK must not write task_id: %q", inbound.TaskID)
	}
	h.associateDispatchIssue(ctx, cmd, dc, issue, "预约A与B本周五下午30分钟", "44444444-4444-4444-4444-444444444444", "")

	result, rerr := h.Assoc.Recall(ctx, assoc.Query{
		WorkspaceID:    uuidToString(ws),
		AgentID:        uuidToString(ag),
		ConversationID: "cid-a",
		Since:          time.Now().UTC().Add(-time.Hour),
	})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items=%+v", result.Items)
	}
	if result.Items[0].Issue != issue {
		t.Fatalf("issue=%q", result.Items[0].Issue)
	}
	if result.Items[0].Origin == nil || result.Items[0].Origin.ConversationID != "cid-a" {
		t.Fatalf("origin=%+v", result.Items[0].Origin)
	}
	foundPerson := false
	for _, p := range result.Items[0].People {
		if p.PersonID == "uid-a" {
			foundPerson = true
		}
	}
	if !foundPerson {
		t.Fatalf("people=%+v", result.Items[0].People)
	}
	linked, lerr := store.GetEventByEvidence(ctx, uuidToString(ws), uuidToString(ag), "msg-in-a")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if linked.TaskID == "" {
		t.Fatal("associate should fill task_id")
	}
	edges, eerr := store.ListEdgesBySrc(ctx, uuidToString(ws), uuidToString(ag), assoc.NodeEvent, linked.ID)
	if eerr != nil {
		t.Fatal(eerr)
	}
	if len(edges) != 1 || edges[0].Rel != assoc.RelEventOf {
		t.Fatalf("event_of=%+v", edges)
	}
}

func TestListAssocEventsRequiresSinceAndConversation(t *testing.T) {
	h := &Handler{Assoc: assoc.NewService(assoc.NewMemory())}
	req := httptest.NewRequest(http.MethodGet, "/api/assoc/events?conversation_id=cid-a", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", "11111111-1111-1111-1111-111111111111")
	req = req.WithContext(middleware.SetMemberContext(context.Background(), "22222222-2222-2222-2222-222222222222", db.Member{}))
	rec := httptest.NewRecorder()
	h.ListAssocEvents(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListAssocEventsReturnsSceneTaggedRows(t *testing.T) {
	store := assoc.NewMemory()
	ws := "22222222-2222-2222-2222-222222222222"
	ag := "11111111-1111-1111-1111-111111111111"
	if _, err := store.InsertEvent(context.Background(), assoc.Event{
		WorkspaceID: ws,
		AgentID:     ag,
		Source:      "outbound_im",
		Direction:   assoc.DirOutbound,
		EvidenceID:  "msg-out-1",
		SceneKey:    "cid-a",
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Assoc: assoc.NewService(store)}
	req := httptest.NewRequest(http.MethodGet, "/api/assoc/events?conversation_id=cid-a&since=48h", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", ag)
	req = req.WithContext(middleware.SetMemberContext(context.Background(), ws, db.Member{}))
	rec := httptest.NewRecorder()
	h.ListAssocEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []assocEventResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].ConversationID != "cid-a" {
		t.Fatalf("items=%+v", out.Items)
	}
}

func TestDispatchAssocIDsPrefersDecimalUID(t *testing.T) {
	ids := dispatchAssocIDs(DispatchCommand{
		Event: DispatchEvent{
			Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: "cid-a", Type: "group"},
				Sender:       DispatchSender{OpenDingTalkID: "$:open-a", StaffID: "123456"},
				Messages:     []DispatchMessage{{OpenMsgID: "msg-1"}},
			},
		},
	})
	if ids.PersonID != "123456" {
		t.Fatalf("person=%q", ids.PersonID)
	}
	if ids.Kind != "group" {
		t.Fatalf("kind=%q", ids.Kind)
	}
}
