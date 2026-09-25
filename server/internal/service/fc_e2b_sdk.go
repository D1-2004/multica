package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// FC/E2B transports selectable with MULTICA_FC_E2B_TRANSPORT. The Go SDK is
// the default. "cli" restores the e2b CLI subprocess as an operator-selected
// rollback while the SDK rolls out; it is never a runtime fallback, so an
// ambiguous create or exec is not repeated through the other backend. Remove
// it together with the image's @e2b/cli once production runs on the SDK.
const (
	FCE2BTransportSDK = "sdk"
	FCE2BTransportCLI = "cli"
)

const (
	// The e2b CLI attaches to a sandbox for every `sandbox exec` through
	// POST /sandboxes/{id}/connect with the JS SDK default lease of 300s.
	// That call is not a read-only attach. Sending the same body keeps the
	// sandbox lease behaviour of every exec unchanged.
	fcE2BSDKConnectTimeoutSeconds = e2b.DefaultSandboxTimeoutSeconds
	// fcE2BSDKMaxOutputBytes bounds the stdout and stderr one command may
	// return. The SDK accumulates the whole stream internally, so exceeding
	// the bound stops reading and fails explicitly instead of truncating a
	// receipt. Callers accept at most 64 KiB of receipt.
	fcE2BSDKMaxOutputBytes = 4 << 20
	// fcE2BSDKMaxTemplateListBytes bounds the template list response.
	fcE2BSDKMaxTemplateListBytes = 8 << 20
	// fcE2BSDKTemplateListTimeout matches the SDK default request timeout.
	fcE2BSDKTemplateListTimeout = 60 * time.Second
	// fcE2BCLIMinSandboxTimeoutSeconds is the e2b CLI create floor.
	fcE2BCLIMinSandboxTimeoutSeconds = 30
)

// fcE2BShellSafeArg is the e2b CLI's SHELL_SAFE_RE: arguments made only of
// these characters are passed to bash unquoted.
var fcE2BShellSafeArg = regexp.MustCompile(`\A[A-Za-z0-9_@%+=:,./-]+\z`)

var fcE2BSDKHTTPClient = newFCE2BSDKHTTPClient()

func newFCE2BSDKHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	// No client-wide timeout: command streams stay open for the command's
	// lifetime. The caller context and the SDK request timeout bound them.
	return &http.Client{Transport: transport}
}

// NewFCE2BCommandRunner returns the CommandRunner for transport. An empty
// value selects the Go SDK.
func NewFCE2BCommandRunner(transport string) (CommandRunner, error) {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "", FCE2BTransportSDK:
		return SDKCommandRunner{}, nil
	case FCE2BTransportCLI:
		return OSCommandRunner{}, nil
	default:
		return nil, fmt.Errorf("invalid MULTICA_FC_E2B_TRANSPORT %q: expected %q or %q", transport, FCE2BTransportSDK, FCE2BTransportCLI)
	}
}

// defaultFCE2BCommandRunner reads MULTICA_FC_E2B_TRANSPORT. An invalid value
// keeps the SDK and is logged; it must not silently re-enable the CLI.
func defaultFCE2BCommandRunner() CommandRunner {
	transport := os.Getenv("MULTICA_FC_E2B_TRANSPORT")
	runner, err := NewFCE2BCommandRunner(transport)
	if err != nil {
		slog.Error("FC/E2B transport configuration ignored", "error", err, "transport", FCE2BTransportSDK)
		return SDKCommandRunner{}
	}
	return runner
}

// SDKCommandRunner performs the FC/E2B operations the launcher builds through
// the Go SDK instead of forking the e2b CLI. It accepts exactly the argv
// shapes Multica produces (sandbox create, sandbox exec, template list) and
// decodes them into typed requests. Any other shape is rejected rather than
// approximated. Credentials come from the E2B_* entries of env, the same
// values the CLI received; request-specific values never enter shared state.
type SDKCommandRunner struct {
	// HTTPClient carries control-plane and envd traffic. Nil uses a shared
	// client so connections are pooled across calls.
	HTTPClient *http.Client
	// maxOutputBytes overrides fcE2BSDKMaxOutputBytes in tests.
	maxOutputBytes int
}

type fcE2BCredentials struct {
	APIKey string
	APIURL string
	Domain string
}

type fcE2BCreateRequest struct {
	Template       string
	TimeoutSeconds int
	OnTimeout      string
}

