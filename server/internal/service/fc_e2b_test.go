package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeAgentIdentityContextCreator struct {
	requests []agentidentityhsf.CreateContextRequest
	result   agentidentityhsf.CreateContextResult
	err      error
}

func (f *fakeAgentIdentityContextCreator) CreateContext(_ context.Context, request agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error) {
	f.requests = append(f.requests, request)
	return f.result, f.err
}

type fakeCommandRunner struct {
	calls     []fakeCommandCall
	deadlines []bool
	out       []string
}

type fakeCommandCall struct {
	name string
	args []string
	env  []string
}

func (f *fakeCommandRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	f.calls = append(f.calls, fakeCommandCall{
		name: name,
		args: append([]string(nil), args...),
		env:  append([]string(nil), env...),
	})
	_, hasDeadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDeadline)
	if len(f.out) == 0 {
		return "", nil
	}
	out := f.out[0]
	f.out = f.out[1:]
	return out, nil
}

func TestFCE2BTaskLaunchBlocker(t *testing.T) {
	agentID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	chatID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	otherChatID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	target := db.AgentTaskQueue{
		ID:            util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
		AgentID:       agentID,
		ChatSessionID: chatID,
		Status:        "queued",
		Priority:      2,
		CreatedAt:     pgtype.Timestamptz{Time: time.Date(2026, 7, 16, 14, 12, 0, 0, time.UTC), Valid: true},
	}
	task := func(id string, chat pgtype.UUID, status string, priority int32, createdAt time.Time) db.AgentTaskQueue {
		return db.AgentTaskQueue{
			ID:            util.MustParseUUID(id),
			AgentID:       agentID,
			ChatSessionID: chat,
			Status:        status,
			Priority:      priority,
			CreatedAt:     pgtype.Timestamptz{Time: createdAt, Valid: true},
		}
	}

	t.Run("active task in the same chat blocks before side effects", func(t *testing.T) {
		active := task("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", chatID, "running", 2, target.CreatedAt.Time.Add(-time.Minute))
		blocker, reason, blocked := fcE2BTaskLaunchBlocker(target, []db.AgentTaskQueue{target, active})
		if !blocked || blocker.ID != active.ID || reason != "active_task" {
			t.Fatalf("blocker = %#v, reason = %q, blocked = %v", blocker, reason, blocked)
		}
	})

	t.Run("earlier queued task in the same chat preserves claim order", func(t *testing.T) {
		earlier := task("cccccccc-cccc-cccc-cccc-cccccccccccc", chatID, "queued", 2, target.CreatedAt.Time.Add(-time.Second))
		blocker, reason, blocked := fcE2BTaskLaunchBlocker(target, []db.AgentTaskQueue{target, earlier})
		if !blocked || blocker.ID != earlier.ID || reason != "queued_predecessor" {
			t.Fatalf("blocker = %#v, reason = %q, blocked = %v", blocker, reason, blocked)
		}
	})

	t.Run("higher priority queued task blocks even when created later", func(t *testing.T) {
		higher := task("dddddddd-dddd-dddd-dddd-dddddddddddd", chatID, "queued", 3, target.CreatedAt.Time.Add(time.Second))
		blocker, reason, blocked := fcE2BTaskLaunchBlocker(target, []db.AgentTaskQueue{target, higher})
		if !blocked || blocker.ID != higher.ID || reason != "queued_predecessor" {
			t.Fatalf("blocker = %#v, reason = %q, blocked = %v", blocker, reason, blocked)
		}
	})

	t.Run("later task and other chat do not block", func(t *testing.T) {
		later := task("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee", chatID, "queued", 2, target.CreatedAt.Time.Add(time.Second))
		otherChat := task("ffffffff-ffff-ffff-ffff-ffffffffffff", otherChatID, "running", 2, target.CreatedAt.Time.Add(-time.Minute))
		completed := task("99999999-9999-9999-9999-999999999999", chatID, "completed", 2, target.CreatedAt.Time.Add(-time.Minute))
		if blocker, reason, blocked := fcE2BTaskLaunchBlocker(target, []db.AgentTaskQueue{target, later, otherChat, completed}); blocked {
			t.Fatalf("unexpected blocker = %#v, reason = %q", blocker, reason)
		}
	})
}

