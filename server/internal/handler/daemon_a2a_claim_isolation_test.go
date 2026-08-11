package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestClaimTaskByRuntime_A2AWorkspaceDataIsolation(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withA2AClaimTestFlags(t, true, true)
	allowA2AClaimExecutionForTest(t)

	ctx := context.Background()
	const (
		workspaceContext = "Internal workspace instructions"
		workspaceRepoURL = "https://github.com/example/private-workspace-repo"
	)
	var (
		priorRepos   []byte
		priorContext pgtype.Text
	)
	if err := testPool.QueryRow(ctx, `
		SELECT repos, context
		FROM workspace
		WHERE id = $1
	`, testWorkspaceID).Scan(&priorRepos, &priorContext); err != nil {
		t.Fatalf("read workspace claim data: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE workspace
		SET repos = $1::jsonb, context = $2
		WHERE id = $3
	`, `[{"url":"`+workspaceRepoURL+`"}]`, workspaceContext, testWorkspaceID); err != nil {
		t.Fatalf("set workspace claim data: %v", err)
	}
	t.Cleanup(func() {
		if _, err := testPool.Exec(context.Background(), `
			UPDATE workspace
			SET repos = $1, context = $2
			WHERE id = $3
		`, priorRepos, priorContext, testWorkspaceID); err != nil {
			t.Errorf("restore workspace claim data: %v", err)
		}
	})

	tests := []struct {
		name              string
		taskContext       string
		wantA2A           bool
		wantWorkspaceData bool
	}{
		{
			name:              "ordinary claim keeps workspace data",
			taskContext:       `{}`,
			wantWorkspaceData: true,
		},
		{
			name:        "A2A claim clears workspace data",
			taskContext: `{"multica_origin":"a2a"}`,
			wantA2A:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := createQueuedA2AClaimTestTask(t, ctx, tc.name, "local", "claude", tc.taskContext, tc.wantA2A)
			w := postA2AClaimTestTask(fixture.runtimeID)
			if w.Code != http.StatusOK {
				t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
			}

			var envelope struct {
				Task *AgentTaskResponse `json:"task"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode claim response: %v", err)
			}
			if envelope.Task == nil || envelope.Task.ID != fixture.taskID {
				t.Fatalf("claimed task = %+v, want id %s: %s", envelope.Task, fixture.taskID, w.Body.String())
			}
			if envelope.Task.A2AInvocation != tc.wantA2A {
				t.Fatalf("a2a_invocation = %v, want %v", envelope.Task.A2AInvocation, tc.wantA2A)
			}

			if tc.wantWorkspaceData {
				if len(envelope.Task.Repos) != 1 || envelope.Task.Repos[0].URL != workspaceRepoURL {
					t.Fatalf("ordinary claim repos = %+v, want workspace repo", envelope.Task.Repos)
				}
				if envelope.Task.WorkspaceContext != workspaceContext {
					t.Fatalf("ordinary claim workspace_context = %q, want %q", envelope.Task.WorkspaceContext, workspaceContext)
				}
				if len(envelope.Task.ConnectedApps) != 1 || envelope.Task.ConnectedApps[0].ToolkitSlug != "github" {
					t.Fatalf("ordinary claim connected_apps = %+v, want GitHub app", envelope.Task.ConnectedApps)
				}
				return
			}

			if len(envelope.Task.Repos) != 0 {
				t.Fatalf("A2A claim leaked repos: %+v", envelope.Task.Repos)
			}
			if envelope.Task.WorkspaceContext != "" {
				t.Fatalf("A2A claim leaked workspace_context: %q", envelope.Task.WorkspaceContext)
			}
			if len(envelope.Task.ConnectedApps) != 0 {
				t.Fatalf("A2A claim leaked connected_apps: %+v", envelope.Task.ConnectedApps)
			}
		})
	}
}

func TestClaimTaskByRuntime_A2AManagedPrereleaseRuntimeAttestation(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withA2AClaimTestFlags(t, true, false)
	allowA2AClaimExecutionForTest(t)

	ctx := context.Background()
	fixture := createQueuedA2AClaimTestTask(
		t,
		ctx,
		"managed prerelease OpenCode A2A claim",
		"cloud",
		"opencode",
		`{"multica_origin":"a2a"}`,
		true,
	)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET metadata = $2::jsonb
		WHERE id = $1
	`, fixture.runtimeID, agentA2ATestManagedOpenCodeRuntimeMetadata); err != nil {
		t.Fatalf("mark managed prerelease runtime: %v", err)
	}

	w := postA2AClaimTestTask(fixture.runtimeID)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Task *AgentTaskResponse `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if envelope.Task == nil || envelope.Task.ID != fixture.taskID {
		t.Fatalf("claimed task = %+v, want id %s", envelope.Task, fixture.taskID)
	}
	if !envelope.Task.A2AInvocation || !envelope.Task.A2AUnsafePrereleaseRuntime {
		t.Fatalf("managed A2A attestation missing: %+v", envelope.Task)
	}
	if envelope.Task.AuthToken != "" || len(envelope.Task.Repos) != 0 || len(envelope.Task.ConnectedApps) != 0 {
		t.Fatalf("managed A2A claim leaked task credentials or workspace capabilities: %+v", envelope.Task)
	}
}

