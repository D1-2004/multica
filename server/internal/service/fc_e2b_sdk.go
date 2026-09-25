package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	e2b "github.com/aliyun-fc/e2b-go-sdk"

	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	// The e2b CLI attaches to a sandbox for every `sandbox exec` through
	// POST /sandboxes/{id}/connect with the JS SDK default lease of 300s.
	// That call is not a read-only attach. Sending the same body keeps the
	// sandbox lease behaviour of every exec unchanged.
	fcE2BSDKConnectTimeoutSeconds = e2b.DefaultSandboxTimeoutSeconds
	// fcE2BSDKMaxOutputBytes bounds the stdout one command may return. The
	// SDK accumulates the whole stream internally, so exceeding the bound stops
	// reading and fails explicitly instead of truncating a receipt. Callers
	// accept at most 64 KiB of receipt.
	fcE2BSDKMaxOutputBytes = 4 << 20
	// fcE2BSDKMaxStderrBytes bounds the stderr one command may write. The CLI
	// kept all of it, so this bound only guards memory and sits far above any
	// progress output. Below it, stderr volume never fails a command.
	fcE2BSDKMaxStderrBytes = 32 << 20
	// fcE2BSDKStderrTailBytes is the end of stderr kept for the error text.
	fcE2BSDKStderrTailBytes = 256 << 10
	// fcE2BSDKMaxTemplateListBytes bounds the template list response.
	fcE2BSDKMaxTemplateListBytes = 8 << 20
	// fcE2BSDKMaxCreateResponseBytes bounds the create response.
	fcE2BSDKMaxCreateResponseBytes = 64 << 10
	// fcE2BAPIRequestTimeout is the CLI's control-plane request timeout.
	fcE2BAPIRequestTimeout = 60 * time.Second
	// fcE2BCLIMinSandboxTimeoutSeconds is the e2b CLI create floor.
	fcE2BCLIMinSandboxTimeoutSeconds = 30
)

// fcE2BShellSafeArg is the e2b CLI's SHELL_SAFE_RE: arguments made only of
// these characters are passed to bash unquoted.
var fcE2BShellSafeArg = regexp.MustCompile(`\A[A-Za-z0-9_@%+=:,./-]+\z`)

var fcE2BSDKHTTPClient = newFCE2BSDKHTTPClient()

func newFCE2BSDKHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The CLI's undici agent never reads HTTP(S)_PROXY; neither does this.
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 16
	// No client-wide timeout: command streams stay open for the command's
	// lifetime. The caller context and the SDK request timeout bound them.
	return &http.Client{Transport: transport, CheckRedirect: fcE2BSameHostRedirect}
}

// fcE2BSameHostRedirect follows redirects within one host only. Go drops the
// Authorization header on a cross-host redirect but would forward X-API-KEY
// and envd's X-Access-Token, so such a redirect is returned unfollowed and
// fails as a non-2xx response.
func fcE2BSameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

// SDKCommandRunner performs the FC/E2B operations the launcher builds in
// process instead of forking the e2b CLI. It accepts exactly the argv shapes
// Multica produces (sandbox create, sandbox exec, template list) and decodes
// them into typed requests. Any other shape is rejected rather than
// approximated. Exec runs through the Go SDK's connect and envd command
// stream; create and template list send the CLI's exact control-plane
// requests. Credentials come from the E2B_* entries of env, the same values
// the CLI received; request-specific values never enter shared state.
type SDKCommandRunner struct {
	// HTTPClient carries control-plane and envd traffic. Nil uses a shared
	// client so connections are pooled across calls.
	HTTPClient *http.Client
	// maxOutputBytes, maxStderrBytes and stderrTailBytes override the output
	// bounds in tests.
	maxOutputBytes  int
	maxStderrBytes  int
	stderrTailBytes int
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
	switch {
	case operation.listTemplates:
		return r.listTemplates(ctx, creds)
	case operation.create != nil:
		return r.create(ctx, creds, *operation.create)
	}
	client, err := r.client(creds)
	if err != nil {
		return "", fcE2BSDKFailure(err, "", "")
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
	// The CLI never checked the key's format; the FC control plane decides.
	options := []e2b.Option{e2b.WithAPIKey(creds.APIKey), e2b.WithValidateAPIKey(false), e2b.WithHTTPClient(r.httpClient())}
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

// create posts the body `e2b sandbox create` sends. The SDK's CreateSandbox
// always adds empty metadata and envVars objects, which the CLI omits, so the
// control-plane request is built here.
func (r SDKCommandRunner) create(ctx context.Context, creds fcE2BCredentials, request fcE2BCreateRequest) (string, error) {
	timeoutSeconds := request.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = e2b.DefaultSandboxTimeoutSeconds
	}
	body := map[string]any{
		"templateID":            request.Template,
		"timeout":               timeoutSeconds,
		"secure":                true,
		"allow_internet_access": true,
		"autoPause":             request.OnTimeout == "pause",
		"autoResume":            map[string]bool{"enabled": false},
	}
	status, payload, err := r.controlPlane(ctx, creds, http.MethodPost, "/sandboxes", nil, body, fcE2BSDKMaxCreateResponseBytes)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fcE2BSDKFailure(fcE2BAPIError(status, payload), "", "")
	}
	var created struct {
		SandboxID   string `json:"sandboxID"`
		EnvdVersion string `json:"envdVersion"`
	}
	if json.Unmarshal(payload, &created) != nil || !fcE2BAPISandboxIDPattern.MatchString(created.SandboxID) {
		return "", errors.New("command failed: FC/E2B sandbox create returned no sandbox id")
	}
	if compareFCE2BVersion(created.EnvdVersion, "0.1.0") < 0 {
		// The CLI removes a sandbox whose envd predates the SDK protocol.
		_, _, _ = r.controlPlane(context.WithoutCancel(ctx), creds, http.MethodDelete, "/sandboxes/"+created.SandboxID, nil, nil, fcE2BSDKMaxCreateResponseBytes)
		return "", errors.New("command failed: You need to update the template to use the new SDK.")
	}
	// Keep the create receipt the launcher already parses.
	return fmt.Sprintf("Sandbox created with ID %s using template %s\n", created.SandboxID, request.Template), nil
}