func TestFCE2BLauncherCommandsUseSandboxReadyTimeout(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{
		"Sandbox created with ID sbx_timeout using template multica-fc-hermes-v1",
	}}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		APIKey:              "e2b_secret",
		APIURL:              "https://api.cn-beijing.e2b.fc.aliyuncs.com",
		Domain:              "cn-beijing.e2b.fc.aliyuncs.com",
		CLIPath:             "/usr/local/bin/e2b",
		TimeoutSeconds:      1800,
		SandboxReadyTimeout: time.Minute,
	}, runner)

	if _, err := launcher.createSandbox(context.Background(), "multica-fc-hermes-v1"); err != nil {
		t.Fatalf("createSandbox returned error: %v", err)
	}
	if len(runner.deadlines) != 1 || !runner.deadlines[0] {
		t.Fatalf("createSandbox runner deadline = %#v, want one deadline", runner.deadlines)
	}
}

func TestParseE2BSandboxIDStrictCreateOutput(t *testing.T) {
	got, err := parseE2BSandboxID("Sandbox created with ID sbx_123abc using template multica-fc-hermes-v1\n")
	if err != nil {
		t.Fatalf("parseE2BSandboxID returned error: %v", err)
	}
	if got != "sbx_123abc" {
		t.Fatalf("sandbox id = %q, want sbx_123abc", got)
	}
	if _, err := parseE2BSandboxID("sandbox: sbx_123abc"); err == nil {
		t.Fatal("parseE2BSandboxID must reject unrecognized output")
	}
}