func TestClaimTaskByRuntime_A2ALegacyDaemonGetsDenyAllCompatibilityToken(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withA2AClaimTestFlags(t, true, false)
	allowA2AClaimExecutionForTest(t)

	ctx := context.Background()
	fixture := createQueuedA2AClaimTestTask(
		t,
		ctx,
		"legacy managed OpenCode A2A claim",
		"cloud",
		"opencode",
		`{"multica_origin":"a2a"}`,
		true,
	)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET metadata = $2::jsonb
		WHERE id = $1
	`, fixture.runtimeID, agentA2ATestManagedOpenCodeStableM2Metadata); err != nil {
		t.Fatalf("mark legacy managed runtime: %v", err)
	}

	w := postA2AClaimTestTaskWithCapabilities(fixture.runtimeID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Task *AgentTaskResponse `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if envelope.Task == nil || envelope.Task.ID != fixture.taskID {
		t.Fatalf("claimed task = %+v, want id %s", envelope.Task, fixture.taskID)
	}
	if envelope.Task.A2AInvocation || envelope.Task.A2AUnsafePrereleaseRuntime {
		t.Fatalf("legacy daemon must not receive native A2A markers: %+v", envelope.Task)
	}
	if !strings.HasPrefix(envelope.Task.AuthToken, "mat_") {
		t.Fatalf("legacy compatibility token has unexpected shape")
	}
	if len(envelope.Task.Repos) != 0 || envelope.Task.WorkspaceContext != "" || len(envelope.Task.ConnectedApps) != 0 {
		t.Fatalf("legacy A2A claim leaked server-owned workspace data: %+v", envelope.Task)
	}
	var persistedTokenCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id = $1`, fixture.taskID).Scan(&persistedTokenCount); err != nil {
		t.Fatalf("count persisted compatibility tokens: %v", err)
	}
	if persistedTokenCount != 0 {
		t.Fatalf("legacy A2A compatibility token unexpectedly gained API authority: %d persisted tokens", persistedTokenCount)
	}
}

func TestHardenLegacyA2AClaimResponseStripsOptionalInternalCapabilities(t *testing.T) {
	response := AgentTaskResponse{
		TraceID:                            "trace-secret",
		TraceStartedAtUnixMS:               123,
		ProjectID:                          "project-secret",
		ProjectResources:                   []ProjectResourceData{{ID: "resource-secret"}},
		PriorSessionID:                     "session-secret",
		PriorWorkDir:                       "/internal/workdir",
		ChatSessionID:                      "chat-routing-id",
		ChatMessage:                        "external A2A input",
		ChatHistory:                        "private history",
		ChatMessageAttachments:             []ChatAttachmentMeta{{ID: "attachment-secret"}},
		RequestingUserName:                 "Private Owner",
		RequestingUserProfileDescription:   "private profile",
		InitiatorID:                        "internal-user-id",
		AgentIdentityContextToken:          "identity-secret",
		AgentIdentityContextTokenExpiresAt: 123,
		Agent: &TaskAgentData{
			Name:          "Public Agent",
			Instructions:  "public instructions",
			CustomEnv:     map[string]string{"SECRET": "value"},
			CustomArgs:    []string{"--unsafe"},
			McpConfig:     json.RawMessage(`{"mcpServers":{"private":{}}}`),
			RuntimeConfig: json.RawMessage(`{"gateway":{"token":"secret"}}`),
		},
	}

	hardenLegacyA2AClaimResponse(&response)

	if response.ChatSessionID != "chat-routing-id" || response.ChatMessage != "external A2A input" {
		t.Fatalf("A2A routing/input was removed: %+v", response)
	}
	if response.TraceID != "" || response.ProjectID != "" || response.PriorSessionID != "" ||
		response.PriorWorkDir != "" || response.ChatHistory != "" || len(response.ChatMessageAttachments) != 0 ||
		response.RequestingUserName != "" || response.InitiatorID != "" || response.AgentIdentityContextToken != "" {
		t.Fatalf("legacy hardening retained internal context: %+v", response)
	}
	if response.Agent == nil || response.Agent.Name != "Public Agent" || response.Agent.Instructions != "public instructions" {
		t.Fatalf("public Agent contract was removed: %+v", response.Agent)
	}
	if len(response.Agent.CustomEnv) != 0 || len(response.Agent.CustomArgs) != 0 || len(response.Agent.RuntimeConfig) != 0 ||
		string(response.Agent.McpConfig) != `{"mcpServers":{}}` {
		t.Fatalf("legacy Agent capabilities were not stripped: %+v", response.Agent)
	}
}

func TestClaimTaskByRuntime_A2AExecutionSafetyRecheck(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	tests := []struct {
		name               string
		runtimeMode        string
		runtimeProvider    string
		taskContext        string
		withA2ABinding     bool
		revoke             func(*testing.T, *featureflag.StaticProvider)
		wantCode           int
		wantTask           bool
		wantTaskState      string
		wantError          string
		wantFailureReason  string
		wantPersistedError string
	}{
		{
			name:            "feature flag off preserves admitted A2A task",
			runtimeMode:     "local",
			runtimeProvider: "claude",
			taskContext:     `{"multica_origin":"a2a"}`,
			withA2ABinding:  true,
			revoke: func(_ *testing.T, provider *featureflag.StaticProvider) {
				provider.Set(featureflags.AgentA2AInbound, featureflag.Rule{Default: false})
			},
			wantCode:      http.StatusOK,
			wantTask:      true,
			wantTaskState: "dispatched",
		},
		{
			name:            "unsafe override revoked after admission",
			runtimeMode:     "local",
			runtimeProvider: "claude",
			taskContext:     `{"multica_origin":"a2a"}`,
			withA2ABinding:  true,
			revoke: func(t *testing.T, _ *featureflag.StaticProvider) {
				t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "")
			},
			wantCode:           http.StatusConflict,
			wantTaskState:      "failed",
			wantError:          a2aClaimRuntimeSafetyPolicyError,
			wantFailureReason:  "agent_error",
			wantPersistedError: a2aClaimRuntimeSafetyPolicyError,
		},
		{
			name:               "unsupported cloud provider fails closed",
			runtimeMode:        "cloud",
			runtimeProvider:    "claude",
			taskContext:        `{"multica_origin":"a2a"}`,
			withA2ABinding:     true,
			wantCode:           http.StatusConflict,
			wantTaskState:      "failed",
			wantError:          a2aClaimRuntimeSafetyPolicyError,
			wantFailureReason:  "agent_error",
			wantPersistedError: a2aClaimRuntimeSafetyPolicyError,
		},
		{
			name:               "unsupported local provider fails closed",
			runtimeMode:        "local",
			runtimeProvider:    "opencode",
			taskContext:        `{"multica_origin":"a2a"}`,
			withA2ABinding:     true,
			wantCode:           http.StatusConflict,
			wantTaskState:      "failed",
			wantError:          a2aClaimRuntimeSafetyPolicyError,
			wantFailureReason:  "agent_error",
			wantPersistedError: a2aClaimRuntimeSafetyPolicyError,
		},
		{
			name:            "ordinary task ignores A2A gate revocation",
			runtimeMode:     "cloud",
			runtimeProvider: "opencode",
			taskContext:     `{}`,
			revoke: func(t *testing.T, provider *featureflag.StaticProvider) {
				provider.Set(featureflags.AgentA2AInbound, featureflag.Rule{Default: false})
				t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "")
			},
			wantCode:      http.StatusOK,
			wantTask:      true,
			wantTaskState: "dispatched",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := withA2AClaimTestFlags(t, true, false)
			allowA2AClaimExecutionForTest(t)
			if !featureflags.AgentA2AInboundEnabled(ctx, testHandler.FeatureFlags) ||
				!evaluateAgentA2ARuntimeSafety(testHandler.currentConfig().PublicURL).Allowed {
				t.Fatal("test precondition: A2A execution must be allowed at admission time")
			}

			fixture := createQueuedA2AClaimTestTask(
				t,
				ctx,
				tc.name,
				tc.runtimeMode,
				tc.runtimeProvider,
				tc.taskContext,
				tc.withA2ABinding,
			)
			if tc.revoke != nil {
				tc.revoke(t, provider)
			}
			w := postA2AClaimTestTask(fixture.runtimeID)
			if w.Code != tc.wantCode {
				t.Fatalf("ClaimTaskByRuntime: expected %d, got %d: %s", tc.wantCode, w.Code, w.Body.String())
			}

			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode claim response: %v", err)
			}
			encodedTask, hasTask := envelope["task"]
			returnedTask := hasTask && string(encodedTask) != "null"
			if returnedTask != tc.wantTask {
				t.Fatalf("claim returned task = %v, want %v: %s", returnedTask, tc.wantTask, w.Body.String())
			}
			if tc.wantError != "" && !strings.Contains(w.Body.String(), tc.wantError) {
				t.Fatalf("claim error = %s, want %q", w.Body.String(), tc.wantError)
			}

			var (
				taskState     string
				failureReason pgtype.Text
				persistedErr  pgtype.Text
			)
			if err := testPool.QueryRow(ctx, `
				SELECT status, failure_reason, error
				FROM agent_task_queue
				WHERE id = $1
			`, fixture.taskID).Scan(&taskState, &failureReason, &persistedErr); err != nil {
				t.Fatalf("read task state: %v", err)
			}
			if taskState != tc.wantTaskState {
				t.Fatalf("task state = %q, want %q", taskState, tc.wantTaskState)
			}
			if failureReason.String != tc.wantFailureReason || failureReason.Valid != (tc.wantFailureReason != "") {
				t.Fatalf("failure_reason = %+v, want %q", failureReason, tc.wantFailureReason)
			}
			if persistedErr.String != tc.wantPersistedError || persistedErr.Valid != (tc.wantPersistedError != "") {
				t.Fatalf("persisted error = %+v, want %q", persistedErr, tc.wantPersistedError)
			}
			if tc.withA2ABinding {
				assertA2AClaimInputAndBindingPersisted(t, ctx, fixture, tc.wantTaskState)
			}
		})
	}
}

func TestClaimTaskByRuntime_A2ASafetyRejectTerminatesRuntimeStartAttempt(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withA2AClaimTestFlags(t, true, false)
	allowA2AClaimExecutionForTest(t)

	ctx := context.Background()
	fixture := createQueuedA2AClaimTestTask(
		t,
		ctx,
		"targeted A2A cloud runtime rejection",
		"cloud",
		"opencode",
		`{"multica_origin":"a2a"}`,
		true,
	)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET metadata = $2::jsonb
		WHERE id = $1
	`, fixture.runtimeID, agentA2ATestManagedOpenCodeStableM2Metadata); err != nil {
		t.Fatalf("mark managed runtime for safety rejection: %v", err)
	}
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(fixture.taskID))
	if err != nil {
		t.Fatalf("load A2A task for runtime start attempt: %v", err)
	}
	attempt, err := testHandler.TaskService.BeginRuntimeStartAttempt(
		ctx,
		task,
		service.SandboxBackendAliyunFC,
		service.RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin A2A runtime start attempt: %v", err)
	}
	// Runtime provider/version no longer rejects A2A. Revoke the deployment
	// safety exemption to exercise the actual fail-closed execution gate.
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "")

	w := claimRuntimeStartFixture(t, fixture.runtimeID, map[string]any{
		"target_task_id":           fixture.taskID,
		"runtime_start_attempt_id": uuidToString(attempt.ID),
		"startup_status_protocol":  service.RuntimeStartProtocolHTTPJSONV1,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("targeted A2A claim status = %d, want 409: %s", w.Code, w.Body.String())
	}

	got, err := testHandler.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID:        attempt.ID,
		TaskID:    parseUUID(fixture.taskID),
		RuntimeID: parseUUID(fixture.runtimeID),
	})
	if err != nil {
		t.Fatalf("load rejected A2A runtime start attempt: %v", err)
	}
	if got.Status != "failed" || !got.FinishedAt.Valid {
		t.Fatalf("rejected A2A runtime start attempt = %+v, want terminal failed", got)
	}
	if got.LastStage != "claim_safety_rejected" || got.ErrorCode != "A2A-CLAIM-SAFETY-REJECTED" {
		t.Fatalf("rejected A2A runtime start diagnostics = stage %q code %q", got.LastStage, got.ErrorCode)
	}
	assertA2AClaimInputAndBindingPersisted(t, ctx, fixture, "failed")
}

