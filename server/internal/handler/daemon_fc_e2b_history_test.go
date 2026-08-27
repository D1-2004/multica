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

// A task-owned chat task (chat_input_task_id set — the shape every web/mobile
// send and current channel enqueue uses) claimed by either cloud backend
// carries the already-answered transcript on both cold and warm claims. The
// server makes that transcript authoritative and withholds the provider-local
// resume pointer, so separately-versioned cloud images cannot contradict the
// recovered history when their daemon fails a native resume.
func TestClaimTaskByRuntime_CloudSandboxCarriesTaskOwnedChatHistory(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	tests := []struct {
		name        string
		metadata    string
		channelType string
		coldStart   bool
	}{
		{name: "web fc cold", metadata: `{"kind":"fc-e2b"}`, coldStart: true},
		{name: "web fc warm", metadata: `{"kind":"fc-e2b"}`, coldStart: false},
		{
			name: "web asb cold",
			metadata: `{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"opencode","artifact_kind":"oci_image",` +
				`"artifact_ref":"hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			coldStart: true,
		},
		{
			name: "web asb warm",
			metadata: `{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"opencode","artifact_kind":"oci_image",` +
				`"artifact_ref":"hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			coldStart: false,
		},
		{name: "dingtalk fc cold", metadata: `{"kind":"fc-e2b"}`, channelType: "dingtalk", coldStart: true},
		{name: "dingtalk fc warm", metadata: `{"kind":"fc-e2b"}`, channelType: "dingtalk", coldStart: false},
		{
			name:        "dingtalk asb cold",
			channelType: "dingtalk",
			metadata: `{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"opencode","artifact_kind":"oci_image",` +
				`"artifact_ref":"hub.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			coldStart: true,
		},
		{
			name:        "dingtalk asb warm",
			channelType: "dingtalk",
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
			if tc.channelType != "" {
				seedChannelBinding(t, ctx, agentID, sessionID, tc.channelType, "message-previous", "message-previous")
			}

			// An already-answered turn: the transcript the cold-started microVM must
			// be told about.
			if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, created_at, channel_ingested)
		VALUES ($1, 'user', 'what is the capital of France?', now() - interval '2 minutes', $2),
		       ($1, 'assistant', 'Paris.', now() - interval '1 minute', FALSE)
	`, sessionID, tc.channelType != ""); err != nil {
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
		INSERT INTO chat_message (chat_session_id, role, content, task_id, channel_ingested)
		VALUES ($1, 'user', 'and its population?', $2, $3)
	`, sessionID, taskID, tc.channelType != ""); err != nil {
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
					ChatChannelType               string `json:"chat_channel_type"`
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
			if resp.Task.ChatChannelType != tc.channelType {
				t.Fatalf("chat_channel_type = %q, want %q", resp.Task.ChatChannelType, tc.channelType)
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

func TestClaimTaskByRuntime_CloudChatWarmResumeGates(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	const metadata = `{"kind":"fc-e2b"}`
	tests := []struct {
		name         string
		enableResume bool
		chatType     string // empty = first-party 1:1
		identity     string
		completedAgo string
		coldStart    bool
		wantPriorID  string
		wantHistory  bool
	}{
		{
			name:         "group never resumes",
			enableResume: true,
			chatType:     "group",
			completedAgo: "2 minutes",
			wantHistory:  true,
		},
		{
			name:         "agent identity changed",
			enableResume: true,
			identity:     "stale-identity",
			completedAgo: "2 minutes",
			wantHistory:  true,
		},
		{
			name:         "last answer older than 20 minutes",
			enableResume: true,
			completedAgo: "21 minutes",
			wantHistory:  true,
		},
		{
			name:         "switch off keeps transcript-only contract",
			completedAgo: "2 minutes",
			wantHistory:  true,
		},
		{
			name:         "cold sandbox withholds provider session",
			enableResume: true,
			completedAgo: "2 minutes",
			coldStart:    true,
			wantHistory:  true,
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
		VALUES ($1, NULL, 'cloud resume runtime', 'cloud', 'handler_test_runtime',
			'online', 'cloud resume fixture', $3::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID, metadata).Scan(&runtimeID); err != nil {
				t.Fatalf("setup: create cloud runtime: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

			agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "cloud resume agent")
			if tc.enableResume {
				if _, err := testPool.Exec(ctx, `UPDATE agent SET chat_session_resume = true WHERE id = $1`, agentID); err != nil {
					t.Fatalf("enable chat_session_resume: %v", err)
				}
			}

			identity := tc.identity
			if identity == "" {
				agentRow, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
				if err != nil {
					t.Fatalf("load agent for identity: %v", err)
				}
				runtimeRow, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
				if err != nil {
					t.Fatalf("load runtime for identity: %v", err)
				}
				identity = cloudChatResumeIdentityFromClaim(agentRow, runtimeRow, &TaskAgentData{})
			}

			var sessionID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title, status,
			runtime_id, session_id, work_dir, resume_identity
		)
		VALUES (
			$1, $2, $3, 'cloud resume', 'active',
			$4, 'provider-session-from-previous-turn', '/workspace/previous-turn/workdir', $5
		)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID, identity).Scan(&sessionID); err != nil {
				t.Fatalf("setup: create chat session: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, sessionID) })
			if tc.chatType != "" {
				seedChannelBindingOfChatType(t, ctx, agentID, sessionID, "dingtalk", tc.chatType, "message-previous", "message-previous")
			}

			if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, created_at)
		VALUES ($1, 'user', 'what is the capital of France?', now() - interval '2 minutes'),
		       ($1, 'assistant', 'Paris.', now() - interval '1 minute')
	`, sessionID); err != nil {
				t.Fatalf("setup: seed answered turn: %v", err)
			}

			if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, chat_session_id, session_id, completed_at
		)
		VALUES ($1, $2, 'completed', 0, $3, 'provider-session-from-previous-turn', now() - ($4)::interval)
	`, agentID, runtimeID, sessionID, tc.completedAgo); err != nil {
				t.Fatalf("setup: seed completed prior task: %v", err)
			}

			var taskID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
		VALUES ($1, $2, 'queued', 0, $3)
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
				t.Fatalf("setup: seed queued chat task: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID) })

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

			var attemptID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_runtime_start_attempt (
			id, task_id, runtime_id, backend, protocol, sandbox_id, cold_start, status
		)
		VALUES (gen_random_uuid(), $1, $2, 'aliyun_fc', 'http-json-v1', 'sbx-resume', $3, 'starting')
		RETURNING id
	`, taskID, runtimeID, tc.coldStart).Scan(&attemptID); err != nil {
				t.Fatalf("setup: seed start attempt: %v", err)
			}

			w := httptest.NewRecorder()
			req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
				map[string]any{
					"fc_e2b_cold_start":        tc.coldStart,
					"target_task_id":           taskID,
					"runtime_start_attempt_id": attemptID,
					"startup_status_protocol":  "http-json-v1",
				}, testWorkspaceID, "cloud-resume")
			req = withURLParam(req, "runtimeId", runtimeID)
			testHandler.ClaimTaskByRuntime(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var resp struct {
				Task *struct {
					ID             string `json:"id"`
					ChatHistory    string `json:"chat_history"`
					PriorSessionID string `json:"prior_session_id"`
				} `json:"task"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode claim response: %v", err)
			}
			if resp.Task == nil {
				t.Fatalf("expected a claimable task, got none: %s", w.Body.String())
			}
			if resp.Task.PriorSessionID != tc.wantPriorID {
				t.Errorf("prior_session_id = %q, want %q", resp.Task.PriorSessionID, tc.wantPriorID)
			}
			if tc.wantHistory && resp.Task.ChatHistory == "" {
				t.Fatal("expected chat_history to stay on the claim for resume fallback")
			}
		})
	}
}

func TestClaimTaskByRuntime_CloudChatWarmResumeAfterPriorTurn(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	for _, tc := range []struct {
		name     string
		chatType string
	}{
		{name: "web direct"},
		{name: "dingtalk p2p", chatType: "p2p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			const metadata = `{"kind":"fc-e2b"}`

			var runtimeID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, 'cloud resume runtime', 'cloud', 'handler_test_runtime',
			'online', 'cloud resume fixture', $3::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID, metadata).Scan(&runtimeID); err != nil {
				t.Fatalf("setup: create cloud runtime: %v", err)
			}
			t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

			agentID, _ := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "cloud resume agent")
			if _, err := testPool.Exec(ctx, `UPDATE agent SET chat_session_resume = true WHERE id = $1`, agentID); err != nil {
				t.Fatalf("enable chat_session_resume: %v", err)
			}

			var sessionID string
			if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title, status,
			runtime_id, session_id, work_dir
		)
		VALUES (
			$1, $2, $3, 'cloud resume', 'active',
			$4, 'provider-session-from-previous-turn', '/workspace/previous-turn/workdir'
		)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, runtimeID).Scan(&sessionID); err != nil {
				t.Fatalf("setup: create chat session: %v", err)
			}
			t.Cleanup(func() {
				testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID)
				testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, sessionID)
			})
			if tc.chatType != "" {
				seedChannelBindingOfChatType(t, ctx, agentID, sessionID, "dingtalk", tc.chatType, "message-previous", "message-previous")
			}

			if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, created_at)
		VALUES ($1, 'user', 'what is the capital of France?', now() - interval '2 minutes'),
		       ($1, 'assistant', 'Paris.', now() - interval '1 minute')
	`, sessionID); err != nil {
				t.Fatalf("setup: seed answered turn: %v", err)
			}
			if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, chat_session_id, session_id, completed_at
		)
		VALUES ($1, $2, 'completed', 0, $3, 'provider-session-from-previous-turn', now() - interval '2 minutes')
	`, agentID, runtimeID, sessionID); err != nil {
				t.Fatalf("setup: seed completed prior task: %v", err)
			}

			claimFollowUp := func() string {
				t.Helper()
				var taskID string
				if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
			VALUES ($1, $2, 'queued', 0, $3)
			RETURNING id
		`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
					t.Fatalf("setup: seed queued chat task: %v", err)
				}
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
				var attemptID string
				if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_runtime_start_attempt (
				id, task_id, runtime_id, backend, protocol, sandbox_id, cold_start, status
			)
			VALUES (gen_random_uuid(), $1, $2, 'aliyun_fc', 'http-json-v1', 'sbx-resume', false, 'starting')
			RETURNING id
		`, taskID, runtimeID).Scan(&attemptID); err != nil {
					t.Fatalf("setup: seed start attempt: %v", err)
				}
				w := httptest.NewRecorder()
				req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
					map[string]any{
						"fc_e2b_cold_start":        false,
						"target_task_id":           taskID,
						"runtime_start_attempt_id": attemptID,
						"startup_status_protocol":  "http-json-v1",
					}, testWorkspaceID, "cloud-resume")
				req = withURLParam(req, "runtimeId", runtimeID)
				testHandler.ClaimTaskByRuntime(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
				}
				var resp struct {
					Task *struct {
						PriorSessionID string `json:"prior_session_id"`
						ChatHistory    string `json:"chat_history"`
					} `json:"task"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode claim response: %v", err)
				}
				if resp.Task == nil {
					t.Fatalf("expected a claimable task, got none: %s", w.Body.String())
				}
				if resp.Task.ChatHistory == "" {
					t.Fatal("expected chat_history to stay on the claim for resume fallback")
				}
				return resp.Task.PriorSessionID
			}

			if prior := claimFollowUp(); prior != "" {
				t.Fatalf("first follow-up must write identity before resuming, got prior_session_id %q", prior)
			}
			if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'completed', completed_at = now(), session_id = 'provider-session-from-previous-turn'
		WHERE chat_session_id = $1 AND status = 'dispatched'
	`, sessionID); err != nil {
				t.Fatalf("complete first follow-up: %v", err)
			}
			if prior := claimFollowUp(); prior != "provider-session-from-previous-turn" {
				t.Fatalf("second follow-up prior_session_id = %q, want provider-session-from-previous-turn", prior)
			}
		})
	}
}