// controlPlane sends one request to the E2B API with the CLI's credentials:
// X-API-KEY, plus a bearer token when E2B_ACCESS_TOKEN is set.
func (r SDKCommandRunner) controlPlane(ctx context.Context, creds fcE2BCredentials, method, path string, query url.Values, body any, limit int64) (int, []byte, error) {
	if creds.APIKey == "" {
		return 0, nil, errors.New("command failed: FC/E2B API key is required")
	}
	base, err := url.Parse(creds.APIURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return 0, nil, errors.New("command failed: invalid FC/E2B API URL")
	}
	target := creds.APIURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, errors.New("command failed: encode FC/E2B request")
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, fcE2BAPIRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, errors.New("command failed: create FC/E2B request")
	}
	req.Header.Set("X-API-KEY", creds.APIKey)
	if token := os.Getenv("E2B_ACCESS_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", "e2b-go-sdk/"+e2b.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := r.httpClient().Do(req)
	if err != nil {
		return 0, nil, fcE2BSDKFailure(err, "", "")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return 0, nil, fcE2BSDKFailure(err, "", "")
	}
	if int64(len(payload)) > limit {
		return 0, nil, fmt.Errorf("command failed: FC/E2B response exceeded %d bytes", limit)
	}
	return response.StatusCode, payload, nil
}

// fcE2BAPIError mirrors the JS SDK's handleApiError text, which the CLI
// printed for a rejected request.
func fcE2BAPIError(status int, payload []byte) error {
	content := strings.TrimSpace(string(payload))
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &parsed) == nil && parsed.Message != "" {
		content = parsed.Message
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("Unauthorized, please check your credentials. - %s", content)
	case http.StatusTooManyRequests:
		return fmt.Errorf("Rate limit exceeded, please try again later - %s", content)
	}
	if content == "" {
		content = http.StatusText(status)
	}
	return fmt.Errorf("%d: %s", status, content)
}

// compareFCE2BVersion compares dotted numeric versions like compare-versions.
func compareFCE2BVersion(a, b string) int {
	left, right := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < max(len(left), len(right)); i++ {
		var l, r int
		if i < len(left) {
			l, _ = strconv.Atoi(strings.SplitN(left[i], "-", 2)[0])
		}
		if i < len(right) {
			r, _ = strconv.Atoi(strings.SplitN(right[i], "-", 2)[0])
		}
		if l != r {
			if l < r {
				return -1
			}
			return 1
		}
	}
	return 0
}

