package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMakeChatHistoryAuthoritative(t *testing.T) {
	t.Parallel()

	resp := AgentTaskResponse{
		ChatHistory:                   "User:\nremember the blue lantern",
		PriorSessionID:                "provider-session-from-another-sandbox",
		PriorSessionResumeUnavailable: true,
	}
	makeChatHistoryAuthoritative(&resp)

	if resp.PriorSessionID != "" {
		t.Fatalf("PriorSessionID = %q, want empty", resp.PriorSessionID)
	}
	if resp.PriorSessionResumeUnavailable {
		t.Fatal("database history is available; unrecoverable-session notice must be false")
	}
}

// A task-owned direct-chat task (chat_input_task_id set — the shape every
// web/mobile send has taken since MUL-4351) claimed by either cloud backend
// carries the already-answered transcript on both cold and warm claims. The
// server makes that transcript authoritative and withholds the provider-local
// resume pointer, so separately-versioned cloud images cannot contradict the
// recovered history when their daemon fails a native resume.
func TestClaimTaskByRuntime_CloudSandboxCarriesTaskOwnedChatHistory(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	tests := []struct {
		name      string
		metadata  string
		coldStart bool
	}{
		{name: "fc cold", metadata: `{"kind":"fc-e2b"}`, coldStart: true},
		{name: "fc warm", metadata: `{"kind":"fc-e2b"}`, coldStart: false},
		{
			name: "asb cold",
			metadata: `{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"opencode","artifact_kind":"oci_image",` +
				`"artifact_ref":"hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			coldStart: true,
		},
		{
			name: "asb warm",
			metadata: `{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"opencode","artifact_kind":"oci_image",` +
				`"artifact_ref":"hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			coldStart: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			var runtimeID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, 'cloud history runtime', 'cloud', 'handler_test_runtime',
			'online', 'cloud history fixture', $3::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID, tc.metadata).Scan(&runtimeID); err != nil {
				t.Fatalf("setup: create cloud runtime: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

			agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "FC E2B history agent")

			var sessionID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title, status,
			runtime_id, session_id, work_dir
		)
		VALUES (
			$1, $2, $3, 'fc-e2b history', 'active',
			$4, 'provider-session-from-previous-turn', '/workspace/previous-turn/workdir'
		)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&sessionID); err != nil {
				t.Fatalf("setup: create chat session: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, sessionID) })

			// An already-answered turn: the transcript the cold-started microVM must
			// be told about.
			if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, created_at)
		VALUES ($1, 'user', 'what is the capital of France?', now() - interval '2 minutes'),
		       ($1, 'assistant', 'Paris.', now() - interval '1 minute')
	`, sessionID); err != nil {
				t.Fatalf("setup: seed answered turn: %v", err)
			}

			var taskID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
		VALUES ($1, $2, 'queued', 0, $3)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
				t.Fatalf("setup: seed queued chat task: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

			// The new turn's input batch is owned by the task itself — the task-owned
			// shape that ListChatInputMessages reads.
			if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'user', 'and its population?', $2)
	`, sessionID, taskID); err != nil {
				t.Fatalf("setup: seed input batch message: %v", err)
			}
			if _, err := testPool.Exec(ctx,
				`UPDATE agent_task_queue SET chat_input_task_id = id WHERE id = $1`, taskID); err != nil {
				t.Fatalf("setup: seal input batch: %v", err)
			}

			w := httptest.NewRecorder()
			req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
				map[string]any{"fc_e2b_cold_start": tc.coldStart}, testWorkspaceID, "cloud-history")
			req = withURLParam(req, "runtimeId", runtimeID)
			testHandler.ClaimTaskByRuntime(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var resp struct {
				Task *struct {
					ID                            string `json:"id"`
					ChatHistory                   string `json:"chat_history"`
					Prompt                        string `json:"prompt"`
					PriorSessionID                string `json:"prior_session_id"`
					PriorSessionResumeUnavailable bool   `json:"prior_session_resume_unavailable"`
				} `json:"task"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode claim response: %v", err)
			}
			if resp.Task == nil {
				t.Fatalf("expected a claimable task, got none: %s", w.Body.String())
			}
			if resp.Task.ID != taskID {
				t.Fatalf("claimed task = %s, want %s", resp.Task.ID, taskID)
			}
			if resp.Task.ChatHistory == "" {
				t.Fatal("expected the answered transcript to be carried by the cloud claim, got empty chat_history")
			}
			if !strings.Contains(resp.Task.ChatHistory, "capital of France") ||
				!strings.Contains(resp.Task.ChatHistory, "Paris.") {
				t.Errorf("chat_history missing the answered turn: %q", resp.Task.ChatHistory)
			}
			// The pending input batch is delivered as the prompt, not as history — it
			// must not be double-sent.
			if strings.Contains(resp.Task.ChatHistory, "and its population?") {
				t.Errorf("chat_history must stop before the task's own input batch: %q", resp.Task.ChatHistory)
			}
			if resp.Task.PriorSessionID != "" {
				t.Errorf("provider session must be withheld when database history is present, got %q", resp.Task.PriorSessionID)
			}
			if resp.Task.PriorSessionResumeUnavailable {
				t.Fatal("database history is available; claim must not carry an unrecoverable-session notice")
			}
		})
	}
}