type fcE2BExecRequest struct {
	SandboxID  string
	User       string
	Env        map[string]string
	Command    string
	Background bool
}

func (r SDKCommandRunner) Run(ctx context.Context, _ string, args []string, env []string) (string, error) {
	operation, err := parseFCE2BOperation(args)
	if err != nil {
		return "", err
	}
	creds := fcE2BCredentialsFromEnv(env)
	if operation.listTemplates {
		return r.listTemplates(ctx, creds)
	}
	client, err := r.client(creds)
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
	}
	if operation.create != nil {
		return r.create(ctx, client, *operation.create)
	}
	return r.exec(ctx, client, *operation.exec)
}

// fcE2BOperation is one decoded launcher command; exactly one field is set.
type fcE2BOperation struct {
	create        *fcE2BCreateRequest
	exec          *fcE2BExecRequest
	listTemplates bool
}

func parseFCE2BOperation(args []string) (fcE2BOperation, error) {
	switch {
	case len(args) >= 2 && args[0] == "sandbox" && args[1] == "exec":
		request, err := parseFCE2BExecArgs(args[2:])
		if err != nil {
			return fcE2BOperation{}, err
		}
		return fcE2BOperation{exec: &request}, nil
	case len(args) >= 2 && args[0] == "sandbox" && args[1] == "create":
		request, err := parseFCE2BCreateArgs(args[2:])
		if err != nil {
			return fcE2BOperation{}, err
		}
		return fcE2BOperation{create: &request}, nil
	case len(args) == 4 && args[0] == "template" && args[1] == "list" && args[2] == "--format" && args[3] == "json":
		return fcE2BOperation{listTemplates: true}, nil
	}
	// Arguments can carry tokens and signed URLs; never echo them.
	return fcE2BOperation{}, errors.New("unsupported FC/E2B operation")
}

func (r SDKCommandRunner) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return fcE2BSDKHTTPClient
}

// client builds a per-call SDK client. It is only configuration; connection
// reuse lives in the shared HTTP transport, so credentials and endpoints are
// never shared between calls with different configuration.
func (r SDKCommandRunner) client(creds fcE2BCredentials) (*e2b.Client, error) {
	options := []e2b.Option{e2b.WithAPIKey(creds.APIKey), e2b.WithHTTPClient(r.httpClient())}
	if creds.Domain != "" {
		options = append(options, e2b.WithDomain(creds.Domain))
	}
	if creds.APIURL != "" {
		options = append(options, e2b.WithAPIURL(creds.APIURL))
	}
	return e2b.NewClient(options...)
}

func fcE2BCredentialsFromEnv(env []string) fcE2BCredentials {
	var creds fcE2BCredentials
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "E2B_API_KEY":
			creds.APIKey = value
		case "E2B_API_URL":
			creds.APIURL = strings.TrimRight(value, "/")
		case "E2B_DOMAIN":
			creds.Domain = value
		}
	}
	return creds
}

// parseFCE2BCreateArgs accepts
// `--detach --timeout <seconds> --lifecycle.ontimeout <kill|pause> <template>`.
func parseFCE2BCreateArgs(args []string) (fcE2BCreateRequest, error) {
	invalid := errors.New("unsupported FC/E2B sandbox create arguments")
	var request fcE2BCreateRequest
	detached := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--detach":
			detached = true
		case "--timeout":
			if i+1 >= len(args) {
				return request, invalid
			}
			i++
			seconds, err := strconv.Atoi(args[i])
			if err != nil || seconds < fcE2BCLIMinSandboxTimeoutSeconds {
				return request, errors.New("FC/E2B sandbox create timeout must be at least 30 seconds")
			}
			request.TimeoutSeconds = seconds
		case "--lifecycle.ontimeout":
			if i+1 >= len(args) {
				return request, invalid
			}
			i++
			if args[i] != "kill" && args[i] != "pause" {
				return request, invalid
			}
			request.OnTimeout = args[i]
		default:
			if strings.HasPrefix(args[i], "-") || request.Template != "" {
				return request, invalid
			}
			request.Template = args[i]
		}
	}
	if !detached || request.Template == "" {
		return request, invalid
	}
	return request, nil
}