func TestParseFCE2BTemplatesUsesAliasesAndBuildStatus(t *testing.T) {
	got, err := parseFCE2BTemplates(`[
		{
			"templateID": "idt7f6on323gsyuqjt59",
			"aliases": ["multica-fc-hermes-dws-v1"],
			"names": ["multica-fc-hermes-dws-v1"],
			"buildStatus": "ready",
			"createdAt": "2026-07-08T13:16:30.740524Z",
			"updatedAt": "2026-07-08T13:19:01.365773Z"
		}
	]`)
	if err != nil {
		t.Fatalf("parseFCE2BTemplates returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("templates = %d, want 1", len(got))
	}
	if got[0].ID != "idt7f6on323gsyuqjt59" {
		t.Fatalf("id = %q", got[0].ID)
	}
	if got[0].Name != "multica-fc-hermes-dws-v1" {
		t.Fatalf("name = %q", got[0].Name)
	}
	if got[0].Template != "multica-fc-hermes-dws-v1" {
		t.Fatalf("template = %q", got[0].Template)
	}
	if got[0].Status != "ready" {
		t.Fatalf("status = %q", got[0].Status)
	}
	if got[0].UpdatedAt != "2026-07-08T13:19:01.365773Z" {
		t.Fatalf("updated_at = %q", got[0].UpdatedAt)
	}
}

func TestFCE2BLauncherBuildsCreateAndExecCommands(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{
		"Sandbox created with ID sbx_123 using template multica-fc-hermes-v1",
		"",
		"",
	}}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		Enabled:             true,
		Template:            "multica-fc-hermes-v1",
		ServerURL:           "https://api.multica.test",
		APIKey:              "e2b_secret",
		APIURL:              "https://api.cn-beijing.e2b.fc.aliyuncs.com",
		Domain:              "cn-beijing.e2b.fc.aliyuncs.com",
		LLMBaseURL:          "https://api-deap.dingtalk.com/deapai",
		LLMAPIKey:           "maas_secret",
		LLMModels:           []string{"qwen3.5-plus", "qwen3.7-max"},
		CLIPath:             "/usr/local/bin/e2b",
		TimeoutSeconds:      1800,
		SandboxReadyTimeout: time.Second,
	}, runner)

	sandboxID, err := launcher.createSandbox(context.Background(), "multica-fc-hermes-v1")
	if err != nil {
		t.Fatalf("createSandbox returned error: %v", err)
	}
	if sandboxID != "sbx_123" {
		t.Fatalf("sandbox id = %q, want sbx_123", sandboxID)
	}
	if err := launcher.waitSandboxReady(context.Background(), sandboxID); err != nil {
		t.Fatalf("waitSandboxReady returned error: %v", err)
	}
	runtimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	rt := db.AgentRuntime{
		ID:       runtimeID,
		Name:     "FC-Hermes",
		DaemonID: pgtype.Text{String: "fc-e2b:ws:fc-hermes", Valid: true},
	}
	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if err := launcher.execRunOnce(context.Background(), sandboxID, rt, taskID, "mdt_test_token", true, map[string]string{
		"OPENAI_MODEL": "qwen3.7-max",
	}); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}

	if got := len(runner.calls); got != 3 {
		t.Fatalf("runner calls = %d, want 3", got)
	}
	wantEnv := []string{
		"E2B_API_KEY=e2b_secret",
		"E2B_API_URL=https://api.cn-beijing.e2b.fc.aliyuncs.com",
		"E2B_DOMAIN=cn-beijing.e2b.fc.aliyuncs.com",
	}
	for i, call := range runner.calls {
		if call.name != "/usr/local/bin/e2b" {
			t.Fatalf("call %d name = %q", i, call.name)
		}
		if !reflect.DeepEqual(call.env, wantEnv) {
			t.Fatalf("call %d env = %#v, want %#v", i, call.env, wantEnv)
		}
	}
	wantCreateArgs := []string{
		"sandbox", "create",
		"--detach",
		"--timeout", "1800",
		"--lifecycle.ontimeout", "kill",
		"multica-fc-hermes-v1",
	}
	if !reflect.DeepEqual(runner.calls[0].args, wantCreateArgs) {
		t.Fatalf("create args = %#v, want %#v", runner.calls[0].args, wantCreateArgs)
	}
	wantReadyArgs := []string{"sandbox", "exec", "sbx_123", "true"}
	if !reflect.DeepEqual(runner.calls[1].args, wantReadyArgs) {
		t.Fatalf("ready args = %#v, want %#v", runner.calls[1].args, wantReadyArgs)
	}
	wantExecArgs := []string{
		"sandbox", "exec",
		"--background",
		"-e", "MULTICA_SERVER_URL=https://api.multica.test",
		"-e", "MULTICA_DAEMON_TOKEN=mdt_test_token",
		"-e", "MULTICA_RUNTIME_ID=11111111-1111-1111-1111-111111111111",
		"-e", "MULTICA_TASK_ID=22222222-2222-2222-2222-222222222222",
		"-e", "MULTICA_DAEMON_ID=fc-e2b:ws:fc-hermes",
		"-e", "MULTICA_AGENT_RUNTIME_NAME=FC-Hermes",
		"-e", "HOME=/home/user",
		"-e", "DWS_CONFIG_DIR=/home/user/.dws",
		"-e", "OPENAI_BASE_URL=https://api-deap.dingtalk.com/deapai",
		"-e", "OPENAI_API_KEY=maas_secret",
		"-e", "MULTICA_FC_E2B_COLD_START=true",
		"-e", "OPENAI_MODEL=qwen3.7-max",
		"sbx_123",
		"--",
		"multica-fc-hermes-runner",
		"--runtime-id", "11111111-1111-1111-1111-111111111111",
		"--provider", "hermes",
		"--health-port", strconv.Itoa(fcE2BHealthPortForTask(taskID)),
	}
	if !reflect.DeepEqual(runner.calls[2].args, wantExecArgs) {
		t.Fatalf("exec args = %#v, want %#v", runner.calls[2].args, wantExecArgs)
	}
}

