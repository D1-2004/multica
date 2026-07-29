package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	asbAPIKeyHeader         = "OPEN-SANDBOX-API-KEY"
	asbExecPort             = 44772
	asbMinCreateTimeout     = 60
	asbMaxCreateTimeout     = 24 * 60 * 60
	asbMaxRenewalDuration   = 7 * 24 * time.Hour
	asbMaxResponseBodyBytes = 1 << 20
	asbMaxSSELineBytes      = 1 << 20
	asbMaxExecOutputBytes   = 8 << 20
)

var (
	ErrASBDisabled            = errors.New("ASB is not configured")
	ErrASBInvalidBaseURL      = errors.New("ASB API URL is invalid")
	ErrASBInvalidSandboxID    = errors.New("ASB sandbox ID is invalid")
	ErrASBInvalidEndpoint     = errors.New("ASB sandbox endpoint is invalid")
	ErrASBIncompleteExecution = errors.New("ASB command stream ended before completion")
	ErrASBOutputTooLarge      = errors.New("ASB command output exceeds the configured limit")

	asbSandboxIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
)

// ASBClientConfig contains only control-plane configuration. Tenant selection
// belongs in the ASB API URL or service credential; it is deliberately not
// copied into sandbox process environments.
type ASBClientConfig struct {
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type ASBClient struct {
	baseURL         *url.URL
	apiKey          string
	lifecycleClient *http.Client
	execClient      *http.Client
}

type ASBImageSpec struct {
	URI string `json:"uri"`
}

type ASBCreateSandboxInput struct {
	ImageURI       string
	TimeoutSeconds int
	ResourceCPU    string
	ResourceMemory string
	Env            map[string]string
	Metadata       map[string]string
	Entrypoint     []string
	Extensions     map[string]string
}

type asbCreateSandboxRequest struct {
	Image          ASBImageSpec      `json:"image"`
	Timeout        int               `json:"timeout"`
	ResourceLimits map[string]string `json:"resourceLimits"`
	Env            map[string]string `json:"env,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Entrypoint     []string          `json:"entrypoint"`
	Extensions     map[string]string `json:"extensions,omitempty"`
}

type ASBSandboxStatus struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type ASBSandbox struct {
	ID         string           `json:"id"`
	Status     ASBSandboxStatus `json:"status"`
	Entrypoint []string         `json:"entrypoint,omitempty"`
	CreatedAt  time.Time        `json:"createdAt"`
	ExpiresAt  *time.Time       `json:"expiresAt,omitempty"`
	Image      *ASBImageSpec    `json:"image,omitempty"`
}

type ASBEndpoint struct {
	URL     *url.URL
	Headers http.Header
}

type asbEndpointResponse struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type ASBExecInput struct {
	Command    string
	CWD        string
	Background bool
	Timeout    time.Duration
	UID        *int
	GID        *int
	Envs       map[string]string
}

type asbExecRequest struct {
	Command    string            `json:"command"`
	CWD        string            `json:"cwd,omitempty"`
	Background bool              `json:"background"`
	Timeout    int64             `json:"timeout,omitempty"`
	UID        *int              `json:"uid,omitempty"`
	GID        *int              `json:"gid,omitempty"`
	Envs       map[string]string `json:"envs,omitempty"`
}

type ASBExecResult struct {
	ExecutionID    string
	ExecutionCount *int
	Stdout         string
	Stderr         string
	Result         string
	ExitCode       *int
	ErrorName      string
	Completed      bool
}

type asbSSEEvent struct {
	Type                  string         `json:"type"`
	Text                  string         `json:"text,omitempty"`
	ExecutionCount        *int           `json:"execution_count,omitempty"`
	ExecutionTimeInMillis *int64         `json:"execution_time,omitempty"`
	Results               *asbSSEResults `json:"results,omitempty"`
	Error                 *asbSSEError   `json:"error,omitempty"`
}

type asbSSEResults struct {
	Text string `json:"text,omitempty"`
}

type asbSSEError struct {
	Name  string `json:"ename,omitempty"`
	Value string `json:"evalue,omitempty"`
}

type ASBAgentIdentityGrant struct {
	RawEmployeeID string `json:"rawEmpID"`
	AgentToken    string `json:"agentToken"`
	AgentID       string `json:"agentID"`
}

type ASBBUCIdentityGrant struct {
	EmployeeID           string `json:"empId"`
	BUCAccessToken       string `json:"bucAccessToken"`
	BUCRefreshToken      string `json:"bucRefreshToken"`
	BUCIDToken           string `json:"bucIdToken"`
	WireGuardCredentials string `json:"wgclientCredentials"`
	OriginalSandboxID    string `json:"originalSandboxId,omitempty"`
}

type ASBHTTPError struct {
	Operation  string
	StatusCode int
	RequestID  string
}

func (e *ASBHTTPError) Error() string {
	if e.RequestID == "" {
		return fmt.Sprintf("ASB %s failed with HTTP %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("ASB %s failed with HTTP %d (request_id=%s)", e.Operation, e.StatusCode, e.RequestID)
}

func NewASBClient(cfg ASBClientConfig) (*ASBClient, error) {
	rawBaseURL := strings.TrimSpace(cfg.BaseURL)
	if rawBaseURL == "" {
		return nil, ErrASBDisabled
	}
	baseURL, err := normalizeASBBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("ASB API key is required")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	lifecycleClient := cloneASBHTTPClient(cfg.HTTPClient)
	lifecycleClient.Timeout = timeout
	execClient := cloneASBHTTPClient(cfg.HTTPClient)
	// The command endpoint streams until the command completes. The caller's
	// context and the execd request timeout are the two authoritative limits.
	execClient.Timeout = 0

	return &ASBClient{
		baseURL:         baseURL,
		apiKey:          cfg.APIKey,
		lifecycleClient: lifecycleClient,
		execClient:      execClient,
	}, nil
}

func cloneASBHTTPClient(source *http.Client) *http.Client {
	var client http.Client
	if source != nil {
		client = *source
	}
	// Endpoint headers can contain credentials. Never follow a redirect and
	// replay them to a different destination.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func normalizeASBBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, ErrASBInvalidBaseURL
	}
	if strings.Contains(parsed.EscapedPath(), "%2f") || strings.Contains(parsed.EscapedPath(), "%2F") {
		return nil, ErrASBInvalidBaseURL
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		path = "/v1"
	} else if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed, nil
}

func (c *ASBClient) CreateSandbox(ctx context.Context, input ASBCreateSandboxInput) (*ASBSandbox, error) {
	if strings.TrimSpace(input.ImageURI) == "" {
		return nil, errors.New("ASB image URI is required")
	}
	if input.TimeoutSeconds < asbMinCreateTimeout || input.TimeoutSeconds > asbMaxCreateTimeout {
		return nil, fmt.Errorf(
			"ASB timeout must be between %d and %d seconds",
			asbMinCreateTimeout,
			asbMaxCreateTimeout,
		)
	}
	if strings.TrimSpace(input.ResourceCPU) == "" || strings.TrimSpace(input.ResourceMemory) == "" {
		return nil, errors.New("ASB CPU and memory limits are required")
	}
	if len(input.Entrypoint) == 0 || strings.TrimSpace(input.Entrypoint[0]) == "" {
		return nil, errors.New("ASB entrypoint is required")
	}
	if strings.TrimSpace(input.Extensions["wireguard.lazyAuth"]) != "" &&
		strings.TrimSpace(input.Extensions["wireguard.worker"]) != "" {
		return nil, errors.New("ASB wireguard.lazyAuth and wireguard.worker extensions are mutually exclusive")
	}

	request := asbCreateSandboxRequest{
		Image:   ASBImageSpec{URI: input.ImageURI},
		Timeout: input.TimeoutSeconds,
		ResourceLimits: map[string]string{
			"cpu":    input.ResourceCPU,
			"memory": input.ResourceMemory,
		},
		Env:        cloneStringMap(input.Env),
		Metadata:   cloneStringMap(input.Metadata),
		Entrypoint: append([]string(nil), input.Entrypoint...),
		Extensions: cloneStringMap(input.Extensions),
	}
	var sandbox ASBSandbox
	if err := c.doLifecycleJSON(ctx, "create_sandbox", http.MethodPost, "/sandboxes", nil, request, &sandbox, http.StatusCreated, http.StatusOK, http.StatusAccepted); err != nil {
		return nil, err
	}
	if err := validateASBSandboxResponse(&sandbox); err != nil {
		return nil, fmt.Errorf("ASB create_sandbox returned an invalid response: %w", err)
	}
	return &sandbox, nil
}

func (c *ASBClient) GetSandbox(ctx context.Context, sandboxID string) (*ASBSandbox, error) {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return nil, err
	}
	var sandbox ASBSandbox
	if err := c.doLifecycleJSON(ctx, "get_sandbox", http.MethodGet, "/sandboxes/"+sandboxID, nil, nil, &sandbox, http.StatusOK); err != nil {
		return nil, err
	}
	if err := validateASBSandboxResponse(&sandbox); err != nil {
		return nil, fmt.Errorf("ASB get_sandbox returned an invalid response: %w", err)
	}
	return &sandbox, nil
}

func (c *ASBClient) DeleteSandbox(ctx context.Context, sandboxID string) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	return c.doLifecycleJSON(
		ctx,
		"delete_sandbox",
		http.MethodDelete,
		"/sandboxes/"+sandboxID,
		nil,
		nil,
		nil,
		http.StatusOK,
		http.StatusAccepted,
		http.StatusNoContent,
		http.StatusNotFound,
	)
}

func (c *ASBClient) RenewSandbox(ctx context.Context, sandboxID string, expiresAt time.Time) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	now := time.Now()
	if !expiresAt.After(now) {
		return errors.New("ASB expiration must be in the future")
	}
	if expiresAt.Sub(now) > asbMaxRenewalDuration {
		return fmt.Errorf("ASB expiration cannot be more than %s in the future", asbMaxRenewalDuration)
	}
	request := struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}{ExpiresAt: expiresAt.UTC()}
	return c.doLifecycleJSON(
		ctx,
		"renew_sandbox",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/renew-expiration",
		nil,
		request,
		nil,
		http.StatusOK,
		http.StatusCreated,
	)
}

func (c *ASBClient) GetEndpoint(ctx context.Context, sandboxID string, port int) (*ASBEndpoint, error) {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return nil, err
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("ASB endpoint port is invalid")
	}
	var response asbEndpointResponse
	if err := c.doLifecycleJSON(
		ctx,
		"get_endpoint",
		http.MethodGet,
		fmt.Sprintf("/sandboxes/%s/endpoints/%d", sandboxID, port),
		nil,
		nil,
		&response,
		http.StatusOK,
	); err != nil {
		return nil, err
	}
	endpointURL, err := normalizeASBEndpoint(response.Endpoint, c.baseURL.Scheme)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header, len(response.Headers))
	for key, value := range response.Headers {
		if err := validateASBEndpointHeader(key, value); err != nil {
			return nil, err
		}
		headers.Set(key, value)
	}
	return &ASBEndpoint{URL: endpointURL, Headers: headers}, nil
}

func (c *ASBClient) AttachAgentIdentity(ctx context.Context, sandboxID string, grant ASBAgentIdentityGrant) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	if strings.TrimSpace(grant.RawEmployeeID) == "" ||
		strings.TrimSpace(grant.AgentToken) == "" ||
		strings.TrimSpace(grant.AgentID) == "" {
		return errors.New("ASB Agent Identity grant is incomplete")
	}
	query := url.Values{"sync": []string{"false"}}
	return c.doLifecycleJSON(
		ctx,
		"attach_agent_identity",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/identity/spiffe",
		query,
		grant,
		nil,
		http.StatusOK,
		http.StatusCreated,
		http.StatusAccepted,
		http.StatusNoContent,
	)
}

func (c *ASBClient) AttachBUCIdentity(ctx context.Context, sandboxID string, grant ASBBUCIdentityGrant, sync bool) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	if strings.TrimSpace(grant.EmployeeID) == "" ||
		strings.TrimSpace(grant.BUCAccessToken) == "" ||
		strings.TrimSpace(grant.BUCRefreshToken) == "" ||
		strings.TrimSpace(grant.BUCIDToken) == "" ||
		strings.TrimSpace(grant.WireGuardCredentials) == "" {
		return errors.New("ASB BUC identity grant is incomplete")
	}
	if grant.OriginalSandboxID != "" {
		if err := validateASBSandboxID(grant.OriginalSandboxID); err != nil {
			return err
		}
	}
	query := url.Values{"sync": []string{strconv.FormatBool(sync)}}
	return c.doLifecycleJSON(
		ctx,
		"attach_buc_identity",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/identity/wireguard",
		query,
		grant,
		nil,
		http.StatusOK,
		http.StatusCreated,
		http.StatusAccepted,
		http.StatusNoContent,
	)
}

func (c *ASBClient) Exec(ctx context.Context, endpoint *ASBEndpoint, input ASBExecInput) (*ASBExecResult, error) {
	if endpoint == nil || endpoint.URL == nil {
		return nil, ErrASBInvalidEndpoint
	}
	if strings.TrimSpace(input.Command) == "" {
		return nil, errors.New("ASB command is required")
	}
	if input.Timeout < 0 {
		return nil, errors.New("ASB command timeout cannot be negative")
	}
	if input.GID != nil && input.UID == nil {
		return nil, errors.New("ASB command UID is required when GID is set")
	}

	timeoutMillis := input.Timeout.Milliseconds()
	request := asbExecRequest{
		Command:    input.Command,
		CWD:        input.CWD,
		Background: input.Background,
		Timeout:    timeoutMillis,
		UID:        input.UID,
		GID:        input.GID,
		Envs:       cloneStringMap(input.Envs),
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode ASB exec request: %w", err)
	}

	commandURL := *endpoint.URL
	commandURL.Path = strings.TrimRight(commandURL.Path, "/") + "/command"
	commandURL.RawPath = ""
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, commandURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create ASB exec request: %w", err)
	}
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("Cache-Control", "no-cache")
	httpRequest.Header.Set("Content-Type", "application/json")
	for key, values := range endpoint.Headers {
		for _, value := range values {
			if err := validateASBEndpointHeader(key, value); err != nil {
				return nil, err
			}
			httpRequest.Header.Add(key, value)
		}
	}
	// A lifecycle API key is never valid on the sandbox endpoint.
	httpRequest.Header.Del(asbAPIKeyHeader)

	response, err := c.execClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("ASB exec request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, newASBHTTPError("exec", response)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "text/event-stream") {
		return nil, errors.New("ASB exec returned an unexpected content type")
	}

	result, err := decodeASBExecStream(response.Body)
	if err != nil {
		return nil, err
	}
	if !input.Background && result.ExitCode == nil {
		if result.Completed {
			exitCode := 0
			result.ExitCode = &exitCode
		} else {
			return nil, ErrASBIncompleteExecution
		}
	}
	return result, nil
}

func decodeASBExecStream(reader io.Reader) (*ASBExecResult, error) {
	result := &ASBExecResult{}
	var stdout, stderr, resultText strings.Builder
	totalOutput := 0

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), asbMaxSSELineBytes)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" ||
			strings.HasPrefix(line, ":") ||
			strings.HasPrefix(line, "event:") ||
			strings.HasPrefix(line, "id:") ||
			strings.HasPrefix(line, "retry:") {
			continue
		}
		data := line
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if data == "" {
			continue
		}

		var event asbSSEEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, errors.New("ASB exec returned a malformed SSE event")
		}
		switch event.Type {
		case "ping":
			// Execd emits application-level heartbeat events before and during
			// commands. They carry no command output or completion semantics.
			continue
		case "init":
			result.ExecutionID = event.Text
		case "stdout":
			if err := appendASBOutput(&stdout, event.Text, &totalOutput); err != nil {
				return nil, err
			}
		case "stderr":
			if err := appendASBOutput(&stderr, event.Text, &totalOutput); err != nil {
				return nil, err
			}
		case "result":
			if event.Results != nil {
				if err := appendASBOutput(&resultText, event.Results.Text, &totalOutput); err != nil {
					return nil, err
				}
			}
		case "error":
			if event.Error != nil {
				result.ErrorName = event.Error.Name
				exitCode := -1
				if parsed, err := strconv.Atoi(strings.TrimSpace(event.Error.Value)); err == nil {
					exitCode = parsed
				}
				result.ExitCode = &exitCode
			}
		case "execution_complete":
			result.Completed = true
		case "execution_count":
			result.ExecutionCount = event.ExecutionCount
		default:
			return nil, fmt.Errorf("ASB exec returned unsupported event type %q", event.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) || strings.Contains(err.Error(), "token too long") {
			return nil, ErrASBOutputTooLarge
		}
		return nil, fmt.Errorf("read ASB exec stream: %w", err)
	}

	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.Result = resultText.String()
	return result, nil
}

func appendASBOutput(builder *strings.Builder, value string, total *int) error {
	if len(value) > asbMaxExecOutputBytes-*total {
		return ErrASBOutputTooLarge
	}
	_, _ = builder.WriteString(value)
	*total += len(value)
	return nil
}

func (c *ASBClient) doLifecycleJSON(
	ctx context.Context,
	operation string,
	method string,
	requestPath string,
	query url.Values,
	input any,
	output any,
	acceptedStatusCodes ...int,
) error {
	if c == nil || c.baseURL == nil {
		return ErrASBDisabled
	}

	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode ASB %s request: %w", operation, err)
		}
		body = bytes.NewReader(encoded)
	}
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(c.baseURL.Path, "/") + requestPath
	requestURL.RawPath = ""
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return fmt.Errorf("create ASB %s request: %w", operation, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set(asbAPIKeyHeader, c.apiKey)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.lifecycleClient.Do(request)
	if err != nil {
		return fmt.Errorf("ASB %s request failed: %w", operation, err)
	}
	defer response.Body.Close()
	if !containsStatus(acceptedStatusCodes, response.StatusCode) {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, asbMaxResponseBodyBytes))
		return newASBHTTPError(operation, response)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, asbMaxResponseBodyBytes))
		return nil
	}

	limited := io.LimitReader(response.Body, asbMaxResponseBodyBytes+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read ASB %s response: %w", operation, err)
	}
	if len(encoded) > asbMaxResponseBodyBytes {
		return fmt.Errorf("ASB %s response exceeds %d bytes", operation, asbMaxResponseBodyBytes)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("decode ASB %s response: invalid JSON", operation)
	}
	return nil
}

func newASBHTTPError(operation string, response *http.Response) error {
	requestID := response.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = response.Header.Get("X-Aone-Request-Id")
	}
	return &ASBHTTPError{
		Operation:  operation,
		StatusCode: response.StatusCode,
		RequestID:  sanitizeASBRequestID(requestID),
	}
}

func sanitizeASBRequestID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	var cleaned strings.Builder
	for _, character := range value {
		if character >= 0x21 && character <= 0x7e {
			cleaned.WriteRune(character)
		}
	}
	return cleaned.String()
}

func normalizeASBEndpoint(raw, defaultScheme string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrASBInvalidEndpoint
	}
	if !strings.Contains(raw, "://") {
		raw = defaultScheme + "://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return nil, ErrASBInvalidEndpoint
	}
	return parsed, nil
}

func validateASBEndpointHeader(key, value string) error {
	key = http.CanonicalHeaderKey(strings.TrimSpace(key))
	if key == "" || strings.ContainsAny(value, "\r\n") {
		return ErrASBInvalidEndpoint
	}
	switch key {
	case asbAPIKeyHeader,
		"Connection",
		"Content-Length",
		"Host",
		"Proxy-Authorization",
		"Proxy-Authenticate",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade":
		return fmt.Errorf("%w: unsafe endpoint header", ErrASBInvalidEndpoint)
	}
	return nil
}

func validateASBSandboxID(sandboxID string) error {
	if !asbSandboxIDPattern.MatchString(sandboxID) {
		return ErrASBInvalidSandboxID
	}
	return nil
}

func validateASBSandboxResponse(sandbox *ASBSandbox) error {
	if sandbox == nil || validateASBSandboxID(sandbox.ID) != nil {
		return ErrASBInvalidSandboxID
	}
	if strings.TrimSpace(sandbox.Status.State) == "" {
		return errors.New("sandbox status is missing")
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func containsStatus(accepted []int, actual int) bool {
	for _, status := range accepted {
		if status == actual {
			return true
		}
	}
	return false
}