func TestStartTask_A2AExecutionSafetyRecheck(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withA2AClaimTestFlags(t, true, false)
	allowA2AClaimExecutionForTest(t)

	ctx := context.Background()
	fixture := createQueuedA2AClaimTestTask(
		t,
		ctx,
		"A2A start safety revocation",
		"local",
		"claude",
		`{"multica_origin":"a2a"}`,
		true,
	)
	claim := postA2AClaimTestTask(fixture.runtimeID)
	if claim.Code != http.StatusOK {
		t.Fatalf("A2A claim before safety revocation = %d: %s", claim.Code, claim.Body.String())
	}

	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "")
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/tasks/"+fixture.taskID+"/start",
		nil,
		testWorkspaceID,
		"a2a-start-safety",
	)
	req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityA2AInvocationV1)
	req = withURLParam(req, "taskId", fixture.taskID)
	testHandler.StartTask(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("A2A StartTask after safety revocation = %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), a2aClaimRuntimeSafetyPolicyError) {
		t.Fatalf("A2A StartTask error = %s, want runtime safety policy error", w.Body.String())
	}
	assertA2AClaimInputAndBindingPersisted(t, ctx, fixture, "failed")
}

func TestClaimTasksByRuntime_A2ALegacyCompatibilityUsesDenyAllToken(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withA2AClaimTestFlags(t, true, false)
	allowA2AClaimExecutionForTest(t)

	ctx := context.Background()
	fixture := createQueuedA2AClaimTestTask(
		t,
		ctx,
		"batch A2A legacy cloud runtime",
		"cloud",
		"opencode",
		`{"multica_origin":"a2a"}`,
		true,
	)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET metadata = $2::jsonb
		WHERE id = $1
	`, fixture.runtimeID, agentA2ATestManagedOpenCodeStableM2Metadata); err != nil {
		t.Fatalf("mark batch legacy managed runtime: %v", err)
	}

	w := postBatchClaim(t, testWorkspaceID, []string{fixture.runtimeID}, 1)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTasksByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response batchClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode batch claim response: %v", err)
	}
	if len(response.Tasks) != 1 || response.Tasks[0].ID != fixture.taskID {
		t.Fatalf("batch claim task = %+v, want %s", response.Tasks, fixture.taskID)
	}
	claimed := response.Tasks[0]
	if claimed.A2AInvocation || claimed.A2AUnsafePrereleaseRuntime || !strings.HasPrefix(claimed.AuthToken, "mat_") {
		t.Fatalf("batch legacy compatibility response = %+v", claimed)
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, fixture.taskID).Scan(&status); err != nil {
		t.Fatalf("read batch-claimed task state: %v", err)
	}
	if status != "dispatched" {
		t.Fatalf("batch-claimed task state = %q, want dispatched", status)
	}
	var persistedTokenCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id = $1`, fixture.taskID).Scan(&persistedTokenCount); err != nil {
		t.Fatalf("count batch compatibility tokens: %v", err)
	}
	if persistedTokenCount != 0 {
		t.Fatalf("batch legacy compatibility token unexpectedly gained API authority: %d", persistedTokenCount)
	}
	assertA2AClaimInputAndBindingPersisted(t, ctx, fixture, "dispatched")
}

