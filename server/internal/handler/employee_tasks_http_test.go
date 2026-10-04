package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

func employeeTaskTestMember(t *testing.T, role string) string {
	t.Helper()
	ctx := context.Background()
	email := "employee-task-" + uuid.NewString() + "@multica.test"
	var userID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user"(name,email) VALUES('EmployeeTask reader',$1) RETURNING id::text`, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id=$1::uuid`, userID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id=$1::uuid`, userID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,$3)`, testWorkspaceID, userID, role); err != nil {
		t.Fatal(err)
	}
	return userID
}

type employeeTaskCall struct {
	user    string
	id      string
	query   url.Values
	headers map[string]string
}

func employeeTaskRequest(t *testing.T, h *Handler, fn func(*Handler, http.ResponseWriter, *http.Request), c employeeTaskCall, out any) int {
	t.Helper()
	req := newRequestAs(c.user, http.MethodGet, "/api/employee-tasks?"+c.query.Encode(), nil)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if c.id != "" {
		req = withURLParam(req, "id", c.id)
	}
	w := httptest.NewRecorder()
	fn(h, w, req)
	if out != nil && w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w.Code
}

func TestEmployeeTasksHTTPReadAccessAndProjection(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_link", "employee_task_wait"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1::uuid`, f.agentID)
		}
	})
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `SELECT task_id::text FROM employee_task_run WHERE id=$1::uuid`, f.runID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	originator, stranger := employeeTaskTestMember(t, "member"), employeeTaskTestMember(t, "member")
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET originator_user_id=$2::uuid,accountable_user_id=$2::uuid WHERE id=$1::uuid`, f.queueID, originator); err != nil {
		t.Fatal(err)
	}
	get := (*Handler).GetEmployeeTask
	var detail EmployeeTaskDetail
	if code := employeeTaskRequest(t, f.h, get, employeeTaskCall{user: testUserID, id: taskID}, &detail); code != http.StatusOK {
		t.Fatalf("owner detail: %d", code)
	}
	run := detail.LatestRun
	if detail.Task.TaskID != taskID || run == nil || run.RunID != f.runID || run.QueueTaskID != f.queueID || run.RunID == run.QueueTaskID || detail.Task.LifecycleVersion != 1 || detail.Task.CompletionMode != "single_run" || detail.Task.State != "succeeded" || detail.Task.InputSeq < 2 {
		t.Fatalf("detail identity/projection: %+v %+v", detail.Task, run)
	}
	if detail.Execution == nil || detail.Execution.QueueTaskID != f.queueID || run.Delivery == nil || run.Delivery.NoticeState != "enqueued" || run.QueueState != "completed" || !strings.Contains(run.ResultReport, "真实已存结果") {
		t.Fatalf("execution/delivery: %+v %+v", detail.Execution, run)
	}
	if len(detail.EvidenceRefs) != 1 || detail.EvidenceRefs[0] != "agent_task_queue:"+f.queueID || detail.Waits == nil || detail.Collections == nil {
		t.Fatalf("evidence/waits: %+v", detail)
	}
	if code := employeeTaskRequest(t, f.h, get, employeeTaskCall{user: originator, id: taskID}, nil); code != http.StatusOK {
		t.Fatalf("originator detail: %d", code)
	}
	for name, c := range map[string]employeeTaskCall{
		"stranger":       {user: stranger, id: taskID},
		"unknown task":   {user: testUserID, id: uuid.NewString()},
		"other queue":    {user: testUserID, id: taskID, headers: map[string]string{"X-Actor-Source": "task_token", "X-Task-ID": uuid.NewString(), "X-Agent-ID": f.agentID, "X-Workspace-ID": testWorkspaceID}},
		"other agent":    {user: testUserID, id: taskID, headers: map[string]string{"X-Actor-Source": "task_token", "X-Task-ID": f.queueID, "X-Agent-ID": uuid.NewString(), "X-Workspace-ID": testWorkspaceID}},
		"queue as task":  {user: testUserID, id: f.queueID},
		"run as task":    {user: testUserID, id: f.runID},
		"not a member":   {user: uuid.NewString(), id: taskID},
		"task_ref style": {user: testUserID, id: "t1"},
	} {
		code := employeeTaskRequest(t, f.h, get, c, nil)
		want := http.StatusNotFound
		switch name {
		case "task_ref style":
			want = http.StatusBadRequest
		case "not a member":
			want = http.StatusNotFound
		}
		if code != want && !(name == "not a member" && code == http.StatusForbidden) {
			t.Fatalf("%s: %d want %d", name, code, want)
		}
	}
	// The exact execution credential of the Task's Run reads its own Task.
	token := map[string]string{"X-Actor-Source": "task_token", "X-Task-ID": f.queueID, "X-Agent-ID": f.agentID, "X-Workspace-ID": testWorkspaceID}
	if code := employeeTaskRequest(t, f.h, get, employeeTaskCall{user: testUserID, id: taskID, headers: token}, nil); code != http.StatusOK {
		t.Fatalf("execution credential: %d", code)
	}

	// Ledger paging is bounded and redacts credentials in bodies.
	store := employeetask.NewStore(testPool)
	task, err := store.Get(ctx, detail.Task.scopeForTest(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
	if _, _, err = store.AppendInput(ctx, task.Scope, task.ID, employeetask.InputParams{Source: employeetask.Source{Namespace: "test", Key: "secret"}, ActorRef: "human:x", Body: "use key " + secret, ExpectedVersion: task.Version}); err != nil {
		t.Fatal(err)
	}
	entries := (*Handler).ListEmployeeTaskEntries
	var page struct {
		InputSeq     int64                   `json:"input_seq"`
		Entries      []EmployeeTaskEntryView `json:"entries"`
		NextAfterSeq *int64                  `json:"next_after_seq"`
	}
	seen := map[int64]bool{}
	after := "0"
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("entry paging did not terminate")
		}
		page.NextAfterSeq = nil
		if code := employeeTaskRequest(t, f.h, entries, employeeTaskCall{user: originator, id: taskID, query: url.Values{"after_seq": {after}, "limit": {"2"}}}, &page); code != http.StatusOK {
			t.Fatalf("entries: %d", code)
		}
		for _, e := range page.Entries {
			if seen[e.Seq] {
				t.Fatalf("entry %d repeated", e.Seq)
			}
			seen[e.Seq] = true
			if strings.Contains(e.Body, secret) {
				t.Fatal("ledger body leaked a credential")
			}
		}
		if page.NextAfterSeq == nil {
			break
		}
		after = strconv.FormatInt(*page.NextAfterSeq, 10)
	}
	if int64(len(seen)) != page.InputSeq {
		t.Fatalf("paged %d entries, ledger has %d", len(seen), page.InputSeq)
	}
	for name, q := range map[string]url.Values{"negative": {"after_seq": {"-1"}}, "limit": {"limit": {"101"}}, "word": {"after_seq": {"x"}}} {
		if code := employeeTaskRequest(t, f.h, entries, employeeTaskCall{user: originator, id: taskID, query: q}, nil); code != http.StatusBadRequest {
			t.Fatalf("entries %s: %d", name, code)
		}
	}
	if code := employeeTaskRequest(t, f.h, entries, employeeTaskCall{user: stranger, id: taskID}, nil); code != http.StatusNotFound {
		t.Fatalf("stranger entries: %d", code)
	}

	runs := (*Handler).ListEmployeeTaskRuns
	var runPage struct {
		Runs []EmployeeTaskRunView `json:"runs"`
	}
	if code := employeeTaskRequest(t, f.h, runs, employeeTaskCall{user: originator, id: taskID}, &runPage); code != http.StatusOK || len(runPage.Runs) != 1 || runPage.Runs[0].QueueTaskID != f.queueID || runPage.Runs[0].Delivery == nil {
		t.Fatalf("runs: %d %+v", code, runPage)
	}
	if code := employeeTaskRequest(t, f.h, runs, employeeTaskCall{user: originator, id: taskID, query: url.Values{"after_run_id": {uuid.NewString()}}}, nil); code != http.StatusNotFound {
		t.Fatalf("foreign after_run_id: %d", code)
	}
	if code := employeeTaskRequest(t, f.h, runs, employeeTaskCall{user: originator, id: taskID, query: url.Values{"after_run_id": {"r1"}}}, nil); code != http.StatusBadRequest {
		t.Fatalf("invalid after_run_id: %d", code)
	}

	// A v2 goal shows its waits, collections, links and lifecycle.
	params := employeetask.CreateParams{Scope: task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: task.RequesterRef, Definition: employeetask.Definition{Goal: "Collect three numbers"}, Source: employeetask.Source{Namespace: "test", Key: "goal"}, Input: "collect", Lifecycle: employeetask.LifecycleV2, CompletionMode: employeetask.CompletionExplicitGoal}
	goal, err := store.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	params.Source.Key, params.Definition.Goal = "upstream", "Prepare the list"
	upstream, err := store.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	authority := "employee_task_entry:" + goal.ID + "/1"
	if _, _, err = store.WaitTask(ctx, goal.Scope, goal.ID, employeetask.WaitParams{Source: employeetask.Source{Namespace: "collection", Key: "c1/open"}, Kind: employeetask.WaitCollection, RefID: "c1", Mandatory: true, AuthorityRef: authority}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.BlockOnTask(ctx, goal.Scope, goal.ID, employeetask.BlockParams{Source: employeetask.Source{Namespace: "dep", Key: "on-upstream"}, UpstreamTaskID: upstream.ID, AuthorityRef: authority}); err != nil {
		t.Fatal(err)
	}
	builder := employeetask.CreateParams{Scope: task.Scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: task.RequesterRef, Definition: employeetask.Definition{Goal: "Retrospective"}, Source: employeetask.Source{Namespace: "test", Key: "builds"}, Input: "retro", BuildsOn: []string{taskID}}
	follower, err := store.Create(ctx, builder)
	if err != nil {
		t.Fatal(err)
	}
	var goalDetail EmployeeTaskDetail
	if code := employeeTaskRequest(t, f.h, get, employeeTaskCall{user: testUserID, id: goal.ID}, &goalDetail); code != http.StatusOK {
		t.Fatalf("goal detail: %d", code)
	}
	if goalDetail.Task.LifecycleVersion != 2 || goalDetail.Task.State != "waiting" || len(goalDetail.Waits) != 2 || goalDetail.WaitingCounts.OpenMandatory != 2 || len(goalDetail.Links.BlockedBy) != 1 || goalDetail.Links.BlockedBy[0].RelatedTaskID != upstream.ID || goalDetail.LatestRun != nil {
		t.Fatalf("goal detail: %+v", goalDetail)
	}
	if code := employeeTaskRequest(t, f.h, get, employeeTaskCall{user: testUserID, id: taskID}, &detail); code != http.StatusOK || len(detail.Links.Dependents) != 1 || detail.Links.Dependents[0].TaskID != follower.ID {
		t.Fatalf("dependents: %d %+v", code, detail.Links)
	}
	// The originator of T1's execution did not originate these Tasks.
	if code := employeeTaskRequest(t, f.h, get, employeeTaskCall{user: originator, id: goal.ID}, nil); code != http.StatusNotFound {
		t.Fatalf("originator read another Task: %d", code)
	}

	// Listing applies the same visibility and pages without repeats.
	list := (*Handler).ListEmployeeTasks
	var listed struct {
		Tasks      []EmployeeTaskView `json:"tasks"`
		NextCursor string             `json:"next_cursor"`
	}
	if code := employeeTaskRequest(t, f.h, list, employeeTaskCall{user: stranger}, &listed); code != http.StatusOK || len(listed.Tasks) != 0 {
		t.Fatalf("stranger list: %d %+v", code, listed.Tasks)
	}
	if code := employeeTaskRequest(t, f.h, list, employeeTaskCall{user: originator, query: url.Values{"agent_id": {f.agentID}}}, &listed); code != http.StatusOK || len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != taskID {
		t.Fatalf("originator list: %d %+v", code, listed.Tasks)
	}
	all := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		q := url.Values{"agent_id": {f.agentID}, "limit": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		listed.NextCursor = ""
		if code := employeeTaskRequest(t, f.h, list, employeeTaskCall{user: testUserID, query: q}, &listed); code != http.StatusOK {
			t.Fatalf("owner list: %d", code)
		}
		for _, v := range listed.Tasks {
			if all[v.TaskID] {
				t.Fatalf("task %s listed twice", v.TaskID)
			}
			all[v.TaskID] = true
		}
		if cursor = listed.NextCursor; cursor == "" {
			break
		}
	}
	if len(all) != 4 || !all[taskID] || !all[goal.ID] || !all[upstream.ID] || !all[follower.ID] {
		t.Fatalf("owner listing: %v", all)
	}
	if code := employeeTaskRequest(t, f.h, list, employeeTaskCall{user: testUserID, query: url.Values{"state": {"waiting"}, "agent_id": {f.agentID}}}, &listed); code != http.StatusOK || len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != goal.ID {
		t.Fatalf("state filter: %d %+v", code, listed.Tasks)
	}
	for name, q := range map[string]url.Values{"state": {"state": {"bogus"}}, "agent": {"agent_id": {"agent-1"}}, "limit": {"limit": {"0"}}, "cursor": {"cursor": {"!!"}}} {
		if code := employeeTaskRequest(t, f.h, list, employeeTaskCall{user: testUserID, query: q}, nil); code != http.StatusBadRequest {
			t.Fatalf("list %s: %d", name, code)
		}
	}
}

func (v EmployeeTaskView) scopeForTest() employeetask.Scope {
	scope := employeetask.Scope{WorkspaceID: v.WorkspaceID, AgentID: v.AgentID, TenantOrgID: v.TenantOrgID, Kind: employeetask.ScopeKind(v.ScopeKind), LegacyID: v.LegacyID}
	scope.Scene.SceneID = v.SceneID
	return scope
}
