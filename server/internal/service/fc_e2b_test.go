package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattrace"
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

type fakeAgentIdentityBindingReader struct {
	identity db.AgentDingtalkIdentity
	err      error
	requests []db.GetAgentDingTalkIdentityParams
}

func (f *fakeAgentIdentityBindingReader) GetAgentDingTalkIdentity(_ context.Context, request db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	f.requests = append(f.requests, request)
	return f.identity, f.err
}

type fakeCommandRunner struct {
	calls     []fakeCommandCall
	deadlines []bool
	out       []string
	errs      []error
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
	var out string
	if len(f.out) > 0 {
		out = f.out[0]
		f.out = f.out[1:]
	}
	var err error
	if len(f.errs) > 0 {
		err = f.errs[0]
		f.errs = f.errs[1:]
	}
	return out, err
}

func mustFCE2BRunnerLaunch(t *testing.T, rt db.AgentRuntime) fcE2BRunnerLaunch {
	t.Helper()
	launch, err := fcE2BRunnerLaunchForRuntime(rt)
	if err != nil {
		t.Fatalf("resolve runner launch: %v", err)
	}
	return launch
}

func TestFCE2BProviderForTemplate(t *testing.T) {
	cases := []struct {
		providers []string
		want      string
		ok        bool
	}{
		{[]string{"hermes", "opencode", "pi"}, "hermes", true},
		{[]string{"opencode", "pi"}, "opencode", true},
		{[]string{"pi"}, "pi", true},
		{[]string{"unknown"}, "", false},
		{nil, "", false},
	}
	for _, tc := range cases {
		got, ok := FCE2BProviderForTemplate(FCE2BTemplate{Providers: tc.providers})
		if got != tc.want || ok != tc.ok {
			t.Fatalf("FCE2BProviderForTemplate(%v) = (%q, %v), want (%q, %v)", tc.providers, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIsFCE2BSupportedProvider(t *testing.T) {
	for _, provider := range []string{"hermes", "opencode", "pi", " Hermes ", "OPENCODE", " PI "} {
		if !IsFCE2BSupportedProvider(provider) {
			t.Fatalf("IsFCE2BSupportedProvider(%q) = false, want true", provider)
		}
	}
	for _, provider := range []string{"", "codex", "claude"} {
		if IsFCE2BSupportedProvider(provider) {
			t.Fatalf("IsFCE2BSupportedProvider(%q) = true, want false", provider)
		}
	}
}

func TestFCE2BRunnerCommandForProvider(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"hermes", "multica-fc-hermes-container-log-entry"},
		{"opencode", "multica-fc-opencode-container-log-entry"},
		{"pi", "multica-fc-pi-container-log-entry"},
		{" OpenCode ", "multica-fc-opencode-container-log-entry"},
		{"", "multica-fc-hermes-container-log-entry"},
	}
	for _, tc := range cases {
		if got := FCE2BRunnerCommandForProvider(tc.provider); got != tc.want {
			t.Fatalf("FCE2BRunnerCommandForProvider(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestFCE2BRuntimeProvider(t *testing.T) {
	legacy := db.AgentRuntime{Provider: ""}
	if got := FCE2BRuntimeProvider(legacy); got != "hermes" {
		t.Fatalf("legacy runtime provider = %q, want hermes", got)
	}

	opencode := db.AgentRuntime{Provider: "opencode"}
	if got := FCE2BRuntimeProvider(opencode); got != "opencode" {
		t.Fatalf("opencode runtime provider = %q, want opencode", got)
	}
}

func TestLegacyFCE2BRuntimeRemainsStableManagedAndLaunchable(t *testing.T) {
	runtime := db.AgentRuntime{
		RuntimeMode: "cloud",
		Provider:    "hermes",
		Metadata: []byte(`{
			"kind":"fc-e2b",
			"template":"multica-m1-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdi-r1-aaaaaa",
			"template_id":"legacy-template",
			"runner":"multica-fc-hermes-runner"
		}`),
	}
	if channel := FCE2BRuntimeTemplateChannel(runtime); channel != "stable" {
		t.Fatalf("legacy runtime channel = %q, want stable", channel)
	}
	launch, err := fcE2BRunnerLaunchForRuntime(runtime)
	if err != nil {
		t.Fatalf("legacy runtime launch contract rejected: %v", err)
	}
	if launch.Mode != fcE2BRunnerLaunchLegacyUser ||
		launch.Command != "/usr/local/bin/multica-fc-hermes-runner" ||
		launch.Home != "/home/user" {
		t.Fatalf("legacy runtime launch = %#v", launch)
	}
}

func TestFCE2BRunnerLaunchForRuntime(t *testing.T) {
	legacyHermes := fcE2BRunnerLaunch{
		Mode:    fcE2BRunnerLaunchLegacyUser,
		Command: "/usr/local/bin/multica-fc-hermes-runner",
		Home:    "/home/user",
	}
	legacyOpenCode := fcE2BRunnerLaunch{
		Mode:    fcE2BRunnerLaunchLegacyUser,
		Command: "/usr/local/bin/multica-fc-opencode-runner",
		Home:    "/home/user",
	}
	rootHermes := fcE2BRunnerLaunch{
		Mode:    fcE2BRunnerLaunchRootLog,
		Command: "/usr/local/libexec/multica-fc-hermes-container-log-entry",
		Home:    "/root",
	}
	rootOpenCode := fcE2BRunnerLaunch{
		Mode:    fcE2BRunnerLaunchRootLog,
		Command: "/usr/local/libexec/multica-fc-opencode-container-log-entry",
		Home:    "/root",
	}
	rootPi := fcE2BRunnerLaunch{
		Mode:    fcE2BRunnerLaunchRootLog,
		Command: "/usr/local/libexec/multica-fc-pi-container-log-entry",
		Home:    "/root",
	}

	cases := []struct {
		name     string
		provider string
		metadata string
		want     fcE2BRunnerLaunch
		wantErr  bool
	}{
		{name: "pre-provider legacy row", want: legacyHermes},
		{name: "legacy Hermes", provider: "hermes", metadata: `{"runner":"multica-fc-hermes-runner"}`, want: legacyHermes},
		{name: "legacy OpenCode", provider: "opencode", metadata: `{"runner":"multica-fc-opencode-runner"}`, want: legacyOpenCode},
		{name: "root Hermes", provider: "hermes", metadata: `{"runner":"multica-fc-hermes-container-log-entry"}`, want: rootHermes},
		{name: "root OpenCode", provider: "opencode", metadata: `{"runner":"multica-fc-opencode-container-log-entry"}`, want: rootOpenCode},
		{name: "root Pi", provider: "pi", metadata: `{"runner":"multica-fc-pi-container-log-entry"}`, want: rootPi},
		{name: "Pi has no legacy runner", provider: "pi", metadata: `{}`, wantErr: true},
		{name: "provider mismatch", provider: "hermes", metadata: `{"runner":"multica-fc-opencode-container-log-entry"}`, wantErr: true},
		{name: "custom command", provider: "opencode", metadata: `{"runner":"/tmp/custom-runner"}`, wantErr: true},
		{name: "shell command", provider: "hermes", metadata: `{"runner":"sh -c id"}`, wantErr: true},
		{name: "whitespace marker", provider: "hermes", metadata: `{"runner":" multica-fc-hermes-runner "}`, wantErr: true},
		{name: "empty marker", provider: "hermes", metadata: `{"runner":""}`, wantErr: true},
		{name: "null marker", provider: "hermes", metadata: `{"runner":null}`, wantErr: true},
		{name: "non-string marker", provider: "hermes", metadata: `{"runner":42}`, wantErr: true},
		{name: "invalid metadata", provider: "hermes", metadata: `{`, wantErr: true},
		{name: "unsupported provider", provider: "custom", metadata: `{}`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := db.AgentRuntime{Provider: tc.provider}
			if tc.metadata != "" {
				rt.Metadata = []byte(tc.metadata)
			}
			got, err := fcE2BRunnerLaunchForRuntime(rt)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("launch = %#v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve launch: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("launch = %#v, want %#v", got, tc.want)
			}
		})
	}
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

func TestParseFCE2BTemplatesUsesVersionedManifestAlias(t *testing.T) {
	const alias = "multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"
	got, err := parseFCE2BTemplates(`[
		{
			"templateID": "idt7f6on323gsyuqjt59",
			"buildID": "a4aa129e-ef89-4fce-9fc9-605a1015e0e1",
			"aliases": ["default", "multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"],
			"names": ["multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"],
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
	if got[0].BuildID != "a4aa129e-ef89-4fce-9fc9-605a1015e0e1" {
		t.Fatalf("build_id = %q", got[0].BuildID)
	}
	if got[0].Name != alias {
		t.Fatalf("name = %q", got[0].Name)
	}
	if got[0].Template != alias {
		t.Fatalf("template = %q", got[0].Template)
	}
	if got[0].Status != "ready" {
		t.Fatalf("status = %q", got[0].Status)
	}
	if got[0].UpdatedAt != "2026-07-08T13:19:01.365773Z" {
		t.Fatalf("updated_at = %q", got[0].UpdatedAt)
	}
	if !IsFCE2BTemplatePublished(got[0]) {
		t.Fatalf("template manifest was not published: %+v", got[0])
	}
	if want := []string{"hermes", "opencode", "pi"}; !reflect.DeepEqual(got[0].Providers, want) {
		t.Fatalf("providers = %#v, want %#v", got[0].Providers, want)
	}
	if want := []string{"dws", "dws.im_event", "mcp"}; !reflect.DeepEqual(got[0].Capabilities, want) {
		t.Fatalf("capabilities = %#v, want %#v", got[0].Capabilities, want)
	}
	if want := map[string]string{
		"hermes": "0.19.0", "opencode": "v1.18.4", "pi": "0.80.10", "dws": "v1.0.53-beta.4",
	}; !reflect.DeepEqual(got[0].ComponentVersions, want) {
		t.Fatalf("component versions = %#v, want %#v", got[0].ComponentVersions, want)
	}
	if got[0].SourceRevision != "9a6bfa" {
		t.Fatalf("source revision = %q, want 9a6bfa", got[0].SourceRevision)
	}
}

func TestApplyFCE2BTemplateManifestAliasIsStrict(t *testing.T) {
	const validAlias = "multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"
	tests := []struct {
		name          string
		buildID       string
		alias         string
		wantApplied   bool
		wantPublished bool
	}{
		{name: "valid current", buildID: "build-current", alias: validAlias, wantApplied: true, wantPublished: true},
		{name: "valid legacy", buildID: "build-legacy", alias: "multica-m1-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdi-r1-aaaaaa", wantApplied: true},
		{name: "missing build ID", alias: validAlias},
		{name: "old template name", buildID: "build-current", alias: "multica-fc-hermes-opencode-dws-v1"},
		{name: "missing patch version", buildID: "build-current", alias: "multica-m2-h0_19-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"},
		{name: "leading zero", buildID: "build-current", alias: "multica-m2-h00_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"},
		{name: "current missing MCP marker", buildID: "build-current", alias: "multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdi-r1-9a6bfa"},
		{name: "legacy falsely claims MCP", buildID: "build-current", alias: "multica-m1-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"},
		{name: "wrong runner", buildID: "build-current", alias: "multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r2-9a6bfa"},
		{name: "uppercase SHA", buildID: "build-current", alias: "multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9A6BFA"},
		{name: "suffix", buildID: "build-current", alias: validAlias + "-extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			template := FCE2BTemplate{BuildID: test.buildID}
			published, err := applyFCE2BTemplateManifestAlias(&template, test.alias)
			if err != nil {
				t.Fatalf("apply manifest alias: %v", err)
			}
			if published != test.wantApplied {
				t.Fatalf("applied = %v, want %v", published, test.wantApplied)
			}
			if got := IsFCE2BTemplatePublished(template); got != test.wantPublished {
				t.Fatalf("IsFCE2BTemplatePublished = %v, want %v: %+v", got, test.wantPublished, template)
			}
		})
	}
	if _, err := applyFCE2BTemplateManifestAlias(nil, validAlias); err == nil {
		t.Fatal("nil template was accepted")
	}
}

func TestListFCE2BTemplatesReadsOnlyManifestAliases(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{`[
		{"id":"tpl-older","buildID":"build-older","aliases":["multica-m1-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdi-r1-aaaaaa"],"status":"ready","updatedAt":"2026-07-20T01:00:00Z"},
		{"id":"tpl-current","buildID":"build-current","aliases":["default","multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"],"names":["multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-9a6bfa"],"status":"ready","updatedAt":"2026-07-21T01:00:00Z"},
		{"id":"tpl-old","buildID":"build-old","aliases":["multica-fc-hermes-opencode-dws-v1"],"status":"ready"},
		{"id":"tpl-malformed","buildID":"build-malformed","aliases":["multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdi-r1-9a6bfa"],"status":"ready"},
		{"id":"tpl-no-build","aliases":["multica-m2-h0_19_0-o1_18_4-p0_80_10-d1_0_53b4-cdim-r1-bbbbbb"],"status":"ready"}
	]`}}
	templates, err := ListFCE2BTemplates(context.Background(), FCE2BConfig{
		APIKey:  "test-key",
		APIURL:  "https://fc-e2b.test",
		Domain:  "fc-e2b.test",
		CLIPath: "e2b-test",
	}, runner)
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("templates = %#v", templates)
	}
	got := templates[0]
	if got.ID != "tpl-current" || got.BuildID != "build-current" || got.ManifestVersion != 2 {
		t.Fatalf("verified template = %#v", got)
	}
	if want := []string{"hermes", "opencode", "pi"}; !reflect.DeepEqual(got.Providers, want) {
		t.Fatalf("providers = %#v, want %#v", got.Providers, want)
	}
	if got.ComponentVersions["pi"] != "0.80.10" || got.ComponentVersions["dws"] != "v1.0.53-beta.4" {
		t.Fatalf("component versions = %#v", got.ComponentVersions)
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].args, []string{"template", "list", "--format", "json"}) {
		t.Fatalf("template list calls = %#v", runner.calls)
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
		Metadata: []byte(`{"runner":"multica-fc-hermes-container-log-entry"}`),
	}
	launch, err := launcher.detectFCE2BRunnerLaunch(context.Background(), sandboxID, rt)
	if err != nil {
		t.Fatalf("detectFCE2BRunnerLaunch returned error: %v", err)
	}
	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if err := launcher.execRunOnce(context.Background(), sandboxID, rt, launch.Mode, taskID, "mdt_test_token", true, map[string]string{
		"OPENAI_MODEL": "qwen3.7-max",
	}); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}

	if got := len(runner.calls); got != 4 {
		t.Fatalf("runner calls = %d, want 4", got)
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
	wantProbeArgs := []string{
		"sandbox", "exec",
		"--user", "user",
		"sbx_123",
		"--",
		"/usr/bin/test", "-x", "/usr/local/libexec/multica-fc-hermes-container-log-entry",
	}
	if !reflect.DeepEqual(runner.calls[2].args, wantProbeArgs) {
		t.Fatalf("probe args = %#v, want %#v", runner.calls[2].args, wantProbeArgs)
	}
	wantExecArgs := []string{
		"sandbox", "exec",
		"--background",
		"--user", "root",
		"-e", "LD_PRELOAD=",
		"-e", "LD_LIBRARY_PATH=",
		"-e", "LD_AUDIT=",
		"-e", "GCONV_PATH=",
		"-e", "BASH_ENV=",
		"-e", "ENV=",
		"-e", "MULTICA_SERVER_URL=https://api.multica.test",
		"-e", "MULTICA_DAEMON_TOKEN=mdt_test_token",
		"-e", "MULTICA_RUNTIME_ID=11111111-1111-1111-1111-111111111111",
		"-e", "MULTICA_TASK_ID=22222222-2222-2222-2222-222222222222",
		"-e", "MULTICA_DAEMON_ID=fc-e2b:ws:fc-hermes",
		"-e", "MULTICA_AGENT_RUNTIME_NAME=FC-Hermes",
		"-e", "HOME=/root",
		"-e", "DWS_CONFIG_DIR=/home/user/.dws",
		"-e", "OPENAI_BASE_URL=https://api-deap.dingtalk.com/deapai",
		"-e", "OPENAI_API_KEY=maas_secret",
		"-e", "MULTICA_FC_E2B_COLD_START=true",
		"-e", "OPENAI_MODEL=qwen3.7-max",
		"sbx_123",
		"--",
		"/usr/local/libexec/multica-fc-hermes-container-log-entry",
		"--runtime-id", "11111111-1111-1111-1111-111111111111",
		"--provider", "hermes",
		"--health-port", strconv.Itoa(fcE2BHealthPortForTask(taskID)),
	}
	if !reflect.DeepEqual(runner.calls[3].args, wantExecArgs) {
		t.Fatalf("exec args = %#v, want %#v", runner.calls[3].args, wantExecArgs)
	}
}

func TestFCE2BExecRunOnceUsesRootLogProtocol(t *testing.T) {
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
		ID:          util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Name:        "FC-Opencode",
		RuntimeMode: "cloud",
		Provider:    "opencode",
		DaemonID:    pgtype.Text{String: "fc-e2b:ws:fc-opencode", Valid: true},
		Metadata:    []byte(`{"kind":"fc-e2b","template":"multica-fc-opencode-v1","runner":"multica-fc-opencode-container-log-entry"}`),
	}

	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	launch := mustFCE2BRunnerLaunch(t, rt)
	if err := launcher.execRunOnce(context.Background(), "sbx_oc", rt, launch.Mode, taskID, "mdt_test_token", false, nil); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want one exec", len(runner.calls))
	}
	args := runner.calls[0].args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--user root") {
		t.Fatalf("exec args must start the fixed wrapper as root: %#v", args)
	}
	if !strings.Contains(joined, "-- /usr/local/libexec/multica-fc-opencode-container-log-entry ") {
		t.Fatalf("exec args must invoke the opencode runner: %#v", args)
	}
	if !strings.Contains(joined, "--provider opencode") {
		t.Fatalf("exec args must pass the runtime provider: %#v", args)
	}
	if strings.Contains(joined, "hermes") {
		t.Fatalf("opencode runtime exec must not mention hermes: %#v", args)
	}
	for _, expected := range []string{
		"HOME=/root",
		"LD_PRELOAD=",
		"LD_LIBRARY_PATH=",
		"LD_AUDIT=",
		"GCONV_PATH=",
		"BASH_ENV=",
		"ENV=",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("root exec args missing sanitized environment %q: %#v", expected, args)
		}
	}
}

func TestFCE2BExecRunOnceUsesLegacyUserProtocol(t *testing.T) {
	runner := &fakeCommandRunner{errs: []error{errors.New("root entrypoint is absent"), nil, nil}}
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
		ID:          util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Name:        "FC-Hermes-Legacy",
		RuntimeMode: "cloud",
		Provider:    "hermes",
		DaemonID:    pgtype.Text{String: "fc-e2b:ws:fc-hermes-legacy", Valid: true},
		Metadata:    []byte(`{"kind":"fc-e2b","runner":"multica-fc-hermes-container-log-entry"}`),
	}

	launch, err := launcher.detectFCE2BRunnerLaunch(context.Background(), "sbx_legacy", rt)
	if err != nil {
		t.Fatalf("detect legacy runner: %v", err)
	}
	if launch.Mode != fcE2BRunnerLaunchLegacyUser {
		t.Fatalf("detected launch = %#v, want legacy user protocol", launch)
	}
	wantRootProbe := []string{
		"sandbox", "exec", "--user", "user", "sbx_legacy", "--",
		"/usr/bin/test", "-x", "/usr/local/libexec/multica-fc-hermes-container-log-entry",
	}
	wantLegacyProbe := []string{
		"sandbox", "exec", "--user", "user", "sbx_legacy", "--",
		"/usr/bin/test", "-x", "/usr/local/bin/multica-fc-hermes-runner",
	}
	if !reflect.DeepEqual(runner.calls[0].args, wantRootProbe) || !reflect.DeepEqual(runner.calls[1].args, wantLegacyProbe) {
		t.Fatalf("probe calls = %#v, want root then legacy probes", runner.calls)
	}

	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	if err := launcher.execRunOnce(context.Background(), "sbx_legacy", rt, launch.Mode, taskID, "mdt_test_token", true, nil); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %d, want root probe, legacy probe, and exec", len(runner.calls))
	}
	joined := strings.Join(runner.calls[2].args, " ")
	if strings.Contains(joined, "--user root") {
		t.Fatalf("legacy exec must use the sandbox user: %#v", runner.calls[2].args)
	}
	if !strings.Contains(joined, "HOME=/home/user") {
		t.Fatalf("legacy exec must preserve the user home: %#v", runner.calls[2].args)
	}
	if !strings.Contains(joined, "-- /usr/local/bin/multica-fc-hermes-runner ") {
		t.Fatalf("legacy exec must use the fixed legacy path: %#v", runner.calls[2].args)
	}
	for _, rootOnly := range []string{"LD_PRELOAD=", "LD_LIBRARY_PATH=", "LD_AUDIT=", "GCONV_PATH=", "BASH_ENV=", "ENV="} {
		if strings.Contains(joined, rootOnly) {
			t.Fatalf("legacy exec unexpectedly contains root-only environment %q: %#v", rootOnly, runner.calls[2].args)
		}
	}
}

func TestFCE2BExecRunOnceRejectsUnknownRunnerProtocolBeforeE2B(t *testing.T) {
	runner := &fakeCommandRunner{}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{}, runner)
	rt := db.AgentRuntime{
		ID:       util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Provider: "hermes",
		Metadata: []byte(`{"runner":"/tmp/untrusted; id"}`),
	}
	if _, err := launcher.detectFCE2BRunnerLaunch(context.Background(), "sbx_unknown", rt); err == nil {
		t.Fatal("unknown runner protocol unexpectedly accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unknown runner protocol reached E2B: %#v", runner.calls)
	}
}

func TestDetectFCE2BRunnerLaunchRejectsImageWithoutSupportedEntrypoint(t *testing.T) {
	runner := &fakeCommandRunner{errs: []error{
		errors.New("root entrypoint is absent"),
		errors.New("legacy entrypoint is absent"),
	}}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{CLIPath: "/usr/local/bin/e2b"}, runner)
	rt := db.AgentRuntime{
		ID:       util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Provider: "hermes",
		Metadata: []byte(`{"runner":"multica-fc-hermes-container-log-entry"}`),
	}

	if _, err := launcher.detectFCE2BRunnerLaunch(context.Background(), "sbx_missing", rt); err == nil {
		t.Fatal("image without a supported runner entrypoint unexpectedly accepted")
	} else if !strings.Contains(err.Error(), "has no executable runner entrypoint for provider hermes") {
		t.Fatalf("probe error = %q", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want exactly two foreground probes", len(runner.calls))
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.args, " "), "--background") {
			t.Fatalf("unsupported image reached background exec: %#v", call.args)
		}
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
	launch := mustFCE2BRunnerLaunch(t, rt)
	if err := launcher.execRunOnce(context.Background(), "sbx_warm", rt, launch.Mode, taskID, "mdt_test_token", false, nil); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	execArgs := runner.calls[len(runner.calls)-1].args
	for _, arg := range execArgs {
		if arg == "MULTICA_FC_E2B_COLD_START=true" {
			t.Fatalf("warm sandbox exec must not inject cold-start marker: %#v", execArgs)
		}
	}
	foundHealthPort := false
	for i := 0; i < len(execArgs)-1; i++ {
		if execArgs[i] == "--health-port" && execArgs[i+1] == strconv.Itoa(fcE2BHealthPortForTask(taskID)) {
			foundHealthPort = true
			break
		}
	}
	if !foundHealthPort {
		t.Fatalf("warm sandbox exec must pass task-specific health port: %#v", execArgs)
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
	launch := mustFCE2BRunnerLaunch(t, rt)
	if err := launcher.execRunOnce(context.Background(), "sbx_dws", rt, launch.Mode, taskID, "mdt_test_token", false, map[string]string{
		"AGENT_IDENTITY_CONTEXT_TOKEN": "context_secret",
	}); err != nil {
		t.Fatalf("execRunOnce returned error: %v", err)
	}
	args := runner.calls[len(runner.calls)-1].args
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

func TestFCE2BExecRunOnceRejectsUnsafeRunnerEnvironment(t *testing.T) {
	rt := db.AgentRuntime{
		ID:          util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		RuntimeMode: "cloud",
		Provider:    "hermes",
		DaemonID:    pgtype.Text{String: "fc-e2b:ws:fc-hermes", Valid: true},
		Metadata:    []byte(`{"kind":"fc-e2b","runner":"multica-fc-hermes-container-log-entry"}`),
	}
	launch := mustFCE2BRunnerLaunch(t, rt)
	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	for _, key := range []string{"BASH_ENV", "LD_PRELOAD", "LD_AUDIT", "PATH", "HOME", "MULTICA_RUNNER_PROVIDER"} {
		t.Run(key, func(t *testing.T) {
			runner := &fakeCommandRunner{}
			launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{}, runner)
			err := launcher.execRunOnce(context.Background(), "sbx_unsafe", rt, launch.Mode, taskID, "mdt_test_token", false, map[string]string{
				key: "/workspace/untrusted",
			})
			if err == nil {
				t.Fatal("unsafe runner environment unexpectedly accepted")
			}
			if len(runner.calls) != 0 {
				t.Fatalf("unsafe environment reached E2B command: %#v", runner.calls)
			}
		})
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
		result: agentidentityhsf.CreateContextResult{
			ContextToken: "ctx_from_agent_binding",
			ExpiresAt:    time.Now().Add(15 * time.Minute).UnixMilli(),
		},
	}
	launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{
		LLMModels:            []string{"qwen3.5-plus"},
		AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
		AgentIdentityTimeout: 2 * time.Second,
		DWSClientSecret:      "dws-client-secret",
	}, nil)
	launcher.AgentIdentity = identityClient
	runtime, err := queries.GetAgentRuntime(ctx, util.MustParseUUID(runtimeID))
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	task := db.AgentTaskQueue{
		ID:            util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID:       util.MustParseUUID(agentID),
		ChatSessionID: util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		CreatedAt:     pgtype.Timestamptz{Time: time.UnixMilli(1_721_000_100_456), Valid: true},
	}
	env, err := launcher.extraEnvForTask(ctx, task, runtime, "sbx-no-identity")
	if err != nil {
		t.Fatalf("extraEnvForTask returned error: %v", err)
	}
	if env["OPENAI_MODEL"] != "qwen3.5-plus" || env[chattrace.TraceIDEnvKey] != util.UUIDToString(task.ID) {
		t.Fatalf("extra env = %#v, want default FC model and task trace", env)
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
	if env["OPENAI_MODEL"] != "qwen3.7-plus" || env[chattrace.TraceIDEnvKey] != util.UUIDToString(task.ID) {
		t.Fatalf("extra env = %#v, want selected FC model and task trace", env)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_dingtalk_identity (
			agent_id, workspace_id, dws_uid, org_id, account_display_name, bound_by
		) VALUES ($1, $2, '24710833', '439446171', 'Xu Mo', $3)
	`, agentID, workspaceID, userID); err != nil {
		t.Fatalf("bind Agent DingTalk identity: %v", err)
	}
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-chat")
	if err != nil {
		t.Fatalf("extraEnvForTask with bound identity returned error: %v", err)
	}
	if env["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_from_agent_binding" {
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
		request.Source["identity_source"] != "agent_binding_fallback" {
		t.Fatalf("Agent Identity request = %#v", request)
	}
	if _, present := request.Source["chat_session_id"]; present {
		t.Fatalf("launcher fallback is aware of chat source: %#v", request.Source)
	}

	task.Context = []byte(`{"dispatch_source":{"platform":"dingtalk","type":"digital_employee"},"agent_identity_context_token":"caller_external_token","agent_identity_context_token_expires_at":4102444800000}`)
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-external")
	if err != nil {
		t.Fatalf("extraEnvForTask with external identity returned error: %v", err)
	}
	if env["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_from_agent_binding" || len(identityClient.requests) != 2 {
		t.Fatalf("Agent binding did not win over external identity: requests=%d env=%#v", len(identityClient.requests), env)
	}
	task.Context = []byte(`{"dingtalk_robot_identity":{"uid":"99999999","org_id":"88888888"}}`)
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-legacy-context")
	if err != nil {
		t.Fatalf("extraEnvForTask legacy task context fallback returned error: %v", err)
	}
	if env["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_from_agent_binding" || len(identityClient.requests) != 3 {
		t.Fatalf("legacy channel identity bypassed Agent binding fallback: requests=%d env=%#v", len(identityClient.requests), env)
	}
	if identityClient.requests[2].UID != "24710833" || identityClient.requests[2].OrgID != "439446171" {
		t.Fatalf("launcher consumed channel identity: %#v", identityClient.requests[2])
	}
	task.Context = []byte(`{"dispatch_source":{"platform":"dingtalk","type":"digital_employee"}}`)
	task.ChatSessionID = pgtype.UUID{}
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-direct-task")
	if err != nil {
		t.Fatalf("extraEnvForTask direct task fallback returned error: %v", err)
	}
	if env["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_from_agent_binding" || len(identityClient.requests) != 4 {
		t.Fatalf("direct task did not use Agent binding: requests=%d env=%#v", len(identityClient.requests), env)
	}
	if _, present := identityClient.requests[3].Source["chat_session_id"]; present {
		t.Fatalf("direct task identity source contains chat session: %#v", identityClient.requests[3].Source)
	}
	task.ChatSessionID = util.MustParseUUID("22222222-2222-2222-2222-222222222222")

	identityClient.err = errors.New("HSF unavailable")
	task.Context = nil
	if _, err := launcher.extraEnvForTask(ctx, task, runtime, "sbx-chat"); err == nil {
		t.Fatal("bound chat must fail when Agent Identity context creation fails")
	}

	identityClient.err = nil
	requestCount := len(identityClient.requests)
	runtime.Metadata = []byte(`{"kind":"fc-e2b","capabilities":["hermes"]}`)
	task.Context = []byte(`{"agent_identity_context_token":"external-on-non-dws","agent_identity_context_token_expires_at":4102444800000}`)
	env, err = launcher.extraEnvForTask(ctx, task, runtime, "sbx-no-dws")
	if err != nil {
		t.Fatalf("non-DWS runtime chat returned error: %v", err)
	}
	if len(identityClient.requests) != requestCount || env["AGENT_IDENTITY_CONTEXT_TOKEN"] != "external-on-non-dws" || env["OPENAI_MODEL"] != "qwen3.7-plus" || env[chattrace.TraceIDEnvKey] != util.UUIDToString(task.ID) {
		t.Fatalf("non-DWS runtime did not consume prepared identity: requests=%d env=%#v", len(identityClient.requests), env)
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

func TestFCE2BIdentityEnvPrefersAgentBindingOverExternalContextToken(t *testing.T) {
	bindings := &fakeAgentIdentityBindingReader{identity: db.AgentDingtalkIdentity{
		DwsUid: "24710833",
		OrgID:  "439446171",
	}}
	identityContexts := &fakeAgentIdentityContextCreator{
		result: agentidentityhsf.CreateContextResult{
			ContextToken: "agent-binding-context-token",
			ExpiresAt:    time.Now().Add(15 * time.Minute).UnixMilli(),
		},
	}
	launcher := &FCE2BLauncher{
		Config: FCE2BConfig{
			AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
			AgentIdentityTimeout: 7 * time.Second,
			DWSClientSecret:      "dws-client-secret",
		},
		IdentityBindings: bindings,
		AgentIdentity:    identityContexts,
	}
	task := db.AgentTaskQueue{
		ID:      util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID: util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		Context: []byte(`{"agent_identity_context_token":"external-context-token","agent_identity_context_token_expires_at":4102444800000,"agent_identity_context_token_source":"external"}`),
	}
	runtime := db.AgentRuntime{
		WorkspaceID: util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		RuntimeMode: "cloud",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`),
	}

	env, err := launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-binding-priority")
	if err != nil {
		t.Fatalf("identityEnvForTask: %v", err)
	}
	if env[protocol.AgentIdentityContextTokenEnvKey] != "agent-binding-context-token" {
		t.Fatalf("identity env = %#v, want Agent binding token", env)
	}
	if len(bindings.requests) != 1 || len(identityContexts.requests) != 1 {
		t.Fatalf("binding requests = %d, context requests = %d", len(bindings.requests), len(identityContexts.requests))
	}
}

func TestFCE2BIdentityEnvUsesOnlyPreparedTokenOrAgentBindingFallback(t *testing.T) {
	bindings := &fakeAgentIdentityBindingReader{identity: db.AgentDingtalkIdentity{
		DwsUid: "24710833",
		OrgID:  "439446171",
	}}
	identityContexts := &fakeAgentIdentityContextCreator{
		result: agentidentityhsf.CreateContextResult{
			ContextToken: "agent-binding-context-token",
			ExpiresAt:    time.Now().Add(15 * time.Minute).UnixMilli(),
		},
	}
	launcher := &FCE2BLauncher{
		Config: FCE2BConfig{
			AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
			AgentIdentityTimeout: 7 * time.Second,
			DWSClientSecret:      "dws-client-secret",
		},
		IdentityBindings: bindings,
		AgentIdentity:    identityContexts,
	}
	task := db.AgentTaskQueue{
		ID:      util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID: util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		Context: []byte(`{"dingtalk_robot_identity":{"uid":"99999999","org_id":"88888888"}}`),
	}
	runtime := db.AgentRuntime{
		WorkspaceID: util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		RuntimeMode: "cloud",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`),
	}

	env, err := launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-1")
	if err != nil {
		t.Fatalf("identityEnvForTask: %v", err)
	}
	if env[protocol.AgentIdentityContextTokenEnvKey] != "agent-binding-context-token" {
		t.Fatalf("identity env = %#v", env)
	}
	if len(bindings.requests) != 1 || len(identityContexts.requests) != 1 {
		t.Fatalf("binding requests = %d, context requests = %d", len(bindings.requests), len(identityContexts.requests))
	}
	request := identityContexts.requests[0]
	if request.UID != "24710833" || request.OrgID != "439446171" || request.Source["identity_source"] != "agent_binding_fallback" {
		t.Fatalf("Agent Identity request = %#v", request)
	}

	task.Context = []byte(`{"agent_identity_context_token":"prepared-context-token","agent_identity_context_token_expires_at":4102444800000,"dingtalk_robot_identity":{"uid":"99999999","org_id":"88888888"}}`)
	runtime.Metadata = []byte(`{"kind":"fc-e2b","capabilities":["hermes"]}`)
	env, err = launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-2")
	if err != nil {
		t.Fatalf("prepared identityEnvForTask: %v", err)
	}
	if env[protocol.AgentIdentityContextTokenEnvKey] != "prepared-context-token" {
		t.Fatalf("prepared identity env = %#v", env)
	}
	if len(bindings.requests) != 1 || len(identityContexts.requests) != 1 {
		t.Fatal("usable task-context cache must bypass Agent binding fallback")
	}

	task.Context = []byte(`{"agent_identity_context_token":"external-context-token","agent_identity_context_token_expires_at":4102444800000,"agent_identity_context_token_source":"external"}`)
	env, err = launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-external")
	if err != nil {
		t.Fatalf("external identityEnvForTask: %v", err)
	}
	if env[protocol.AgentIdentityContextTokenEnvKey] != "external-context-token" {
		t.Fatalf("external identity env = %#v", env)
	}
	if len(bindings.requests) != 1 || len(identityContexts.requests) != 1 {
		t.Fatal("external ContextToken must bypass Agent binding fallback")
	}

	runtime.Metadata = []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`)
	bindings.err = pgx.ErrNoRows
	bindings.requests = nil
	identityContexts.requests = nil
	for _, invalidContext := range []string{
		`{"agent_identity_context_token":42}`,
		`{"agent_identity_context_token":""}`,
		`{"agent_identity_context_token":null}`,
		`{"agent_identity_context_token":"missing-expiry"}`,
		`{"agent_identity_context_token_expires_at":4102444800000}`,
		`{"agent_identity_context_token":"","agent_identity_context_token_expires_at":1}`,
		`{"agent_identity_context_token":null,"agent_identity_context_token_expires_at":1}`,
	} {
		task.Context = []byte(invalidContext)
		if _, err := launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-invalid"); err == nil {
			t.Fatalf("invalid prepared ContextToken silently used Agent binding fallback: %s", invalidContext)
		}
	}
	if len(identityContexts.requests) != 0 {
		t.Fatal("invalid prepared ContextToken must not invoke Agent Identity HSF without an Agent binding")
	}

	bindings.err = nil
	bindings.requests = nil
	for _, cachedContext := range []string{
		`{"agent_identity_context_token":"expired-cache","agent_identity_context_token_expires_at":1}`,
		fmt.Sprintf(
			`{"agent_identity_context_token":"near-expiry-cache","agent_identity_context_token_expires_at":%d}`,
			time.Now().Add(30*time.Second).UnixMilli(),
		),
	} {
		task.Context = []byte(cachedContext)
		identityContexts.result.ExpiresAt = time.Now().Add(15 * time.Minute).UnixMilli()
		env, err = launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-refresh-cache")
		if err != nil {
			t.Fatalf("refresh cached ContextToken: %v", err)
		}
		if env[protocol.AgentIdentityContextTokenEnvKey] != "agent-binding-context-token" {
			t.Fatalf("refreshed cache env = %#v", env)
		}
	}
	if len(bindings.requests) != 2 || len(identityContexts.requests) != 2 {
		t.Fatalf("expired/near-expiry cache did not refresh from Agent binding: bindings=%d contexts=%d",
			len(bindings.requests), len(identityContexts.requests))
	}

	bindings.err = pgx.ErrNoRows
	task.Context = []byte(`{"agent_identity_context_token":"expired-cache","agent_identity_context_token_expires_at":1}`)
	if _, err := launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-refresh-without-binding"); err == nil {
		t.Fatal("expired cache without a Multica Agent binding must not run without identity")
	}

	task.Context = []byte(`{"agent_identity_context_token":"expired-external","agent_identity_context_token_expires_at":1,"agent_identity_context_token_source":"external"}`)
	if _, err := launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-expired-external"); err == nil {
		t.Fatal("expired external ContextToken must fail instead of changing execution identity")
	}
	if len(identityContexts.requests) != 2 {
		t.Fatal("expired external ContextToken invoked HSF without an Agent binding")
	}

	bindings.err = nil
	task.Context = nil
	identityContexts.result.ExpiresAt = 1
	env, err = launcher.identityEnvForTask(context.Background(), task, runtime, "sandbox-fresh-token")
	if err != nil {
		t.Fatalf("freshly acquired ContextToken must trust the HSF response: %v", err)
	}
	if env[protocol.AgentIdentityContextTokenEnvKey] != "agent-binding-context-token" {
		t.Fatalf("freshly acquired identity env = %#v", env)
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
		Context: []byte(`{"agent_identity_context_token":"ctx_sandbox_token","agent_identity_context_token_expires_at":4102444800000}`),
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

func TestFCE2BExtraEnvAllowsGithubOnlyAgentIdentityWithoutDWSSecret(t *testing.T) {
	task := db.AgentTaskQueue{
		Context: []byte(`{"agent_identity_context_token":"ctx_github_token","agent_identity_context_token_expires_at":4102444800000}`),
	}
	got, err := fcE2BAgentIdentityExtraEnv(task, FCE2BConfig{
		AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
		AgentIdentityTimeout: 7 * time.Second,
	})
	if err != nil {
		t.Fatalf("fcE2BAgentIdentityExtraEnv: %v", err)
	}
	if got["AGENT_IDENTITY_CONTEXT_TOKEN"] != "ctx_github_token" {
		t.Fatalf("AGENT_IDENTITY_CONTEXT_TOKEN = %q, want ctx_github_token", got["AGENT_IDENTITY_CONTEXT_TOKEN"])
	}
	if _, ok := got["DWS_CLIENT_SECRET"]; ok {
		t.Fatal("DWS_CLIENT_SECRET should be omitted when not configured")
	}
}

func TestFCE2BExtraEnvRejectsContextTokenWithoutUsableExpiry(t *testing.T) {
	cfg := FCE2BConfig{
		AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
		DWSClientSecret:      "dws-client-secret",
	}
	tests := []struct {
		name    string
		context string
	}{
		{name: "missing expiry", context: `{"agent_identity_context_token":"ctx-token"}`},
		{name: "expiry without token", context: `{"agent_identity_context_token_expires_at":4102444800000}`},
		{name: "expired external", context: `{"agent_identity_context_token":"ctx-token","agent_identity_context_token_expires_at":1,"agent_identity_context_token_source":"external"}`},
		{
			name: "external inside one minute safety window",
			context: fmt.Sprintf(
				`{"agent_identity_context_token":"ctx-token","agent_identity_context_token_expires_at":%d,"agent_identity_context_token_source":"external"}`,
				time.Now().Add(30*time.Second).UnixMilli(),
			),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fcE2BAgentIdentityExtraEnv(db.AgentTaskQueue{Context: []byte(tc.context)}, cfg); err == nil {
				t.Fatalf("context %s was accepted", tc.context)
			}
		})
	}
}

func TestFCE2BTaskTraceEnv(t *testing.T) {
	trace, err := chattrace.From("37d0871a-3657-4c74-91fa-39e846fa90a0", "web", 1_721_000_000_123)
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := chattrace.Merge([]byte(`{"other":true}`), trace)
	if err != nil {
		t.Fatal(err)
	}
	taskID := "cc22f8e5-c591-43bb-8757-699bd98f5797"
	task := db.AgentTaskQueue{
		ID:        util.MustParseUUID(taskID),
		CreatedAt: pgtype.Timestamptz{Time: time.UnixMilli(1_721_000_000_999), Valid: true},
		Context:   contextJSON,
	}
	env, err := fcE2BTaskTraceEnv(task)
	if err != nil {
		t.Fatal(err)
	}
	if env[chattrace.TraceIDEnvKey] != trace.TraceID || env[chattrace.TraceStartedAtUnixMSEnvKey] != "1721000000123" {
		t.Fatalf("trace env = %#v", env)
	}
	if !isAllowedFCE2BRunnerExtraEnv(chattrace.TraceIDEnvKey) || !isAllowedFCE2BRunnerExtraEnv(chattrace.TraceStartedAtUnixMSEnvKey) {
		t.Fatal("trace env keys are not allowed through the fixed root entrypoint")
	}

	createdAt := time.UnixMilli(1_721_000_100_456)
	env, err = fcE2BTaskTraceEnv(db.AgentTaskQueue{
		ID:        util.MustParseUUID(taskID),
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		Context:   []byte(`{"non_chat_task":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if env[chattrace.TraceIDEnvKey] != taskID || env[chattrace.TraceStartedAtUnixMSEnvKey] != strconv.FormatInt(createdAt.UnixMilli(), 10) {
		t.Fatalf("non-chat trace env = %#v", env)
	}
}

func TestFCE2BExtraEnvDoesNotUseRuntimePromptEnvironmentVariable(t *testing.T) {
	task := db.AgentTaskQueue{
		Context: []byte(`{"dispatch_runtime_prompt":"private runtime instruction"}`),
	}
	got, err := fcE2BAgentIdentityExtraEnv(task, FCE2BConfig{})
	if err != nil {
		t.Fatalf("fcE2BAgentIdentityExtraEnv: %v", err)
	}
	if _, exists := got["MULTICA_DISPATCH_RUNTIME_PROMPT"]; exists {
		t.Fatalf("runtime prompt left in an unconsumed environment variable: %#v", got)
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

func TestFCE2BConfigFromEnvKeepsConfiguredAgentIdentityURLInPrePublish(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("AONE_ENV_TYPE", "prepub")
	t.Setenv("MULTICA_AGENT_IDENTITY_BASE_URL", "https://agent-identity.dingtalk.com/")
	cfg := FCE2BConfigFromEnv()
	if cfg.AgentIdentityBaseURL != "https://agent-identity.dingtalk.com" {
		t.Fatalf("base url = %q", cfg.AgentIdentityBaseURL)
	}
}

func TestFCE2BConfigFromEnvKeepsAgentIdentityProductionURLInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("MULTICA_AGENT_IDENTITY_BASE_URL", "https://agent-identity.dingtalk.com/")
	cfg := FCE2BConfigFromEnv()
	if cfg.AgentIdentityBaseURL != "https://agent-identity.dingtalk.com" {
		t.Fatalf("base url = %q", cfg.AgentIdentityBaseURL)
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
