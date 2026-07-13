package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
		LLMModel:            "qwen3.5-plus",
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
	if err := launcher.execRunOnce(context.Background(), sandboxID, rt, taskID, "mdt_test_token", true, nil); err != nil {
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
		"-e", "OPENAI_MODEL=qwen3.5-plus",
		"-e", "MULTICA_FC_E2B_COLD_START=true",
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
		LLMModel:   "qwen3.5-plus",
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
		LLMModel:   "qwen3.5-plus",
		CLIPath:    "/usr/local/bin/e2b",
	}, runner)
	rt := db.AgentRuntime{
		ID:       util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Name:     "FC-Hermes-DWS",
		DaemonID: pgtype.Text{String: "fc-e2b:ws:fc-hermes-dws", Valid: true},
	}

	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if err := launcher.execRunOnce(context.Background(), "sbx_dws", rt, taskID, "mdt_test_token", false, map[string]string{
		"DWS_AUTH_ARCHIVE_B64": "archive_secret",
	}); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	args := runner.calls[0].args
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && args[i+1] == "DWS_AUTH_ARCHIVE_B64=archive_secret" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("exec args did not include DWS auth env")
	}
}

func TestFCE2BExtraEnvIncludesAgentIdentityContextToken(t *testing.T) {
	task := db.AgentTaskQueue{
		Context: []byte(`{"agent_identity_context_token":"ctx_sandbox_token"}`),
	}
	got := fcE2BAgentIdentityExtraEnv(task)
	if got["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_sandbox_token" {
		t.Fatalf("AGENT_IDENTITY_CONTEXT_TOKEN = %q, want ctx_sandbox_token", got["AGENT_IDENTITY_CONTEXT_TOKEN"])
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