func (r SDKCommandRunner) exec(ctx context.Context, client *e2b.Client, request fcE2BExecRequest) (string, error) {
	sandbox, err := client.ConnectSandbox(ctx, request.SandboxID, fcE2BSDKConnectTimeoutSeconds)
	if err != nil {
		return "", fcE2BSDKFailure(fcE2BContextCause(ctx, err), "", "")
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
			return "", fcE2BSDKFailure(fcE2BContextCause(ctx, err), "", "")
		}
		// Detach only after envd reported the process start. Disconnect does
		// not stop the remote process.
		_ = handle.Disconnect()
		return "", nil
	}

	waitCtx, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()
	output := &fcE2BBoundedOutput{
		stdoutLimit: positiveOr(r.maxOutputBytes, fcE2BSDKMaxOutputBytes),
		stderrLimit: positiveOr(r.maxStderrBytes, fcE2BSDKMaxStderrBytes),
		stderrTail:  positiveOr(r.stderrTailBytes, fcE2BSDKStderrTailBytes),
		exceeded:    stopWaiting,
	}
	handle, err := sandbox.Commands.Start(waitCtx, request.Command, options...)
	if err != nil {
		return "", fcE2BSDKFailure(fcE2BContextCause(ctx, err), "", "")
	}
	// Cancelling the wait closes the local stream only; like killing the CLI,
	// it does not stop the remote command.
	defer func() { _ = handle.Disconnect() }()
	result, err := handle.Wait(waitCtx, e2b.WithWaitStdout(output.writeStdout), e2b.WithWaitStderr(output.writeStderr))
	if output.overflow != nil {
		return "", output.overflow
	}
	stdout, stderr := output.stdout.String(), output.stderrText()
	if err == nil {
		return stdout, nil
	}
	var exitErr *e2b.CommandExitError
	if errors.As(err, &exitErr) {
		// The CLI printed envd's end error after the command's stderr and
		// exited with the remote code, truncated to 8 bits by the OS.
		if result.Error != "" {
			stderr += result.Error + "\n"
		}
		return stdout, fcE2BSDKFailure(&fcE2BCommandExitError{ExitCode: result.ExitCode & 0xff}, stdout, stderr)
	}
	return stdout, fcE2BSDKFailure(err, stdout, stderr)
}

// listTemplates reads GET /templates exactly as `e2b template list --format
// json` does and returns the same JSON. The SDK's typed TemplateInfo drops
// unknown fields and rewrites timestamps, which would change the template
// directory's parsing and ordering, so the response stays raw.
func (r SDKCommandRunner) listTemplates(ctx context.Context, creds fcE2BCredentials) (string, error) {
	// With E2B_API_KEY set the CLI only scopes the list by E2B_TEAM_ID.
	var query url.Values
	if teamID := os.Getenv("E2B_TEAM_ID"); teamID != "" {
		query = url.Values{"teamID": {teamID}}
	}
	status, body, err := r.controlPlane(ctx, creds, http.MethodGet, "/templates", query, nil, fcE2BSDKMaxTemplateListBytes)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fcE2BSDKFailure(fcE2BAPIError(status, body), "", "")
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

// fcE2BContextCause keeps a cancelled or expired caller context visible to
// errors.Is. The SDK reports a context that ends during connect or start as
// its own request timeout.
func fcE2BContextCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
		return fmt.Errorf("%w (%v)", ctxErr, err)
	}
	return err
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// fcE2BOutputLimitError reports a stream that outgrew its bound.
type fcE2BOutputLimitError struct {
	stream string
	limit  int
}

func (e *fcE2BOutputLimitError) Error() string {
	return fmt.Sprintf("command failed: %s exceeded %d bytes", e.stream, e.limit)
}

// fcE2BBoundedOutput collects stdout up to stdoutLimit bytes and keeps the
// last stderrTail bytes of stderr, up to stderrLimit bytes in total. The
// callbacks run on the Wait goroutine only. On overflow it stops the wait,
// which closes the stream.
type fcE2BBoundedOutput struct {
	stdoutLimit int
	stderrLimit int
	stderrTail  int
	stdout      strings.Builder
	stderr      []byte
	stderrTotal int
	overflow    error
	exceeded    context.CancelFunc
}

func (o *fcE2BBoundedOutput) writeStdout(chunk string) {
	if o.overflow != nil {
		return
	}
	if o.stdout.Len()+len(chunk) > o.stdoutLimit {
		o.stop("stdout", o.stdoutLimit)
		return
	}
	o.stdout.WriteString(chunk)
}

func (o *fcE2BBoundedOutput) writeStderr(chunk string) {
	if o.overflow != nil {
		return
	}
	o.stderrTotal += len(chunk)
	if o.stderrTotal > o.stderrLimit {
		o.stop("stderr", o.stderrLimit)
		return
	}
	o.stderr = append(o.stderr, chunk...)
	if extra := len(o.stderr) - o.stderrTail; extra > 0 {
		kept := o.stderr[extra:]
		for len(kept) > 0 && !utf8.RuneStart(kept[0]) {
			kept = kept[1:]
		}
		o.stderr = append(o.stderr[:0], kept...)
	}
}

func (o *fcE2BBoundedOutput) stop(stream string, limit int) {
	o.overflow = &fcE2BOutputLimitError{stream: stream, limit: limit}
	o.exceeded()
}

// stderrText is the kept stderr, marked when its beginning was dropped.
func (o *fcE2BBoundedOutput) stderrText() string {
	if omitted := o.stderrTotal - len(o.stderr); omitted > 0 {
		return fmt.Sprintf("[first %d bytes of stderr omitted]\n", omitted) + string(o.stderr)
	}
	return string(o.stderr)
}
