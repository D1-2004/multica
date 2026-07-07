package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeCommandRunner struct {
	calls []fakeCommandCall
	out   []string
}

type fakeCommandCall struct {
	name string
	args []string
	env  []string
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args []string, env []string) (string, error) {
	f.calls = append(f.calls, fakeCommandCall{
		name: name,
		args: append([]string(nil), args...),
		env:  append([]string(nil), env...),
	})
	if len(f.out) == 0 {
		return "", nil
	}
	out := f.out[0]
	f.out = f.out[1:]
	return out, nil
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
		LLMBaseURL:          "https://maas-api.alibaba-inc.com/v1",
		LLMAPIKey:           "maas_secret",
		LLMModel:            "qwen3.7-max",
		CLIPath:             "/usr/local/bin/e2b",
		TimeoutSeconds:      1800,
		SandboxReadyTimeout: time.Second,
	}, runner)

	sandboxID, err := launcher.createSandbox(context.Background())
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
	if err := launcher.execRunOnce(context.Background(), sandboxID, rt, "mdt_test_token"); err != nil {
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
		"-e", "MULTICA_DAEMON_ID=fc-e2b:ws:fc-hermes",
		"-e", "MULTICA_AGENT_RUNTIME_NAME=FC-Hermes",
		"-e", "OPENAI_BASE_URL=https://maas-api.alibaba-inc.com/v1",
		"-e", "OPENAI_API_KEY=maas_secret",
		"-e", "OPENAI_MODEL=qwen3.7-max",
		"sbx_123",
		"multica-fc-hermes-runner",
		"--runtime-id", "11111111-1111-1111-1111-111111111111",
		"--provider", "hermes",
	}
	if !reflect.DeepEqual(runner.calls[2].args, wantExecArgs) {
		t.Fatalf("exec args = %#v, want %#v", runner.calls[2].args, wantExecArgs)
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
