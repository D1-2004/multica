package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestListTasksByIssueHydratesSandboxID(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID := createHandlerTestAgent(t, "SandboxListAgent", []byte("[]"))

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'sandbox-list-issue', 'todo', 'medium', $2, 'member', 92779, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	newTask := func() string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id)
			VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'completed', 0, $2)
			RETURNING id
		`, agentID, issueID).Scan(&id); err != nil {
			t.Fatalf("create task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, id) })
		return id
	}

	withSandbox := newTask()
	withoutSandbox := newTask()

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_runtime_start_attempt (
			id, task_id, runtime_id, backend, protocol, sandbox_id, status
		)
		VALUES (
			gen_random_uuid(),
			$1,
			(SELECT runtime_id FROM agent WHERE id = $2),
			'aliyun_fc',
			'http-json-v1',
			'sbx_issue_exec_1',
			'claimed'
		)
	`, withSandbox, agentID); err != nil {
		t.Fatalf("insert start attempt: %v", err)
	}

	req := newRequest("GET", "/api/issues/"+issueID+"/task-runs", nil)
	req = withURLParam(req, "id", issueID)
	w := httptest.NewRecorder()
	testHandler.ListTasksByIssue(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	byID := make(map[string]AgentTaskResponse, len(resp))
	for _, task := range resp {
		byID[task.ID] = task
	}

	got := byID[withSandbox]
	if got.SandboxID != "sbx_issue_exec_1" {
		t.Fatalf("sandbox_id = %q, want sbx_issue_exec_1; body=%s", got.SandboxID, w.Body.String())
	}
	if byID[withoutSandbox].SandboxID != "" {
		t.Fatalf("task without attempt should omit sandbox_id, got %q", byID[withoutSandbox].SandboxID)
	}
}
