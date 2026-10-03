package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTaskRunEventsAtomicReplayAndConflict(t *testing.T) {
	if testPool == nil {
		t.Fatal("PostgreSQL required")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "event-batch", nil)
	id := createHandlerTestTaskForAgent(t, agentID)
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.TaskRunEventsEnabled = true
	batch := []TaskMessageRequest{{Seq: 1, Type: "text", Content: "started", Event: &protocol.TaskEventSource{SessionID: "session-1", Phase: "commentary", ObservedAt: time.Now().UTC()}}, {Seq: 2, Type: "tool_use", Tool: "bash", Input: map[string]any{"command": "echo ok"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { _, e := h.persistTaskMessageBatch(ctx, task, testWorkspaceID, batch); errs <- e })
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	rows, err := h.Queries.ListTaskMessages(ctx, task.ID)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	var meta protocol.TaskEventContext
	if json.Unmarshal(rows[0].Event, &meta) != nil || meta.WorkspaceID != testWorkspaceID || meta.AgentID != agentID || meta.Source.SessionID != "session-1" || meta.Version != 1 {
		t.Fatalf("host metadata: %s", rows[0].Event)
	}
	_, err = h.persistTaskMessageBatch(ctx, task, testWorkspaceID, []TaskMessageRequest{{Seq: 2, Type: "tool_use", Tool: "other"}, {Seq: 3, Type: "text", Content: "must rollback"}})
	if !errors.Is(err, errTaskEventConflict) {
		t.Fatal(err)
	}
	// A new prefix must also roll back when a later member conflicts.
	_, err = h.persistTaskMessageBatch(ctx, task, testWorkspaceID, []TaskMessageRequest{{Seq: 1, Type: "text", Content: "changed"}, {Seq: 3, Type: "text", Content: "tail"}})
	if !errors.Is(err, errTaskEventConflict) {
		t.Fatal(err)
	}
	rows, err = h.Queries.ListTaskMessages(ctx, task.ID)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	_, err = h.persistTaskMessageBatch(ctx, task, testWorkspaceID, []TaskMessageRequest{{Seq: 3, Type: "text", Content: "ok"}, {Seq: 4, Type: "text", Input: map[string]any{"unencodable": make(chan int)}}})
	if err == nil {
		t.Fatal("invalid member accepted")
	}
	rows, err = h.Queries.ListTaskMessages(ctx, task.ID)
	if err != nil || len(rows) != 2 {
		t.Fatal("partial batch", rows, err)
	}
}

func TestTaskRunEventsSceneBindingAndPrivacyProjection(t *testing.T) {
	if testPool == nil {
		t.Fatal("PostgreSQL required")
	}
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	h := *f.h
	h.TaskRunEventsEnabled = true
	task, err := h.Queries.GetAgentTask(context.Background(), parseUUID(f.queueID))
	if err != nil {
		t.Fatal(err)
	}
	var sceneID, employeeTaskID string
	if err := testPool.QueryRow(context.Background(), `SELECT t.scene_id::text,t.id::text FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE r.id=$1`, f.runID).Scan(&sceneID, &employeeTaskID); err != nil {
		t.Fatal(err)
	}
	rows, err := h.persistTaskMessageBatch(context.Background(), task, testWorkspaceID, []TaskMessageRequest{{Seq: 1, Type: "thinking", Content: "PRIVATE_REASONING"}, {Seq: 2, Type: "tool_result", Tool: "bash", Output: "PRIVATE_TOOL_RESULT"}, {Seq: 3, Type: "text", Content: "public progress", Event: &protocol.TaskEventSource{Phase: "delta", SessionID: "session-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		e := taskRunEvent(row)
		if e.SceneID != sceneID || e.TaskID != employeeTaskID || e.RunID != f.runID || e.QueueTaskID != f.queueID {
			t.Fatalf("invalid binding: %+v", e)
		}
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), "PRIVATE_") {
			t.Fatal("diagnostic disclosed", string(raw))
		}
	}
	req := newRequest(http.MethodGet, "/api/tasks/"+f.queueID+"/events?limit=2", nil)
	w := httptest.NewRecorder()
	h.listTaskRunEvents(w, req, task, testWorkspaceID)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var p protocol.TaskRunEventPage
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || !p.HasMore || p.NextSeq != 2 || len(p.Events) != 2 {
		t.Fatal(w.Body.String())
	}
	req = newRequest(http.MethodGet, "/api/tasks/"+f.queueID+"/events?since=2&limit=2", nil)
	w = httptest.NewRecorder()
	h.listTaskRunEvents(w, req, task, testWorkspaceID)
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || p.HasMore || p.NextSeq != 3 || len(p.Events) != 1 {
		t.Fatal(w.Body.String())
	}
	req = newRequest(http.MethodGet, "/api/tasks/"+f.queueID+"/events?session_id=session-1", nil)
	w = httptest.NewRecorder()
	h.listTaskRunEvents(w, req, task, testWorkspaceID)
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Events) != 1 || p.NextSeq != 3 {
		t.Fatal("session filter lost", w.Body.String())
	}
	// A valid secondary tenant retains access when the primary identity changes.
	if _, err = testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET org_id='new-primary' WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(context.Background(), `INSERT INTO agent_tenant(workspace_id,agent_id,org_id,name) VALUES($1,$2,'456','secondary')`, testWorkspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_tenant WHERE agent_id=$1`, f.agentID)
	})
	w = httptest.NewRecorder()
	h.listTaskRunEvents(w, req, task, testWorkspaceID)
	if w.Code != http.StatusOK {
		t.Fatal("secondary tenant refused", w.Code, w.Body.String())
	}
	// A current tenant fence must deny an otherwise readable historical task.
	_, err = testPool.Exec(context.Background(), `UPDATE agent_scene SET tenant_org_id='other-tenant' WHERE id=$1`, sceneID)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.listTaskRunEvents(w, req, task, testWorkspaceID)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTaskRunEventStatusIsNotADeliverableReply(t *testing.T) {
	if testPool == nil {
		t.Fatal("PostgreSQL required")
	}
	agent := createHandlerTestAgent(t, "event-status-summary", nil)
	id := createHandlerTestTaskForAgent(t, agent)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.TaskRunEventsEnabled = true
	_, err = h.persistTaskMessageBatch(context.Background(), task, testWorkspaceID, []TaskMessageRequest{{Seq: 1, Type: "status", Content: "running", Event: &protocol.TaskEventSource{Status: "running"}}, {Seq: 2, Type: "log", Content: "diagnostic"}})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := h.Queries.GetTaskMessageSummary(context.Background(), task.ID)
	if err != nil || summary.MessageCount != 0 || summary.FirstEffectiveReplyAt.Valid {
		t.Fatal("diagnostic became deliverable", summary, err)
	}
	cursor, err := h.Queries.GetTaskMessageCursor(context.Background(), task.ID)
	if err != nil || cursor != 2 {
		t.Fatal(cursor, err)
	}
}

func TestTaskRunEventDiagnosticAndUnknownStatusAreNotReportable(t *testing.T) {
	for _, kind := range []string{"thinking", "log", "unknown", "status"} {
		e := taskRunEvent(db.TaskMessage{Type: kind})
		if e.Reportability != "none" || e.Content != "" {
			t.Fatal(e)
		}
	}
}

func TestTaskRunEventsKeepDirectReadBoundaries(t *testing.T) {
	f := employeeDirectAccessFixture(t)
	h := *testHandler
	h.TaskRunEventsEnabled = true
	for _, user := range []string{testUserID, f.originator, f.plain} {
		for _, task := range []string{f.owned, f.external} {
			for _, daemon := range []bool{false, true} {
				r := employeeAccessRequest(t, user, task)
				w := httptest.NewRecorder()
				if daemon {
					h.ListDaemonTaskRunEvents(w, r)
				} else {
					h.ListTaskRunEventsByUser(w, r)
				}
				want := user == testUserID || user == f.originator && task == f.owned
				if (w.Code == http.StatusOK) != want {
					t.Fatalf("user=%s task=%s daemon=%v HTTP %d %s", user, task, daemon, w.Code, w.Body.String())
				}
				if !want && strings.Contains(w.Body.String(), "PRIVATE_") {
					t.Fatal("private progress leaked")
				}
			}
		}
	}
}
