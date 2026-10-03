package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// employeeSecondRequest admits another message from the same requester in the
// fixture's scene and returns its source_ref.
func employeeSecondRequest(t *testing.T, f employeeNoticeFixture, messageID, text string) string {
	t.Helper()
	ctx := context.Background()
	var ep db.AgentDispatchEndpoint
	var err error
	if ep, err = f.h.Queries.GetAgentDispatchEndpoint(ctx, parseUUID(f.agentID)); err != nil {
		t.Fatal(err)
	}
	dc := agentDispatchContext{EndpointID: ep.EndpointID, EndpointNamespaceID: ep.ID, UserID: parseUUID(testUserID), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}
	message := f.command.Event.Data.Messages[0]
	message.OpenMsgID, message.Text = messageID, text
	f.command.Event.Data.Messages = []DispatchMessage{message}
	if w := employeeHTTP(t, f.dingTalkResponseFixture, dc, uuid.NewString()); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	var items []employeeentry.Item
	if err = testPool.QueryRow(ctx, `SELECT items FROM employee_scene_job WHERE agent_id=$1::uuid AND id<>$2::uuid ORDER BY created_at DESC LIMIT 1`, f.agentID, f.jobID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	return items[0].ReceiptID + "/" + messageID
}

// "基于刚才的统计写复盘": the new work packet carries the finished upstream
// report, the link is durable, and no extra model call is made for it.
func TestEmployeeDispatchBuildsOnInjectsUpstreamResult(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM employee_task_link WHERE agent_id=$1::uuid`, f.agentID) })
	var upstreamID, upstreamResult string
	if err := testPool.QueryRow(ctx, `SELECT task_id::text,result FROM employee_task_run WHERE id=$1::uuid AND state='succeeded'`, f.runID).Scan(&upstreamID, &upstreamResult); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upstreamResult, "真实已存结果") {
		t.Fatalf("fixture upstream result: %q", upstreamResult)
	}

	// A ref that is not a candidate of this wake is refused; nothing is created.
	f.model.sourceRef, f.model.buildsOn = employeeSecondRequest(t, f, "message-2", "基于刚才的统计写复盘"), []string{"t9"}
	callsBefore := f.model.calls
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var tasks, links int
	if err := testPool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE agent_id=$1::uuid),(SELECT count(*) FROM employee_task_link WHERE agent_id=$1::uuid)`, f.agentID).Scan(&tasks, &links); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || links != 0 {
		t.Fatalf("an unknown builds_on ref created work: tasks=%d links=%d", tasks, links)
	}
	if f.model.calls-callsBefore > 3 {
		t.Fatalf("model calls for one wake: %d", f.model.calls-callsBefore)
	}

	f.model.sourceRef, f.model.buildsOn = employeeSecondRequest(t, f, "message-3", "基于刚才的统计写复盘"), []string{"t1"}
	callsBefore = f.model.calls
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if f.model.calls-callsBefore != 1 {
		t.Fatalf("builds_on cost extra model calls: %d", f.model.calls-callsBefore)
	}
	var downstreamID, relation string
	var entrySeq int64
	if err := testPool.QueryRow(ctx, `SELECT task_id::text,relation,entry_seq FROM employee_task_link WHERE agent_id=$1::uuid AND related_task_id=$2::uuid`, f.agentID, upstreamID).Scan(&downstreamID, &relation, &entrySeq); err != nil {
		t.Fatal(err)
	}
	if relation != "builds_on" || entrySeq != 1 || downstreamID == upstreamID {
		t.Fatalf("link: %s %d %s", relation, entrySeq, downstreamID)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT q.context FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.task_id=$1::uuid`, downstreamID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var queueContext struct {
		Prompt      string   `json:"direct_task_prompt"`
		ContextUsed []string `json:"employee_context_used"`
	}
	if err := json.Unmarshal(raw, &queueContext); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(queueContext.Prompt, "UPSTREAM RESULTS") || !strings.Contains(queueContext.Prompt, "真实已存结果") {
		t.Fatalf("work packet lacks the upstream report:\n%s", queueContext.Prompt)
	}
	found := false
	for _, ref := range queueContext.ContextUsed {
		found = found || ref == "upstream:"+upstreamID
	}
	if !found {
		t.Fatalf("ContextUsed lacks the upstream: %v", queueContext.ContextUsed)
	}
}