// parseFCE2BExecArgs accepts
// `[--background] [--user <user>] [-e KEY=VALUE]... <sandbox-id> [--] <command...>`,
// the only exec form the launcher builds. The command is joined exactly as
// the e2b CLI does before it runs through `/bin/bash -l -c`.
func parseFCE2BExecArgs(args []string) (fcE2BExecRequest, error) {
	invalid := errors.New("unsupported FC/E2B sandbox exec arguments")
	request := fcE2BExecRequest{Env: map[string]string{}}
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "--background":
			request.Background = true
			continue
		case "--user":
			if i+1 >= len(args) || args[i+1] == "" {
				return request, invalid
			}
			i++
			request.User = args[i]
			continue
		case "-e":
			if i+1 >= len(args) {
				return request, invalid
			}
			i++
			key, value, ok := strings.Cut(args[i], "=")
			if !ok || key == "" {
				return request, invalid
			}
			request.Env[key] = value
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			return request, invalid
		}
		break
	}
	if i >= len(args) || !fcE2BAPISandboxIDPattern.MatchString(args[i]) {
		return request, invalid
	}
	request.SandboxID = args[i]
	parts := args[i+1:]
	if len(parts) > 0 && parts[0] == "--" {
		parts = parts[1:]
	} else {
		// Without "--" the CLI would parse a dash token as its own option.
		for _, part := range parts {
			if strings.HasPrefix(part, "-") {
				return request, invalid
			}
		}
	}
	if len(parts) == 0 {
		return request, invalid
	}
	request.Command = fcE2BShellCommand(parts)
	return request, nil
}

// fcE2BShellCommand reproduces the e2b CLI's buildCommand: a single part is
// passed verbatim and several parts are shell-quoted and space-joined.
func fcE2BShellCommand(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = fcE2BShellQuote(part)
	}
	return strings.Join(quoted, " ")
}

func fcE2BShellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if fcE2BShellSafeArg.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
}

func (r SDKCommandRunner) create(ctx context.Context, client *e2b.Client, request fcE2BCreateRequest) (string, error) {
	options := []e2b.SandboxCreateOption{e2b.WithTemplate(request.Template)}
	if request.TimeoutSeconds > 0 {
		options = append(options, e2b.WithTimeout(request.TimeoutSeconds))
	}
	if request.OnTimeout != "" {
		options = append(options, e2b.WithLifecycle(e2b.SandboxLifecycle{OnTimeout: request.OnTimeout}))
	}
	sandbox, err := client.CreateSandbox(ctx, options...)
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
	}
	// Keep the create receipt the launcher already parses.
	return fmt.Sprintf("Sandbox created with ID %s using template %s\n", sandbox.SandboxID(), request.Template), nil
}

func (r SDKCommandRunner) exec(ctx context.Context, client *e2b.Client, request fcE2BExecRequest) (string, error) {
	sandbox, err := client.ConnectSandbox(ctx, request.SandboxID, fcE2BSDKConnectTimeoutSeconds)
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
	}
	// Like the CLI, the command itself has no remote timeout; the caller's
	// context bounds how long Multica waits for it.
	options := []e2b.CommandOption{e2b.WithCommandEnvs(request.Env), e2b.WithCommandTimeout(0)}
	if request.User != "" {
		options = append(options, e2b.WithCommandUser(request.User))
	}
	if request.Background {
		handle, err := sandbox.Commands.Start(ctx, request.Command, options...)
		if err != nil {
			return "", fcE2BSDKFailure(err, "", "")
		}
		// Detach only after envd reported the process start. Disconnect does
		// not stop the remote process.
		_ = handle.Disconnect()
		return "", nil
	}

	waitCtx, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	limit := r.maxOutputBytes
	if limit <= 0 {
		limit = fcE2BSDKMaxOutputBytes
	}
	output := &fcE2BBoundedOutput{limit: limit, exceeded: stopWaiting}
	handle, err := sandbox.Commands.Start(waitCtx, request.Command, options...)
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
	}
	// Cancelling the wait closes the local stream only; like killing the CLI,
	// it does not stop the remote command.
	defer func() { _ = handle.Disconnect() }()
	result, err := handle.Wait(waitCtx, e2b.WithWaitStdout(output.writeStdout), e2b.WithWaitStderr(output.writeStderr))
	stdout, stderr := output.stdout.String(), output.stderr.String()
	if output.overflow {
		return "", fmt.Errorf("command failed: output exceeded %d bytes", limit)
	}
	if err == nil {
		return stdout, nil
	}
	var exitErr *e2b.CommandExitError
	if errors.As(err, &exitErr) {
		return stdout, fcE2BSDKFailure(&fcE2BCommandExitError{ExitCode: result.ExitCode}, stdout, stderr)
	}
	return stdout, fcE2BSDKFailure(err, stdout, stderr)
}