func TestFailA2AClaimOnAgentLoadError_OrdinaryTaskUnchanged(t *testing.T) {
	terminalFailCalled := false
	failure := failA2AClaimOnAgentLoadError(
		context.Background(),
		&db.AgentTaskQueue{Context: []byte(`{}`)},
		errors.New("forced agent load failure"),
		func(context.Context, pgtype.UUID, string) error {
			terminalFailCalled = true
			return nil
		},
	)
	if failure != nil {
		t.Fatalf("ordinary task agent-load failure = %+v, want historical best-effort behavior", failure)
	}
	if terminalFailCalled {
		t.Fatal("ordinary task unexpectedly used the A2A terminal-failure path")
	}
}

func TestFailA2AClaimOnAgentLoadError_DurableA2ATerminalFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	fixture := createQueuedA2AClaimTestTask(
		t,
		ctx,
		"A2A agent load failure",
		"local",
		"claude",
		`{"multica_origin":"a2a"}`,
		true,
	)
	claimed, err := testHandler.TaskService.ClaimTaskByIDForRuntime(
		ctx,
		parseUUID(fixture.runtimeID),
		parseUUID(fixture.taskID),
	)
	if err != nil {
		t.Fatalf("claim A2A task for agent-load failure test: %v", err)
	}
	if claimed == nil || uuidToString(claimed.ID) != fixture.taskID {
		t.Fatalf("claimed task = %+v, want %s", claimed, fixture.taskID)
	}

	failure := failA2AClaimOnAgentLoadError(
		ctx,
		claimed,
		errors.New("forced agent load failure"),
		func(ctx context.Context, taskID pgtype.UUID, message string) error {
			if uuidToString(taskID) != fixture.taskID {
				t.Fatalf("terminal failure task = %s, want %s", uuidToString(taskID), fixture.taskID)
			}
			if message != a2aClaimAgentLoadError {
				t.Fatalf("terminal failure message = %q, want %q", message, a2aClaimAgentLoadError)
			}
			_, err := testHandler.TaskService.FailA2ATaskForExecutionSafety(ctx, taskID, message)
			return err
		},
	)
	if failure == nil {
		t.Fatal("durable A2A agent-load failure unexpectedly returned a claimable payload")
	}
	if failure.outcome != "error_a2a_agent_load" || failure.status != http.StatusInternalServerError || failure.message != a2aClaimAgentLoadError {
		t.Fatalf("claim failure = %+v, want terminal A2A agent-load failure", failure)
	}

	var (
		status        string
		failureReason pgtype.Text
		persistedErr  pgtype.Text
	)
	if err := testPool.QueryRow(ctx, `
		SELECT status, failure_reason, error
		FROM agent_task_queue
		WHERE id = $1
	`, fixture.taskID).Scan(&status, &failureReason, &persistedErr); err != nil {
		t.Fatalf("read agent-load failed task: %v", err)
	}
	if status != "failed" || !failureReason.Valid || failureReason.String != "agent_error" {
		t.Fatalf("agent-load failed task status/reason = %q/%+v, want failed/agent_error", status, failureReason)
	}
	if !persistedErr.Valid || persistedErr.String != a2aClaimAgentLoadError {
		t.Fatalf("agent-load failed task error = %+v, want %q", persistedErr, a2aClaimAgentLoadError)
	}
	assertA2AClaimInputAndBindingPersisted(t, ctx, fixture, "failed")
}

