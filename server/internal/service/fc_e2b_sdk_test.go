package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// requireFCE2BSDKAccepts panics when the SDK transport cannot decode an argv
// the launcher built. The launcher test fakes call it, so every existing
// launcher flow also proves the SDK accepts its commands.
func requireFCE2BSDKAccepts(args []string) {
	if _, err := parseFCE2BOperation(args); err != nil {
		panic(fmt.Sprintf("SDK transport rejects launcher command %q: %v", strings.Join(args[:min(len(args), 3)], " "), err))
	}
}

type fakeFCE2BStart struct {
	sandboxID     string
	authorization string
	accessToken   string
	timeoutHeader string
	cmd           string
	args          []string
	envs          map[string]string
}

// fakeFCE2BServer is the FC/E2B control plane and envd Connect endpoint.
type fakeFCE2BServer struct {
	t      *testing.T
	server *httptest.Server

	mu        sync.Mutex
	creates   []map[string]any
	connects  []string
	starts    []fakeFCE2BStart
	signals   int
	templates []*http.Request

	createStatus  int
	createBody    string
	connectStatus int
	templateBody  string
	// events returns the stream for one Start; hold keeps it open afterwards.
	events func(start fakeFCE2BStart) (events []map[string]any, hold bool)
	closed chan struct{}
}