// listTemplates reads GET /templates exactly as `e2b template list --format
// json` does and returns the same JSON. The SDK's typed TemplateInfo drops
// unknown fields and rewrites timestamps, which would change the template
// directory's parsing and ordering, so the response stays raw.
func (r SDKCommandRunner) listTemplates(ctx context.Context, creds fcE2BCredentials) (string, error) {
	if creds.APIKey == "" {
		return "", errors.New("command failed: FC/E2B API key is required")
	}
	base, err := url.Parse(creds.APIURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return "", errors.New("command failed: invalid FC/E2B API URL")
	}
	target := creds.APIURL + "/templates"
	// With E2B_API_KEY set the CLI only scopes the list by E2B_TEAM_ID.
	if teamID := os.Getenv("E2B_TEAM_ID"); teamID != "" {
		target += "?" + url.Values{"teamID": {teamID}}.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, fcE2BSDKTemplateListTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", errors.New("command failed: create FC/E2B template list request")
	}
	req.Header.Set("X-API-KEY", creds.APIKey)
	req.Header.Set("User-Agent", "e2b-go-sdk/"+e2b.Version)
	response, err := r.httpClient().Do(req)
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, fcE2BSDKMaxTemplateListBytes+1))
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
	}
	if len(body) > fcE2BSDKMaxTemplateListBytes {
		return "", fmt.Errorf("command failed: FC/E2B template list exceeded %d bytes", fcE2BSDKMaxTemplateListBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("command failed: FC/E2B template list returned HTTP %d: %s", response.StatusCode, redact.Text(string(body)))
	}
	return fcE2BSortTemplateAliases(body)
}

// fcE2BSortTemplateAliases applies the CLI's sortTemplatesAliases so the
// first non-default alias, which names the template, is chosen the same way.
func fcE2BSortTemplateAliases(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var templates []map[string]any
	if err := decoder.Decode(&templates); err != nil {
		// parseFCE2BTemplates reports the exact shape problem.
		return string(body), nil
	}
	for _, template := range templates {
		aliases, ok := template["aliases"].([]any)
		if !ok {
			continue
		}
		sortable := true
		for _, alias := range aliases {
			if _, ok := alias.(string); !ok {
				sortable = false
				break
			}
		}
		if sortable {
			sort.SliceStable(aliases, func(i, j int) bool { return aliases[i].(string) < aliases[j].(string) })
		}
	}
	encoded, err := json.Marshal(templates)
	if err != nil {
		return "", errors.New("command failed: encode FC/E2B template list")
	}
	return string(encoded), nil
}

// fcE2BCommandExitError mirrors exec.ExitError's text for a remote non-zero
// exit so existing error messages keep their shape.
type fcE2BCommandExitError struct {
	ExitCode int
}

func (e *fcE2BCommandExitError) Error() string {
	return "exit status " + strconv.Itoa(e.ExitCode)
}

// fcE2BSDKError keeps the cause for errors.Is/As while its text is redacted
// like OSCommandRunner's.
type fcE2BSDKError struct {
	cause error
	text  string
}

func (e *fcE2BSDKError) Error() string { return e.text }

func (e *fcE2BSDKError) Unwrap() error { return e.cause }

func fcE2BSDKFailure(cause error, stdout, stderr string) error {
	return &fcE2BSDKError{
		cause: cause,
		text:  "command failed: " + redact.Text(cause.Error()) + ": " + redact.Text(stdout+stderr),
	}
}

// fcE2BBoundedOutput collects command output up to limit bytes in total. On
// overflow it stops the wait, which closes the stream.
type fcE2BBoundedOutput struct {
	limit    int
	stdout   strings.Builder
	stderr   strings.Builder
	overflow bool
	exceeded context.CancelFunc
}

func (o *fcE2BBoundedOutput) writeStdout(chunk string) { o.write(&o.stdout, chunk) }

func (o *fcE2BBoundedOutput) writeStderr(chunk string) { o.write(&o.stderr, chunk) }

func (o *fcE2BBoundedOutput) write(target *strings.Builder, chunk string) {
	if o.overflow {
		return
	}
	if o.stdout.Len()+o.stderr.Len()+len(chunk) > o.limit {
		o.overflow = true
		o.exceeded()
		return
	}
	target.WriteString(chunk)
}