func TestA2AClaimModeNegotiationIsVersionIndependent(t *testing.T) {
	validRuntime := func(mode, provider, metadata string) db.AgentRuntime {
		return db.AgentRuntime{
			ID:          pgtype.UUID{Valid: true},
			WorkspaceID: pgtype.UUID{Valid: true},
			RuntimeMode: mode,
			Provider:    provider,
			Metadata:    []byte(metadata),
		}
	}

	if isA2AClaimRuntimeSupported(db.AgentRuntime{}) {
		t.Fatal("missing runtime identity must fail the execution-time support check")
	}
	for _, runtime := range []db.AgentRuntime{
		validRuntime("local", "claude", `{}`),
		validRuntime("cloud", "opencode", agentA2ATestManagedOpenCodeStableM2Metadata),
		validRuntime("cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadataWithoutA2A),
		validRuntime("cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadata),
		validRuntime("cloud", "opencode", `{"kind":"fc-e2b","template":"legacy-opencode"}`),
	} {
		if !isA2AClaimRuntimeSupported(runtime) {
			t.Fatalf("supported runtime family must remain A2A-executable independent of version metadata: %+v", runtime)
		}
	}
	for _, runtime := range []db.AgentRuntime{
		validRuntime("local", "codex", `{}`),
		validRuntime("cloud", "hermes", `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"hermes","artifact_kind":"e2b_template","artifact_ref":"template"}`),
		validRuntime("cloud", "opencode", `{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"opencode","artifact_kind":"oci_image","artifact_ref":"registry.example/repo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		validRuntime("cloud", "opencode", `{}`),
	} {
		if isA2AClaimRuntimeSupported(runtime) {
			t.Fatalf("runtime family without a verified A2A adapter was accepted: %+v", runtime)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/claim", nil)
	request.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityA2AInvocationV1)
	a2aContext := []byte(`{"multica_origin":"a2a"}`)

	localClaude := validRuntime("local", "claude", `{}`)
	if !requestUsesNativeA2AInvocation(request, a2aContext, localClaude) {
		t.Fatal("A2A-aware local Claude daemon must use the native tokenless path")
	}

	for name, metadata := range map[string]string{
		"stable m2 template":      agentA2ATestManagedOpenCodeStableM2Metadata,
		"pre-capability template": agentA2ATestManagedOpenCodeRuntimeMetadataWithoutA2A,
		"current template":        agentA2ATestManagedOpenCodeRuntimeMetadata,
	} {
		t.Run(name, func(t *testing.T) {
			runtime := validRuntime("cloud", "opencode", metadata)
			if !requestUsesNativeA2AInvocation(request, a2aContext, runtime) || !isA2AUnsafePrereleaseManagedRuntime(runtime) {
				t.Fatalf("managed OpenCode native negotiation must not depend on template capability/version: %+v", runtime)
			}
		})
	}

	legacyRequest := httptest.NewRequest(http.MethodPost, "/claim", nil)
	if requestUsesNativeA2AInvocation(legacyRequest, a2aContext, localClaude) {
		t.Fatal("daemon without A2A capability must use legacy compatibility")
	}
	legacyManaged := validRuntime("cloud", "opencode", agentA2ATestManagedOpenCodeStableM2Metadata)
	if requestUsesNativeA2AInvocation(legacyRequest, a2aContext, legacyManaged) {
		t.Fatal("pre-capability managed image must use legacy compatibility")
	}
	attestedManaged := validRuntime("cloud", "opencode", agentA2ATestManagedOpenCodeRuntimeMetadata)
	if !requestUsesNativeA2AInvocation(legacyRequest, a2aContext, attestedManaged) {
		t.Fatal("existing m5 image must retain its native A2A manifest attestation")
	}
	if requestUsesNativeA2AInvocation(request, []byte(`{}`), localClaude) {
		t.Fatal("ordinary task must never negotiate A2A invocation")
	}
	for _, runtime := range []db.AgentRuntime{
		validRuntime("local", "codex", `{}`),
		validRuntime("cloud", "hermes", `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"hermes"}`),
	} {
		if requestUsesNativeA2AInvocation(request, a2aContext, runtime) {
			t.Fatalf("provider without native isolation unexpectedly negotiated A2A: %+v", runtime)
		}
	}
}

func withA2AClaimTestFlags(t *testing.T, a2aEnabled, composioEnabled bool) *featureflag.StaticProvider {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.AgentA2AInbound, featureflag.Rule{Default: a2aEnabled})
	provider.Set(featureflags.ComposioMCPApps, featureflag.Rule{Default: composioEnabled})
	flags := featureflag.NewService(provider)

	originalHandlerFlags := testHandler.FeatureFlags
	originalTaskFlags := testHandler.TaskService.FeatureFlags
	testHandler.FeatureFlags = flags
	testHandler.TaskService.FeatureFlags = flags
	t.Cleanup(func() {
		testHandler.FeatureFlags = originalHandlerFlags
		testHandler.TaskService.FeatureFlags = originalTaskFlags
	})
	return provider
}

func allowA2AClaimExecutionForTest(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "true")
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{PublicURL: "http://127.0.0.1:8080"}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })
}

type a2aClaimTestFixture struct {
	runtimeID      string
	taskID         string
	inputMessageID string
	endpointID     string
	clientID       string
	messageID      string
}

func createQueuedA2AClaimTestTask(
	t *testing.T,
	ctx context.Context,
	name, runtimeMode, runtimeProvider, taskContext string,
	withA2ABinding bool,
) a2aClaimTestFixture {
	t.Helper()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, $2, $3, $4, 'online', $2, '{}'::jsonb, now(), 'private', $5)
		RETURNING id
	`, testWorkspaceID, name+" runtime", runtimeMode, runtimeProvider, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, $2, '', $3, '{}'::jsonb, $4, 'private', 1, $5)
		RETURNING id
	`, testWorkspaceID, name+" agent", runtimeMode, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (
			workspace_id, agent_id, creator_id, title, status, runtime_id
		)
		VALUES ($1, $2, $3, $4, 'active', $5)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, name, runtimeID).Scan(&chatSessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, chatSessionID)
	})

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, chat_session_id, status, priority,
			runtime_connected_apps, context
		)
		VALUES (
			$1, $2, $3, 'queued', 2,
			'[{"provider":"composio","server_name":"composio","toolkit_slug":"github","toolkit_name":"GitHub"}]'::jsonb,
			$4::jsonb
		)
		RETURNING id
	`, agentID, runtimeID, chatSessionID, taskContext).Scan(&taskID); err != nil {
		t.Fatalf("create queued task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	var inputMessageID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'user', 'A2A claim safety test input', $2)
		RETURNING id
	`, chatSessionID, taskID).Scan(&inputMessageID); err != nil {
		t.Fatalf("create task input message: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET chat_input_task_id = id
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("seal task input batch: %v", err)
	}

	fixture := a2aClaimTestFixture{
		runtimeID:      runtimeID,
		taskID:         taskID,
		inputMessageID: inputMessageID,
	}
	if !withA2ABinding {
		return fixture
	}

	identity := strings.ReplaceAll(agentID, "-", "")
	publicAgentID := "agent_" + identity
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name, card_description, card_version
		)
		VALUES ($1, $2, $3, TRUE, $4, $5, '', '1.0.0')
		RETURNING id
	`, testWorkspaceID, agentID, publicAgentID, testUserID, name).Scan(&fixture.endpointID); err != nil {
		t.Fatalf("create A2A endpoint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_a2a_endpoint WHERE id = $1`, fixture.endpointID)
	})
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client (
			endpoint_id, name, status, scopes, created_by, updated_by
		)
		VALUES ($1, $2, 'active', ARRAY['send', 'read']::text[], $3, $3)
		RETURNING id
	`, fixture.endpointID, name, testUserID).Scan(&fixture.clientID); err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	var credentialID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client_credential (
			client_id, key_id, token_hash, token_prefix, status, created_by
		)
		VALUES ($1, $2, $3, 'a2a_test', 'active', $4)
		RETURNING id
	`, fixture.clientID, "key_"+identity, identity+identity, testUserID).Scan(&credentialID); err != nil {
		t.Fatalf("create A2A credential: %v", err)
	}

	var contextID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_context (
			endpoint_id, client_id, public_context_id, chat_session_id
		)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, fixture.endpointID, fixture.clientID, "ctx_"+identity, chatSessionID).Scan(&contextID); err != nil {
		t.Fatalf("create A2A context: %v", err)
	}

	fixture.messageID = "msg_" + identity
	if _, err := testPool.Exec(ctx, `
		INSERT INTO a2a_task_binding (
			endpoint_id, client_id, context_id, accepted_credential_id,
			public_task_id, message_id, request_fingerprint, artifact_id,
			root_local_task_id, input_chat_message_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		fixture.endpointID,
		fixture.clientID,
		contextID,
		credentialID,
		"tsk_"+identity,
		fixture.messageID,
		identity+identity,
		"art_"+identity,
		fixture.taskID,
		fixture.inputMessageID,
	); err != nil {
		t.Fatalf("create A2A task binding: %v", err)
	}
	return fixture
}

func assertA2AClaimInputAndBindingPersisted(t *testing.T, ctx context.Context, fixture a2aClaimTestFixture, wantTaskStatus string) {
	t.Helper()
	var inputCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM chat_message WHERE id = $1`, fixture.inputMessageID).Scan(&inputCount); err != nil {
		t.Fatalf("count A2A input message: %v", err)
	}
	if inputCount != 1 {
		t.Fatalf("A2A input message count = %d, want 1", inputCount)
	}

	params := db.GetA2AMessageClaimForClientParams{
		EndpointID: parseUUID(fixture.endpointID),
		ClientID:   parseUUID(fixture.clientID),
		MessageID:  fixture.messageID,
	}
	first, err := testHandler.Queries.GetA2AMessageClaimForClient(ctx, params)
	if err != nil {
		t.Fatalf("load first A2A message claim: %v", err)
	}
	second, err := testHandler.Queries.GetA2AMessageClaimForClient(ctx, params)
	if err != nil {
		t.Fatalf("load repeated A2A message claim: %v", err)
	}
	if first.ID != second.ID || uuidToString(first.RootLocalTaskID) != fixture.taskID {
		t.Fatalf("repeated A2A message claim changed: first=%+v second=%+v task=%s", first, second, fixture.taskID)
	}

	projectionParams := db.GetA2ATaskProjectionForClientParams{
		EndpointID:   parseUUID(fixture.endpointID),
		ClientID:     parseUUID(fixture.clientID),
		PublicTaskID: first.PublicTaskID,
	}
	projection, err := testHandler.Queries.GetA2ATaskProjectionForClient(ctx, projectionParams)
	if err != nil {
		t.Fatalf("load A2A task projection after claim: %v", err)
	}
	if uuidToString(projection.RootLocalTaskID) != fixture.taskID ||
		uuidToString(projection.CurrentLocalTaskID) != fixture.taskID {
		t.Fatalf(
			"A2A task projection changed local task: root=%s current=%s want=%s",
			uuidToString(projection.RootLocalTaskID),
			uuidToString(projection.CurrentLocalTaskID),
			fixture.taskID,
		)
	}
	if projection.TaskStatus != wantTaskStatus {
		t.Fatalf("A2A task projection status = %q, want %q", projection.TaskStatus, wantTaskStatus)
	}

	var completionCount int
	if err := testPool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM task_completion_outbox
		WHERE terminal_task_id = $1
	`, fixture.taskID).Scan(&completionCount); err != nil {
		t.Fatalf("count A2A task completion callbacks: %v", err)
	}
	if completionCount != 0 {
		t.Fatalf("A2A task queued %d unexpected completion callbacks", completionCount)
	}
}

func postA2AClaimTestTask(runtimeID string) *httptest.ResponseRecorder {
	return postA2AClaimTestTaskWithCapabilities(runtimeID, protocol.DaemonCapabilityA2AInvocationV1)
}

func postA2AClaimTestTaskWithCapabilities(runtimeID, capabilities string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
		nil,
		testWorkspaceID,
		"a2a-claim-safety",
	)
	if capabilities != "" {
		req.Header.Set("X-Client-Capabilities", capabilities)
	}
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	return w
}