func TestFCE2BExecRunOnceWarmSandboxDoesNotInjectColdStart(t *testing.T) {
	runner := &fakeCommandRunner{}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		ServerURL:  "https://api.multica.test",
		APIKey:     "e2b_secret",
		APIURL:     "https://api.cn-beijing.e2b.fc.aliyuncs.com",
		Domain:     "cn-beijing.e2b.fc.aliyuncs.com",
		LLMBaseURL: "https://api-deap.dingtalk.com/deapai",
		LLMAPIKey:  "maas_secret",
		LLMModels:  []string{"qwen3.5-plus"},
		CLIPath:    "/usr/local/bin/e2b",
	}, runner)
	rt := db.AgentRuntime{
		ID:       util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Name:     "FC-Hermes",
		DaemonID: pgtype.Text{String: "fc-e2b:ws:fc-hermes", Valid: true},
	}

	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if err := launcher.execRunOnce(context.Background(), "sbx_warm", rt, taskID, "mdt_test_token", false, nil); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	for _, arg := range runner.calls[0].args {
		if arg == "MULTICA_FC_E2B_COLD_START=true" {
			t.Fatalf("warm sandbox exec must not inject cold-start marker: %#v", runner.calls[0].args)
		}
	}
	foundHealthPort := false
	for i := 0; i < len(runner.calls[0].args)-1; i++ {
		if runner.calls[0].args[i] == "--health-port" && runner.calls[0].args[i+1] == strconv.Itoa(fcE2BHealthPortForTask(taskID)) {
			foundHealthPort = true
			break
		}
	}
	if !foundHealthPort {
		t.Fatalf("warm sandbox exec must pass task-specific health port: %#v", runner.calls[0].args)
	}
}

func TestFCE2BExecRunOnceInjectsExtraEnv(t *testing.T) {
	runner := &fakeCommandRunner{}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		ServerURL:  "https://api.multica.test",
		APIKey:     "e2b_secret",
		APIURL:     "https://api.cn-beijing.e2b.fc.aliyuncs.com",
		Domain:     "cn-beijing.e2b.fc.aliyuncs.com",
		LLMBaseURL: "https://api-deap.dingtalk.com/deapai",
		LLMAPIKey:  "maas_secret",
		LLMModels:  []string{"qwen3.5-plus"},
		CLIPath:    "/usr/local/bin/e2b",
	}, runner)
	rt := db.AgentRuntime{
		ID:       util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Name:     "FC-Hermes-DWS",
		DaemonID: pgtype.Text{String: "fc-e2b:ws:fc-hermes-dws", Valid: true},
	}

	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if err := launcher.execRunOnce(context.Background(), "sbx_dws", rt, taskID, "mdt_test_token", false, map[string]string{
		"AGENT_IDENTITY_CONTEXT_TOKEN": "context_secret",
	}); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	args := runner.calls[0].args
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && args[i+1] == "AGENT_IDENTITY_CONTEXT_TOKEN=context_secret" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("exec args did not include DWS auth env")
	}
}