func newFakeFCE2BServer(t *testing.T) *fakeFCE2BServer {
	f := &fakeFCE2BServer{t: t, closed: make(chan struct{}, 16)}
	f.events = func(fakeFCE2BStart) ([]map[string]any, bool) {
		return []map[string]any{fcE2BStartEvent(7), fcE2BEndEvent(0)}, false
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	// Route envd traffic for every sandbox to this server.
	t.Setenv("E2B_SANDBOX_URL", f.server.URL)
	t.Setenv("E2B_DEBUG", "")
	t.Setenv("E2B_TEAM_ID", "")
	return f
}

func (f *fakeFCE2BServer) env() []string {
	return []string{"E2B_API_KEY=e2b_0123abcd", "E2B_API_URL=" + f.server.URL, "E2B_DOMAIN=fc.test"}
}

func (f *fakeFCE2BServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
		if r.Header.Get("X-API-KEY") != "e2b_0123abcd" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request map[string]any
		_ = json.Unmarshal(body, &request)
		f.mu.Lock()
		f.creates = append(f.creates, request)
		f.mu.Unlock()
		if f.createStatus != 0 {
			w.WriteHeader(f.createStatus)
			_, _ = io.WriteString(w, f.createBody)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx_123","envdVersion":"0.5.4","envdAccessToken":"envd-token","templateID":"tpl"}`)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/sandboxes/") && strings.HasSuffix(r.URL.Path, "/connect"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/sandboxes/"), "/connect")
		f.mu.Lock()
		f.connects = append(f.connects, id+" "+strings.TrimSpace(string(body)))
		f.mu.Unlock()
		if f.connectStatus != 0 {
			w.WriteHeader(f.connectStatus)
			_, _ = io.WriteString(w, `{"code":404,"message":"sandbox not found"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"sandboxID":%q,"envdVersion":"0.5.4","envdAccessToken":"envd-token"}`, id)
	case r.Method == http.MethodGet && r.URL.Path == "/templates":
		f.mu.Lock()
		f.templates = append(f.templates, r)
		f.mu.Unlock()
		_, _ = io.WriteString(w, f.templateBody)
	case r.URL.Path == "/process.Process/SendSignal":
		f.mu.Lock()
		f.signals++
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	case r.URL.Path == "/process.Process/Start":
		f.start(w, r, body)
	default:
		f.t.Errorf("unexpected FC/E2B request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *fakeFCE2BServer) start(w http.ResponseWriter, r *http.Request, body []byte) {
	var request struct {
		Process struct {
			Cmd  string            `json:"cmd"`
			Args []string          `json:"args"`
			Envs map[string]string `json:"envs"`
		} `json:"process"`
	}
	if len(body) < 5 || json.Unmarshal(body[5:], &request) != nil {
		f.t.Errorf("invalid Start envelope")
		return
	}
	start := fakeFCE2BStart{
		sandboxID:     r.Header.Get("E2b-Sandbox-Id"),
		authorization: r.Header.Get("Authorization"),
		accessToken:   r.Header.Get("X-Access-Token"),
		timeoutHeader: r.Header.Get("Connect-Timeout-Ms"),
		cmd:           request.Process.Cmd,
		args:          request.Process.Args,
		envs:          request.Process.Envs,
	}
	f.mu.Lock()
	f.starts = append(f.starts, start)
	events := f.events
	f.mu.Unlock()
	stream, hold := events(start)
	w.Header().Set("Content-Type", "application/connect+json")
	w.WriteHeader(http.StatusOK)
	for _, event := range stream {
		writeFCE2BEnvelope(w, 0, event)
	}
	if hold {
		<-r.Context().Done()
		f.closed <- struct{}{}
		return
	}
	writeFCE2BEnvelope(w, 0x02, map[string]any{})
}

func writeFCE2BEnvelope(w http.ResponseWriter, flags byte, payload any) {
	encoded, _ := json.Marshal(payload)
	header := make([]byte, 5)
	header[0] = flags
	binary.BigEndian.PutUint32(header[1:], uint32(len(encoded)))
	_, _ = w.Write(append(header, encoded...))
	w.(http.Flusher).Flush()
}

func fcE2BStartEvent(pid int) map[string]any {
	return map[string]any{"event": map[string]any{"start": map[string]any{"pid": pid}}}
}

func fcE2BOutputEvent(stream, text string) map[string]any {
	return map[string]any{"event": map[string]any{"data": map[string]any{stream: base64.StdEncoding.EncodeToString([]byte(text))}}}
}

func fcE2BEndEvent(code int) map[string]any {
	return map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": code}}}
}

func (f *fakeFCE2BServer) snapshot() ([]map[string]any, []string, []fakeFCE2BStart, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.creates...), append([]string(nil), f.connects...), append([]fakeFCE2BStart(nil), f.starts...), f.signals
}

func basicUser(user string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"))
}

// The CLI's buildCommand output for these argv was produced by running the
// e2b CLI 2.13.0 helper itself.
func TestFCE2BShellCommandMatchesCLI(t *testing.T) {
	cases := []struct {
		parts []string
		want  string
	}{
		{[]string{"true"}, "true"},
		{[]string{"/usr/bin/test", "-x", "/usr/local/libexec/multica-fc-hermes-container-log-entry"}, "/usr/bin/test -x /usr/local/libexec/multica-fc-hermes-container-log-entry"},
		{[]string{"python3", "-c", "import sys; print('it''s')\nprint(\"中文\")"}, "python3 -c 'import sys; print('\"'\"'it'\"'\"''\"'\"'s'\"'\"')\nprint(\"中文\")'"},
		{[]string{"/opt/task-python/bin/python3", "-E", "-s", "/opt/multica-dsh/multica_dsh_profile.py", "--stage"}, "/opt/task-python/bin/python3 -E -s /opt/multica-dsh/multica_dsh_profile.py --stage"},
		{[]string{"echo", "", "a b", "$HOME", "x=y,z@1%2+3:4"}, "echo '' 'a b' '$HOME' x=y,z@1%2+3:4"},
		{[]string{"line\n"}, "line\n"},
	}
	for _, tc := range cases {
		if got := fcE2BShellCommand(tc.parts); got != tc.want {
			t.Fatalf("fcE2BShellCommand(%q) = %q, want %q", tc.parts, got, tc.want)
		}
	}
}

func TestParseFCE2BOperationAcceptsOnlyLauncherShapes(t *testing.T) {
	exec, err := parseFCE2BOperation([]string{"sandbox", "exec", "--background", "--user", "root", "-e", "LD_PRELOAD=", "-e", "A=b=c", "-e", "A=d", "sbx_1", "--", "/bin/run", "--flag"})
	if err != nil || exec.exec == nil {
		t.Fatalf("launcher exec rejected: %v", err)
	}
	want := fcE2BExecRequest{SandboxID: "sbx_1", User: "root", Env: map[string]string{"LD_PRELOAD": "", "A": "d"}, Command: "/bin/run --flag", Background: true}
	if !reflect.DeepEqual(*exec.exec, want) {
		t.Fatalf("exec = %#v, want %#v", *exec.exec, want)
	}
	create, err := parseFCE2BOperation([]string{"sandbox", "create", "--detach", "--timeout", "4800", "--lifecycle.ontimeout", "kill", "tpl"})
	if err != nil || create.create == nil || *create.create != (fcE2BCreateRequest{Template: "tpl", TimeoutSeconds: 4800, OnTimeout: "kill"}) {
		t.Fatalf("launcher create = %#v, %v", create.create, err)
	}
	if list, err := parseFCE2BOperation([]string{"template", "list", "--format", "json"}); err != nil || !list.listTemplates {
		t.Fatalf("template list rejected: %v", err)
	}
	for _, args := range [][]string{
		nil,
		{"sandbox", "kill", "sbx_1"},
		{"sandbox", "exec", "sbx_1"},
		{"sandbox", "exec", "--cwd", "/tmp", "sbx_1", "true"},
		{"sandbox", "exec", "-e", "NOVALUE", "sbx_1", "true"},
		{"sandbox", "exec", "-e", "=x", "sbx_1", "true"},
		{"sandbox", "exec", "--user", "", "sbx_1", "true"},
		{"sandbox", "exec", "sbx_1", "ls", "-la"},
		{"sandbox", "exec", "../sbx", "--", "true"},
		{"sandbox", "exec", "sbx_1", "--"},
		{"sandbox", "create", "--timeout", "4800", "tpl"},
		{"sandbox", "create", "--detach", "--timeout", "10", "tpl"},
		{"sandbox", "create", "--detach", "--lifecycle.ontimeout", "stop", "tpl"},
		{"sandbox", "create", "--detach", "tpl", "other"},
		{"template", "list"},
	} {
		if _, err := parseFCE2BOperation(args); err == nil {
			t.Fatalf("unsupported command accepted: %q", args)
		}
	}
	_, err = parseFCE2BOperation([]string{"sandbox", "exec", "--token", "mdt_secret_value", "sbx_1", "true"})
	if err == nil || strings.Contains(err.Error(), "mdt_secret_value") {
		t.Fatalf("rejection must not echo arguments: %v", err)
	}
}

func TestNewFCE2BCommandRunnerSelectsTransport(t *testing.T) {
	for _, transport := range []string{"", "sdk", " SDK "} {
		if runner, err := NewFCE2BCommandRunner(transport); err != nil || !reflect.DeepEqual(runner, SDKCommandRunner{}) {
			t.Fatalf("transport %q = %#v, %v; want SDK", transport, runner, err)
		}
	}
	if runner, err := NewFCE2BCommandRunner("cli"); err != nil || runner != (OSCommandRunner{}) {
		t.Fatalf("cli transport = %#v, %v", runner, err)
	}
	if _, err := NewFCE2BCommandRunner("both"); err == nil {
		t.Fatal("unknown transport accepted")
	}
	t.Setenv("MULTICA_FC_E2B_TRANSPORT", "both")
	if _, ok := defaultFCE2BCommandRunner().(SDKCommandRunner); !ok {
		t.Fatal("invalid transport configuration must keep the SDK")
	}
	t.Setenv("MULTICA_FC_E2B_TRANSPORT", "cli")
	if _, ok := NewFCE2BLauncher(nil, nil, FCE2BConfig{}, nil).Runner.(OSCommandRunner); !ok {
		t.Fatal("explicit CLI rollback was not selected")
	}
	t.Setenv("MULTICA_FC_E2B_TRANSPORT", "")
	if _, ok := NewFCE2BLauncher(nil, nil, FCE2BConfig{}, nil).Runner.(SDKCommandRunner); !ok {
		t.Fatal("SDK is not the default transport")
	}
}

// The launcher's create → ready → probe → run-once sequence sends the same
// HTTP requests through the SDK that the e2b CLI sent.
func TestFCE2BLauncherSDKTransportEndToEnd(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	fake.events = func(start fakeFCE2BStart) ([]map[string]any, bool) {
		if strings.Contains(start.args[2], "--runtime-id") {
			// The runner keeps running after launch.
			return []map[string]any{fcE2BStartEvent(42)}, true
		}
		return []map[string]any{fcE2BStartEvent(7), fcE2BEndEvent(0)}, false
	}
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{
		ServerURL:           "https://api.multica.test",
		APIKey:              "e2b_0123abcd",
		APIURL:              fake.server.URL,
		Domain:              "fc.test",
		LLMBaseURL:          "https://llm.test/v1",
		LLMAPIKey:           "maas_secret",
		LLMModels:           []string{"qwen3.5-plus"},
		CLIPath:             "/usr/local/bin/e2b",
		SandboxReadyTimeout: 5 * time.Second,
	}, SDKCommandRunner{})

	sandboxID, err := launcher.createSandbox(context.Background(), "multica-fc-hermes-v1")
	if err != nil || sandboxID != "sbx_123" {
		t.Fatalf("createSandbox = %q, %v", sandboxID, err)
	}
	if err := launcher.waitSandboxReady(context.Background(), sandboxID); err != nil {
		t.Fatalf("waitSandboxReady: %v", err)
	}
	rt := db.AgentRuntime{
		ID:       util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		Name:     "FC-Hermes",
		DaemonID: pgtype.Text{String: "fc-e2b:ws:fc-hermes", Valid: true},
		Metadata: []byte(`{"runner":"multica-fc-hermes-container-log-entry"}`),
	}
	launch, err := launcher.detectFCE2BRunnerLaunch(context.Background(), sandboxID, rt)
	if err != nil {
		t.Fatalf("detectFCE2BRunnerLaunch: %v", err)
	}
	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	started := time.Now()
	if err := launcher.execRunOnce(context.Background(), sandboxID, rt, launch.Mode, taskID, "mdt_test_token", true, map[string]string{"OPENAI_MODEL": "qwen3.5-plus"}); err != nil {
		t.Fatalf("execRunOnce: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("background launch waited for the runner to exit")
	}
	select {
	case <-fake.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("background launch did not detach from the runner stream")
	}

	creates, connects, starts, signals := fake.snapshot()
	if len(creates) != 1 {
		t.Fatalf("creates = %d", len(creates))
	}
	create := creates[0]
	if create["templateID"] != "multica-fc-hermes-v1" || create["timeout"] != float64(4800) || create["autoPause"] != false ||
		create["secure"] != true || create["allow_internet_access"] != true {
		t.Fatalf("create body = %#v", create)
	}
	if want := []string{`sbx_123 {"timeout":300}`, `sbx_123 {"timeout":300}`, `sbx_123 {"timeout":300}`}; !reflect.DeepEqual(connects, want) {
		t.Fatalf("connects = %#v, want the CLI's per-exec 300s attach %#v", connects, want)
	}
	if len(starts) != 3 {
		t.Fatalf("starts = %d", len(starts))
	}
	for _, start := range starts {
		if start.cmd != "/bin/bash" || len(start.args) != 3 || start.args[0] != "-l" || start.args[1] != "-c" ||
			start.accessToken != "envd-token" || start.sandboxID != "sbx_123" || start.timeoutHeader != "" {
			t.Fatalf("start = %#v", start)
		}
	}
	if starts[0].args[2] != "true" || starts[0].authorization != "" || len(starts[0].envs) != 0 {
		t.Fatalf("ready probe = %#v", starts[0])
	}
	if starts[1].args[2] != "/usr/bin/test -x /usr/local/libexec/multica-fc-hermes-container-log-entry" || starts[1].authorization != basicUser("user") {
		t.Fatalf("runner probe = %#v", starts[1])
	}
	run := starts[2]
	wantCommand := "/usr/local/libexec/multica-fc-hermes-container-log-entry --runtime-id 11111111-1111-1111-1111-111111111111 --provider hermes --health-port " + strconv.Itoa(fcE2BHealthPortForTask(taskID))
	if run.args[2] != wantCommand || run.authorization != basicUser("root") {
		t.Fatalf("runner launch = %#v", run)
	}
	for key, want := range map[string]string{
		"LD_PRELOAD": "", "BASH_ENV": "", "ENV": "", "MULTICA_DAEMON_TOKEN": "mdt_test_token", "HOME": "/root",
		"OPENAI_API_KEY": "maas_secret", "MULTICA_FC_E2B_COLD_START": "true", "OPENAI_MODEL": "qwen3.5-plus",
	} {
		if got, ok := run.envs[key]; !ok || got != want {
			t.Fatalf("runner env %s = %q (present %v), want %q", key, got, ok, want)
		}
	}
	if signals != 0 {
		t.Fatal("detaching from the runner must not signal it")
	}
}

func TestSDKCommandRunnerReportsRemoteExitLikeCLI(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	token := "ghp_" + "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn"
	fake.events = func(fakeFCE2BStart) ([]map[string]any, bool) {
		return []map[string]any{fcE2BStartEvent(7), fcE2BOutputEvent("stdout", "partial\n"), fcE2BOutputEvent("stderr", "boom "+token), fcE2BEndEvent(3)}, false
	}
	out, err := SDKCommandRunner{}.Run(context.Background(), "e2b", []string{"sandbox", "exec", "--user", "user", "sbx_1", "--", "python3", "-c", "raise SystemExit(3)"}, fake.env())
	if out != "partial\n" {
		t.Fatalf("stdout = %q", out)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "command failed: exit status 3: partial\nboom ") || strings.Contains(err.Error(), token) {
		t.Fatalf("error = %v", err)
	}
	var exitErr *fcE2BCommandExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode != 3 {
		t.Fatalf("exit code not preserved: %v", err)
	}
}

func TestSDKCommandRunnerFailsExplicitlyOnOutputOverflow(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	fake.events = func(fakeFCE2BStart) ([]map[string]any, bool) {
		chunk := strings.Repeat("x", 800)
		return []map[string]any{fcE2BStartEvent(7), fcE2BOutputEvent("stdout", chunk), fcE2BOutputEvent("stderr", chunk)}, true
	}
	out, err := SDKCommandRunner{maxOutputBytes: 1024}.Run(context.Background(), "e2b", []string{"sandbox", "exec", "sbx_1", "true"}, fake.env())
	if out != "" || err == nil || !strings.Contains(err.Error(), "output exceeded 1024 bytes") {
		t.Fatalf("overflow = %q, %v", out, err)
	}
	select {
	case <-fake.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("overflow did not stop reading the stream")
	}
	if _, _, _, signals := fake.snapshot(); signals != 0 {
		t.Fatal("overflow must not kill the remote command")
	}
}

func TestSDKCommandRunnerDeadlineClosesStreamOnly(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	fake.events = func(fakeFCE2BStart) ([]map[string]any, bool) {
		return []map[string]any{fcE2BStartEvent(7), fcE2BOutputEvent("stdout", "started")}, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	out, err := SDKCommandRunner{}.Run(ctx, "e2b", []string{"sandbox", "exec", "sbx_1", "sleep 60"}, fake.env())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || out != "started" {
		t.Fatalf("deadline = %q, %v", out, err)
	}
	select {
	case <-fake.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("deadline did not close the stream")
	}
	if _, _, _, signals := fake.snapshot(); signals != 0 {
		t.Fatal("like the CLI, a local deadline must not kill the remote command")
	}
}

func TestSDKCommandRunnerMissingSandboxDoesNotExec(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	fake.connectStatus = http.StatusNotFound
	_, err := SDKCommandRunner{}.Run(context.Background(), "e2b", []string{"sandbox", "exec", "sbx_gone", "true"}, fake.env())
	if err == nil || !strings.HasPrefix(err.Error(), "command failed: ") || !strings.Contains(err.Error(), "sandbox not found") {
		t.Fatalf("missing sandbox = %v", err)
	}
	if _, _, starts, _ := fake.snapshot(); len(starts) != 0 {
		t.Fatal("exec started without a connected sandbox")
	}
}

func TestSDKCommandRunnerCreateCapacityErrorKeepsClassification(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	fake.createStatus = http.StatusTooManyRequests
	fake.createBody = `{"code":429,"message":"StatusCode: 429 Code: ResourceExhausted Message: Function concurrent request count exceeded"}`
	launcher := NewFCE2BLauncher(nil, nil, FCE2BConfig{APIKey: "e2b_0123abcd", APIURL: fake.server.URL, Domain: "fc.test", SandboxReadyTimeout: 5 * time.Second}, SDKCommandRunner{})
	var delays []time.Duration
	launcher.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	launcher.jitter = func(base time.Duration) time.Duration { return base }
	_, err := launcher.createSandbox(context.Background(), "tpl")
	if err == nil || !isFCE2BSandboxCapacityRateLimitText(err.Error()) {
		t.Fatalf("capacity error lost its classification: %v", err)
	}
	if creates, _, _, _ := fake.snapshot(); len(creates) != fcE2BSandboxCreateMaxAttempts || len(delays) != fcE2BSandboxCreateMaxAttempts-1 {
		t.Fatalf("capacity retries = %d creates, %d delays", len(creates), len(delays))
	}
}

func TestSDKCommandRunnerListsTemplatesLikeCLI(t *testing.T) {
	fake := newFakeFCE2BServer(t)
	fake.templateBody = `[{"templateID":"tpl_1","aliases":["zeta","multica-m7-v0123456789abcdef-r1-abcdef","default"],"buildStatus":"ready","createdAt":"2026-09-20T01:02:03.100Z","updatedAt":"2026-09-21T01:02:03.100Z","cpuCount":2}]`
	cfg := FCE2BConfig{
		APIKey:                      "e2b_0123abcd",
		APIURL:                      fake.server.URL,
		Domain:                      "fc.test",
		CLIPath:                     "/usr/local/bin/e2b",
		RuntimeProviderFingerprints: map[string][]string{"0123456789abcdef": {"hermes"}},
	}
	templates, err := ListFCE2BTemplates(context.Background(), cfg, SDKCommandRunner{})
	if err != nil || len(templates) != 1 {
		t.Fatalf("templates = %#v, %v", templates, err)
	}
	got := templates[0]
	// The CLI sorts aliases, so the fingerprint alias names the template.
	if got.ID != "tpl_1" || got.Name != "multica-m7-v0123456789abcdef-r1-abcdef" || got.Status != "ready" ||
		got.UpdatedAt != "2026-09-21T01:02:03.100Z" || !reflect.DeepEqual(got.Providers, []string{"hermes"}) {
		t.Fatalf("template = %#v", got)
	}
	fake.mu.Lock()
	request := fake.templates[0]
	fake.mu.Unlock()
	if request.Header.Get("X-API-KEY") != "e2b_0123abcd" || request.URL.RawQuery != "" {
		t.Fatalf("template list request = %v %v", request.Header, request.URL)
	}

	fake.templateBody = `not json`
	if _, err := ListFCE2BTemplates(context.Background(), cfg, SDKCommandRunner{}); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("invalid list = %v", err)
	}
}
