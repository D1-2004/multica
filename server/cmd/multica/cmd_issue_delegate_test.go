package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newIssueDelegateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "delegate"}
	registerIssueDelegateFlags(cmd)
	return cmd
}

func TestRunIssueDelegateCreateUsesTaskContextWithoutSendingSecrets(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agents" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":   "33333333-3333-3333-3333-333333333333",
				"name": "News Agent",
			}})
			return
		}
		if r.URL.Path != "/api/issue-delegations" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("X-Task-ID"); got != "11111111-1111-1111-1111-111111111111" {
			t.Errorf("X-Task-ID = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue_id":       "issue-1",
			"target_task_id": "task-2",
			"release_parent": true,
		})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "22222222-2222-2222-2222-222222222222")
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_TASK_ID", "11111111-1111-1111-1111-111111111111")

	cmd := newIssueDelegateTestCmd()
	_ = cmd.Flags().Set("title", "采集 2026-07-29 科技新闻并生成消息卡片")
	_ = cmd.Flags().Set("description", "采集公开新闻，整理并发送卡片")
	_ = cmd.Flags().Set("assignee-id", "33333333-3333-3333-3333-333333333333")
	if err := runIssueDelegate(cmd, nil); err != nil {
		t.Fatalf("runIssueDelegate: %v", err)
	}

	if got := body["source_task_id"]; got != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("source_task_id = %#v", got)
	}
	if got := body["mode"]; got != "create" {
		t.Fatalf("mode = %#v", got)
	}
	for _, forbidden := range []string{
		"context_token",
		"agent_identity_context_token",
		"callback_url",
		"callback_token",
		"chat_session_id",
	} {
		if got, ok := body[forbidden]; ok {
			t.Fatalf("request leaked %s = %#v", forbidden, got)
		}
	}
}

func TestRunIssueDelegateContinueKeepsEachMessageIndependent(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/issues/44444444-4444-4444-4444-444444444444" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":         "44444444-4444-4444-4444-444444444444",
				"identifier": "MUL-44",
			})
			return
		}
		if r.URL.Path != "/api/issue-delegations" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue_id":       "44444444-4444-4444-4444-444444444444",
			"queued":         true,
			"release_parent": true,
		})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "22222222-2222-2222-2222-222222222222")
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_TASK_ID", "11111111-1111-1111-1111-111111111111")

	cmd := newIssueDelegateTestCmd()
	_ = cmd.Flags().Set("issue", "44444444-4444-4444-4444-444444444444")
	_ = cmd.Flags().Set("content", "不需要发送给 chensi 了")
	if err := runIssueDelegate(cmd, nil); err != nil {
		t.Fatalf("runIssueDelegate: %v", err)
	}

	if got := body["mode"]; got != "continue" {
		t.Fatalf("mode = %#v", got)
	}
	if got := body["content"]; got != "不需要发送给 chensi 了" {
		t.Fatalf("content = %#v", got)
	}
	if _, exists := body["title"]; exists {
		t.Fatal("continue request unexpectedly sent title")
	}
}

func TestRunIssueDelegateRejectsTaskIDDifferentFromInjectedTask(t *testing.T) {
	t.Setenv("MULTICA_TASK_ID", "11111111-1111-1111-1111-111111111111")
	cmd := newIssueDelegateTestCmd()
	_ = cmd.Flags().Set("task-id", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	_ = cmd.Flags().Set("title", "后台任务")
	_ = cmd.Flags().Set("description", "执行后台任务")
	_ = cmd.Flags().Set("assignee-id", "33333333-3333-3333-3333-333333333333")

	err := runIssueDelegate(cmd, nil)
	if err == nil {
		t.Fatal("expected task-id mismatch error")
	}
}

func TestRunIssueDelegateRequiresTaskScopedExecution(t *testing.T) {
	t.Setenv("MULTICA_TASK_ID", "")
	cmd := newIssueDelegateTestCmd()
	_ = cmd.Flags().Set("title", "后台任务")
	_ = cmd.Flags().Set("description", "执行后台任务")
	_ = cmd.Flags().Set("assignee-id", "33333333-3333-3333-3333-333333333333")

	err := runIssueDelegate(cmd, nil)
	if err == nil {
		t.Fatal("expected missing task context error")
	}
}

func TestRunIssueDelegateRejectsResponseWithoutCommittedHandoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agents" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":   "33333333-3333-3333-3333-333333333333",
				"name": "News Agent",
			}})
			return
		}
		if r.URL.Path != "/api/issue-delegations" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue_id":       "issue-without-handoff",
			"target_task_id": "task-without-handoff",
			"queued":         true,
			"release_parent": false,
		})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "22222222-2222-2222-2222-222222222222")
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_TASK_ID", "11111111-1111-1111-1111-111111111111")

	cmd := newIssueDelegateTestCmd()
	_ = cmd.Flags().Set("title", "后台任务")
	_ = cmd.Flags().Set("description", "执行后台任务")
	_ = cmd.Flags().Set("assignee-id", "33333333-3333-3333-3333-333333333333")

	if err := runIssueDelegate(cmd, nil); err == nil {
		t.Fatal("response without release_parent=true was accepted")
	}
}