func TestFCE2BChatIdentityComesOnlyFromAgentBinding(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	suffix := time.Now().UnixNano()

	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "FC No DWS Test", fmt.Sprintf("fc-no-dws-%d@multica.test", suffix)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'FND')
		RETURNING id
	`, "FC No DWS Test", fmt.Sprintf("fc-no-dws-%d", suffix)).Scan(&workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	var runtimeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status,
			device_info, metadata, visibility, owner_id
		)
		VALUES ($1, 'FC DWS Runtime', 'cloud', 'hermes', 'online',
			'test runtime', '{"kind":"fc-e2b","capabilities":["hermes","dws"]}'::jsonb, 'private', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var agentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'FC Agent Without DWS', 'cloud', '{}'::jsonb, $2, 'private', 1, $3)
		RETURNING id
	`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		pool.Exec(cleanupCtx, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(cleanupCtx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		pool.Exec(cleanupCtx, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
		pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id = $1`, userID)
	})

	identityClient := &fakeAgentIdentityContextCreator{
		result: agentidentityhsf.CreateContextResult{ContextToken: "ctx_from_agent_binding"},
	}
	launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{LLMModels: []string{"qwen3.5-plus"}}, nil)
	launcher.AgentIdentity = identityClient
	runtime, err := queries.GetAgentRuntime(ctx, util.MustParseUUID(runtimeID))
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	task := db.AgentTaskQueue{
		ID:            util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:       util.MustParseUUID(agentID),
		ChatSessionID: util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		Context:       []byte(`{"agent_identity_context_token":"caller_token_must_be_ignored"}`),
	}
	env, err := launcher.extraEnvForTask(ctx, task, runtime, "sbx-no-identity")
	if err != nil {
		t.Fatalf("extraEnvForTask returned error: %v", err)
	}
	if !reflect.DeepEqual(env, map[string]string{"OPENAI_MODEL": "qwen3.5-plus"}) {
		t.Fatalf("extra env = %#v, want default FC model only", env)
	}
	if len(identityClient.requests) != 0 {
		t.Fatalf("unbound chat made Agent Identity requests: %#v", identityClient.requests)
	}

	if _, err := pool.Exec(ctx, `UPDATE agent SET model = 'qwen3.7-plus' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("save selected FC model: %v", err)
	}
	launcher.Config.LLMModels = []string{"qwen3.5-plus", "qwen3.7-plus"}
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-no-identity")
	if err != nil {
		t.Fatalf("extraEnvForTask with selected model returned error: %v", err)
	}
	if !reflect.DeepEqual(env, map[string]string{"OPENAI_MODEL": "qwen3.7-plus"}) {
		t.Fatalf("extra env = %#v, want selected FC model", env)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_dingtalk_identity (
			agent_id, workspace_id, dws_uid, org_id, account_display_name, bound_by
		) VALUES ($1, $2, '24710833', '439446171', 'Xu Mo', $3)
	`, agentID, workspaceID, userID); err != nil {
		t.Fatalf("bind Agent DingTalk identity: %v", err)
	}
	launcher.Config.AgentIdentityBaseURL = "https://pre-agent-identity.dingtalk.com"
	launcher.Config.AgentIdentityTimeout = 2 * time.Second
	launcher.Config.DWSClientSecret = "dws-client-secret"
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-chat")
	if err != nil {
		t.Fatalf("extraEnvForTask with bound identity returned error: %v", err)
	}
	if env["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_from_agent_binding" ||
		env["AGENT_IDENTITY_CONTEXT_TOKEN"] == "caller_token_must_be_ignored" {
		t.Fatalf("chat ContextToken env = %#v", env)
	}
	if _, present := env["DWS_UID"]; present {
		t.Fatalf("chat must not inject a legacy DWS_UID: %#v", env)
	}
	if len(identityClient.requests) != 1 {
		t.Fatalf("Agent Identity requests = %d, want 1", len(identityClient.requests))
	}
	request := identityClient.requests[0]
	if request.UID != "24710833" || request.OrgID != "439446171" || request.TTLSeconds != 900 ||
		request.RuntimeID != "sbx-chat" || request.TaskID != util.UUIDToString(task.ID) ||
		request.Source["chat_session_id"] != util.UUIDToString(task.ChatSessionID) {
		t.Fatalf("Agent Identity request = %#v", request)
	}

	identityClient.err = errors.New("HSF unavailable")
	if _, err := launcher.extraEnvForTask(ctx, task, runtime, "sbx-chat"); err == nil {
		t.Fatal("bound chat must fail when Agent Identity context creation fails")
	}

	identityClient.err = nil
	requestCount := len(identityClient.requests)
	runtime.Metadata = []byte(`{"kind":"fc-e2b","capabilities":["hermes"]}`)
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-no-dws")
	if err != nil {
		t.Fatalf("non-DWS runtime chat returned error: %v", err)
	}
	if len(identityClient.requests) != requestCount || !reflect.DeepEqual(env, map[string]string{"OPENAI_MODEL": "qwen3.7-plus"}) {
		t.Fatalf("non-DWS runtime used Agent Identity: requests=%d env=%#v", len(identityClient.requests), env)
	}
}

func TestParseFCE2BModels(t *testing.T) {
	models, err := parseFCE2BModels(`[" qwen3.7-plus ","claude-sonnet-4-6"]`)
	if err != nil {
		t.Fatalf("parseFCE2BModels returned error: %v", err)
	}
	if !reflect.DeepEqual(models, []string{"qwen3.7-plus", "claude-sonnet-4-6"}) {
		t.Fatalf("models = %#v", models)
	}
	for _, raw := range []string{
		`"qwen3.7-plus"`,
		`[]`,
		`["qwen3.7-plus", "qwen3.7-plus"]`,
		`["qwen3.7-plus", ""]`,
	} {
		if _, err := parseFCE2BModels(raw); err == nil {
			t.Fatalf("parseFCE2BModels(%s) must fail", raw)
		}
	}
}

func TestChatDWSIdentityAcceptsExplicitUnavailableSender(t *testing.T) {
	launcher := &FCE2BLauncher{}
	task := db.AgentTaskQueue{
		ChatSessionID: util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		Context:       []byte(`{"dingtalk_robot_identity_unavailable":{"reason":"missing_organization_identity"}}`),
	}
	runtime := db.AgentRuntime{Metadata: []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`)}
	env, err := launcher.chatDWSIdentityEnv(context.Background(), task, runtime, "sandbox-external-sender")
	if err != nil {
		t.Fatalf("chatDWSIdentityEnv: %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("identity-less sender must start without DWS env: %#v", env)
	}
	uid, orgID, source, err := launcher.chatDWSIdentity(context.Background(), task, runtime)
	if err != nil {
		t.Fatalf("chatDWSIdentity: %v", err)
	}
	if uid != "" || orgID != "" || source != "dingtalk_robot_sender_unavailable" {
		t.Fatalf("identity = uid %q org %q source %q", uid, orgID, source)
	}
}

func TestChatDWSIdentityRejectsMalformedUnavailableMarker(t *testing.T) {
	launcher := &FCE2BLauncher{}
	task := db.AgentTaskQueue{Context: []byte(`{"dingtalk_robot_identity_unavailable":{}}`)}
	if _, _, _, err := launcher.chatDWSIdentity(context.Background(), task, db.AgentRuntime{}); err == nil {
		t.Fatal("chatDWSIdentity must reject a marker without a reason")
	}
}

func TestFCE2BModelForAgent(t *testing.T) {
	cfg := FCE2BConfig{LLMModels: []string{"qwen3.7-plus", "qwen3.7-max"}}
	if got, err := cfg.ModelForAgent(""); err != nil || got != "qwen3.7-plus" {
		t.Fatalf("default model = %q, %v", got, err)
	}
	if got, err := cfg.ModelForAgent("qwen3.7-max"); err != nil || got != "qwen3.7-max" {
		t.Fatalf("selected model = %q, %v", got, err)
	}
	if _, err := cfg.ModelForAgent("unknown-model"); err == nil {
		t.Fatal("unknown agent model must fail")
	}
}

func TestFCE2BExtraEnvIncludesAgentIdentityContextToken(t *testing.T) {
	task := db.AgentTaskQueue{
		Context: []byte(`{"agent_identity_context_token":"ctx_sandbox_token"}`),
	}
	got, err := fcE2BAgentIdentityExtraEnv(task, FCE2BConfig{
		AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
		AgentIdentityTimeout: 7 * time.Second,
		DWSClientSecret:      "dws-client-secret",
	})
	if err != nil {
		t.Fatalf("fcE2BAgentIdentityExtraEnv: %v", err)
	}
	if got["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_sandbox_token" {
		t.Fatalf("AGENT_IDENTITY_CONTEXT_TOKEN = %q, want ctx_sandbox_token", got["AGENT_IDENTITY_CONTEXT_TOKEN"])
	}
	if got["MULTICA_AGENT_IDENTITY_BASE_URL"] != "https://pre-agent-identity.dingtalk.com" {
		t.Fatalf("MULTICA_AGENT_IDENTITY_BASE_URL = %q", got["MULTICA_AGENT_IDENTITY_BASE_URL"])
	}
	if got["MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS"] != "7" {
		t.Fatalf("MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS = %q", got["MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS"])
	}
	if got["DWS_CLIENT_SECRET"] != "dws-client-secret" {
		t.Fatal("DWS_CLIENT_SECRET was not forwarded")
	}
}

func TestSandboxSourceEnvCarriesLauncherAndDingTalkStreamHosts(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	taskContext, err := json.Marshal(map[string]any{
		protocol.DingTalkStreamSourceJSONKey: protocol.DingTalkStreamSource{
			Hostname:     "dt-fde-multica033008056137.pre.na620",
			NodeID:       "node-a",
			ConnectionID: "node-a-g3",
		},
	})
	if err != nil {
		t.Fatalf("marshal task context: %v", err)
	}
	env, err := sandboxSourceEnv(taskContext)
	if err != nil {
		t.Fatalf("sandboxSourceEnv: %v", err)
	}
	if env[protocol.SandboxSourceHostnameEnvKey] != hostname {
		t.Fatalf("sandbox source hostname = %q, want %q", env[protocol.SandboxSourceHostnameEnvKey], hostname)
	}
	if env[protocol.DingTalkStreamHostnameEnvKey] != "dt-fde-multica033008056137.pre.na620" ||
		env[protocol.DingTalkStreamNodeIDEnvKey] != "node-a" ||
		env[protocol.DingTalkStreamConnectionIDEnvKey] != "node-a-g3" {
		t.Fatalf("DingTalk Stream env = %#v", env)
	}
}

func TestSandboxSourceEnvRejectsPartialDingTalkStreamSource(t *testing.T) {
	_, err := sandboxSourceEnv([]byte(`{"dingtalk_stream_source":{"hostname":"stream-host"}}`))
	if err == nil {
		t.Fatal("sandboxSourceEnv accepted a partial DingTalk Stream source")
	}
}

func TestFCE2BConfigFromEnvAgentIdentity(t *testing.T) {
	t.Setenv("MULTICA_AGENT_IDENTITY_BASE_URL", "https://pre-agent-identity.dingtalk.com/")
	t.Setenv("MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS", "7")
	t.Setenv("MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET", "dws-client-secret")
	cfg := FCE2BConfigFromEnv()
	if cfg.AgentIdentityBaseURL != "https://pre-agent-identity.dingtalk.com" {
		t.Fatalf("base url = %q", cfg.AgentIdentityBaseURL)
	}
	if cfg.AgentIdentityTimeout != 7*time.Second {
		t.Fatalf("timeout = %s", cfg.AgentIdentityTimeout)
	}
	if cfg.DWSClientSecret != "dws-client-secret" {
		t.Fatal("DWS client secret was not loaded")
	}
}

func TestFCE2BScopeForTask(t *testing.T) {
	chatID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	issueID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")

	scope, ok := fcE2BScopeForTask(db.AgentTaskQueue{ChatSessionID: chatID, IssueID: issueID})
	if !ok || scope.typ != fcE2BScopeTypeChat || scope.id != chatID {
		t.Fatalf("chat scope = (%+v, %v), want chat %s", scope, ok, util.UUIDToString(chatID))
	}

	scope, ok = fcE2BScopeForTask(db.AgentTaskQueue{IssueID: issueID})
	if !ok || scope.typ != fcE2BScopeTypeIssue || scope.id != issueID {
		t.Fatalf("issue scope = (%+v, %v), want issue %s", scope, ok, util.UUIDToString(issueID))
	}

	if _, ok := fcE2BScopeForTask(db.AgentTaskQueue{}); ok {
		t.Fatal("task without chat or issue scope must not be scoped")
	}
}

func TestFCE2BTaskHasActiveBlocker(t *testing.T) {
	agentID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	targetID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	activeID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	otherID := util.MustParseUUID("44444444-4444-4444-4444-444444444444")
	chatID := util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	issueID := util.MustParseUUID("66666666-6666-6666-6666-666666666666")

	target := db.AgentTaskQueue{
		ID:            targetID,
		AgentID:       agentID,
		Status:        "queued",
		ChatSessionID: chatID,
	}
	if !fcE2BTaskHasActiveBlocker(target, []db.AgentTaskQueue{{
		ID:            activeID,
		AgentID:       agentID,
		Status:        "running",
		ChatSessionID: chatID,
	}}) {
		t.Fatal("expected active task in same chat to block target claim")
	}
	if fcE2BTaskHasActiveBlocker(target, []db.AgentTaskQueue{{
		ID:            activeID,
		AgentID:       agentID,
		Status:        "completed",
		ChatSessionID: chatID,
	}}) {
		t.Fatal("completed task must not block target claim")
	}
	if fcE2BTaskHasActiveBlocker(target, []db.AgentTaskQueue{{
		ID:            activeID,
		AgentID:       agentID,
		Status:        "queued",
		ChatSessionID: chatID,
	}}) {
		t.Fatal("queued task must not be treated as an active blocker")
	}
	if fcE2BTaskHasActiveBlocker(target, []db.AgentTaskQueue{{
		ID:            activeID,
		AgentID:       agentID,
		Status:        "running",
		ChatSessionID: otherID,
	}}) {
		t.Fatal("different chat must not block target claim")
	}

	issueTarget := db.AgentTaskQueue{
		ID:      targetID,
		AgentID: agentID,
		Status:  "queued",
		IssueID: issueID,
	}
	if !fcE2BTaskHasActiveBlocker(issueTarget, []db.AgentTaskQueue{{
		ID:      activeID,
		AgentID: agentID,
		Status:  "dispatched",
		IssueID: issueID,
	}}) {
		t.Fatal("active task in same issue must block target claim")
	}
	if fcE2BTaskHasActiveBlocker(issueTarget, []db.AgentTaskQueue{{
		ID:      activeID,
		AgentID: otherID,
		Status:  "running",
		IssueID: issueID,
	}}) {
		t.Fatal("different agent must not block target claim")
	}
}

func TestIsFCE2BRuntime(t *testing.T) {
	metadata, err := json.Marshal(map[string]any{"kind": FCE2BMetadataKind})
	if err != nil {
		t.Fatal(err)
	}
	if !IsFCE2BRuntime(db.AgentRuntime{RuntimeMode: "cloud", Metadata: metadata}) {
		t.Fatal("expected cloud runtime with fc-e2b metadata to match")
	}
	if IsFCE2BRuntime(db.AgentRuntime{RuntimeMode: "local", Metadata: metadata}) {
		t.Fatal("local runtime must not match")
	}
	if IsFCE2BRuntime(db.AgentRuntime{RuntimeMode: "cloud", Metadata: []byte(`{"kind":"other"}`)}) {
		t.Fatal("other cloud runtime must not match")
	}
}
