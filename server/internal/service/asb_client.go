package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/startupobs"
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
	CapacityGate    *ASBCapacityGate
	baseURL         *url.URL
	apiKey          string
	capacityLockKey int32
	lifecycleClient *http.Client
	identityClient  *http.Client
	execClient      *http.Client
}

type ASBImageSpec struct {
	URI string `json:"uri"`
}

type ASBCreateSandboxInput struct {
	AllowedRegions []string // Local scheduling constraint; never sent as an ASB extension.
	NetworkPolicy  ASBNetworkPolicy
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
	NetworkPolicy  ASBNetworkPolicy  `json:"networkPolicy"`
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
	ID         string            `json:"id"`
	Status     ASBSandboxStatus  `json:"status"`
	Entrypoint []string          `json:"entrypoint,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	ExpiresAt  *time.Time        `json:"expiresAt,omitempty"`
	Image      *ASBImageSpec     `json:"image,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Extensions map[string]string `json:"extensions,omitempty"`
}

type asbSandboxPagination struct {
	Page        int  `json:"page"`
	PageSize    int  `json:"pageSize"`
	TotalItems  int  `json:"totalItems"`
	TotalPages  int  `json:"totalPages"`
	HasNextPage bool `json:"hasNextPage"`
}

type asbSandboxPage struct {
	Items      []ASBSandbox          `json:"items"`
	Pagination *asbSandboxPagination `json:"pagination"`
}

type asbSandboxListFilter struct {
	States []string
}

type ASBSandboxQuota struct {
	NetworkZone        string `json:"networkZone"`
	Region             string `json:"region"`
	Quota              int    `json:"quota"`
	Usage              int    `json:"usage"`
	AlertPercentage    *int   `json:"alertPercentage,omitempty"`
	VolumeSizeQuotaGiB *int64 `json:"volumeSizeQuotaGib,omitempty"`
	VolumeUsageGiB     int64  `json:"volumeUsageGib"`
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
	BUCAccessToken       string `json:"bucAccessToken,omitempty"`
	BUCRefreshToken      string `json:"bucRefreshToken,omitempty"`
	BUCIDToken           string `json:"bucIdToken,omitempty"`
	WireGuardCredentials string `json:"wgclientCredentials"`
	OriginalSandboxID    string `json:"originalSandboxId,omitempty"`
}

type ASBHTTPError struct {
	identityReason *asbIdentityFailureReason
	RetryAfter     time.Duration
	Operation      string
	StatusCode     int
	RequestID      string
	ErrorCode      string
	ErrorMessage   string
}

func (e *ASBHTTPError) Error() string {
	if e.RequestID == "" {
		return fmt.Sprintf("ASB %s failed with HTTP %d", e.Operation, e.StatusCode)
	}
	return fmt.Sprintf("ASB %s failed with HTTP %d (request_id=%s)", e.Operation, e.StatusCode, e.RequestID)
}

func (e *ASBHTTPError) runtimeStartUserDetail() string {
	if e == nil {
		return ""
	}
	diagnostics := make([]string, 0, 2)
	if e.identityReason != nil {
		return e.identityReason.detail + " " + e.Error()
	}
	if e.ErrorCode != "" {
		diagnostics = append(diagnostics, "code="+e.ErrorCode)
	}
	if e.ErrorMessage != "" {
		diagnostics = append(diagnostics, "message="+e.ErrorMessage)
	}
	if len(diagnostics) == 0 {
		return e.Error()
	}
	return e.Error() + ": " + strings.Join(diagnostics, "; ")
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
	identityClient := cloneASBHTTPClient(cfg.HTTPClient)
	// A synchronous BUC identity attachment can outlive the generic lifecycle
	// timeout while ASB checks the sandbox-side WireGuard state. The caller's
	// context is the authoritative deadline for this one request.
	identityClient.Timeout = 0
	execClient := cloneASBHTTPClient(cfg.HTTPClient)
	// The command endpoint streams until the command completes. The caller's
	// context and the execd request timeout are the two authoritative limits.
	execClient.Timeout = 0

	return &ASBClient{
		baseURL:         baseURL,
		apiKey:          cfg.APIKey,
		capacityLockKey: asbAPIKeyCapacityLockKey(cfg.APIKey),
		lifecycleClient: lifecycleClient,
		identityClient:  identityClient,
		execClient:      execClient,
	}, nil
}

func asbAPIKeyCapacityLockKey(apiKey string) int32 {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return int32(binary.BigEndian.Uint32(sum[:4]))
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

func (c *ASBClient) CreateSandbox(ctx context.Context, input ASBCreateSandboxInput) (result *ASBSandbox, resultErr error) {
	finish := startupobs.Start(ctx, "asb_create")
	defer func() { finish(resultErr) }()
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

	// An omitted policy is fail-closed at the lowest creation boundary.
	if input.NetworkPolicy.DefaultAction == "" {
		input.NetworkPolicy.DefaultAction = "deny"
	}
	if input.NetworkPolicy.DefaultAction != "deny" {
		return nil, errors.New("ASB network policy must default to deny")
	}
	if input.NetworkPolicy.Egress == nil {
		input.NetworkPolicy.Egress = []ASBNetworkRule{}
	}
	for _, rule := range input.NetworkPolicy.Egress {
		if rule.Action != "allow" || strings.TrimSpace(rule.Target) == "" {
			return nil, errors.New("ASB network rules must explicitly allow targets")
		}
		if !isASBManagedNetworkFamily(rule.Target) {
			if _, err := NormalizeASBNetworkTargets([]string{rule.Target}); err != nil {
				return nil, err
			}
		}
	}
	request := asbCreateSandboxRequest{
		NetworkPolicy: input.NetworkPolicy,
		Image:         ASBImageSpec{URI: input.ImageURI},
		Timeout:       input.TimeoutSeconds,
		ResourceLimits: map[string]string{
			"cpu":    input.ResourceCPU,
			"memory": input.ResourceMemory,
		},
		Env:        cloneStringMap(input.Env),
		Metadata:   cloneStringMap(input.Metadata),
		Entrypoint: append([]string(nil), input.Entrypoint...),
		Extensions: cloneStringMap(input.Extensions),
	}
	if request.Metadata == nil {
		request.Metadata = map[string]string{}
	}
	request.Metadata[asbNetworkPolicyFingerprintKey] = input.NetworkPolicy.Fingerprint()
	var sandbox ASBSandbox
	if err := c.doLifecycleJSON(ctx, "create_sandbox", http.MethodPost, "/sandboxes", nil, request, &sandbox, http.StatusCreated, http.StatusOK, http.StatusAccepted); err != nil {
		return nil, err
	}
	if err := validateASBSandboxResponse(&sandbox); err != nil {
		return nil, fmt.Errorf("ASB create_sandbox returned an invalid response: %w", err)
	}
	return &sandbox, nil
}

// ListQuotas checks the tenant API key against the read-only quota endpoint
// and returns its current sandbox allocation. It does not consume a slot.
func (c *ASBClient) ListQuotas(ctx context.Context) ([]ASBSandboxQuota, error) {
	var quotas []ASBSandboxQuota
	err := c.doLifecycleJSON(
		ctx,
		"list_quotas",
		http.MethodGet,
		"/sandboxes/quotas",
		nil,
		nil,
		&quotas,
		http.StatusOK,
	)
	if err != nil {
		return nil, err
	}
	return quotas, nil
}

// ListSandboxes returns the current control-plane state for every sandbox
// visible to the tenant API key.
func (c *ASBClient) ListSandboxes(ctx context.Context, states ...string) ([]ASBSandbox, error) {
	return c.listSandboxes(ctx, asbSandboxListFilter{States: states})
}

// ListLiveSandboxes explicitly queries every lifecycle state that can still
// consume tenant capacity. The hosted OpenSandbox API uses the same title-case
// state enums returned by lifecycle responses. It accepts only one effective
// state per request, so merge the paginated results locally.
func (c *ASBClient) ListLiveSandboxes(ctx context.Context) ([]ASBSandbox, error) {
	states := []string{"Pending", "Running", "Paused"}
	sandboxes := make([]ASBSandbox, 0)
	seen := make(map[string]struct{})
	for _, state := range states {
		stateSandboxes, err := c.ListSandboxes(ctx, state)
		if err != nil {
			return nil, fmt.Errorf("list %s ASB sandboxes: %w", strings.ToLower(state), err)
		}
		for _, sandbox := range stateSandboxes {
			if _, exists := seen[sandbox.ID]; exists {
				continue
			}
			seen[sandbox.ID] = struct{}{}
			sandboxes = append(sandboxes, sandbox)
		}
	}
	return sandboxes, nil
}

func (c *ASBClient) listSandboxes(
	ctx context.Context,
	filter asbSandboxListFilter,
) ([]ASBSandbox, error) {
	const pageSize = 100
	var sandboxes []ASBSandbox
	for page := 1; ; page++ {
		query := url.Values{
			"page":     {strconv.Itoa(page)},
			"pageSize": {strconv.Itoa(pageSize)},
		}
		for _, state := range filter.States {
			if normalized := strings.TrimSpace(state); normalized != "" {
				query.Add("state", normalized)
			}
		}
		var response asbSandboxPage
		if err := c.doLifecycleJSON(
			ctx,
			"list_sandboxes",
			http.MethodGet,
			"/sandboxes",
			query,
			nil,
			&response,
			http.StatusOK,
		); err != nil {
			return nil, err
		}
		if response.Pagination == nil ||
			response.Pagination.Page != page ||
			response.Pagination.PageSize <= 0 {
			return nil, errors.New("ASB list_sandboxes returned invalid pagination")
		}
		for index := range response.Items {
			if err := validateASBSandboxResponse(&response.Items[index]); err != nil {
				return nil, fmt.Errorf(
					"ASB list_sandboxes returned an invalid sandbox: %w",
					err,
				)
			}
		}
		sandboxes = append(sandboxes, response.Items...)
		if !response.Pagination.HasNextPage {
			return sandboxes, nil
		}
	}
}

// ValidateCredential checks the key again at the persistence boundary.
func (c *ASBClient) ValidateCredential(ctx context.Context) error {
	_, err := c.ListQuotas(ctx)
	return err
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

func (c *ASBClient) PauseSandbox(ctx context.Context, sandboxID string) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	return c.doLifecycleJSON(
		ctx,
		"pause_sandbox",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/pause",
		nil,
		nil,
		nil,
		http.StatusOK,
		http.StatusAccepted,
		http.StatusNoContent,
	)
}

func (c *ASBClient) ResumeSandbox(ctx context.Context, sandboxID string) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	return c.doLifecycleJSON(
		ctx,
		"resume_sandbox",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/resume",
		nil,
		nil,
		nil,
		http.StatusOK,
		http.StatusAccepted,
		http.StatusNoContent,
	)
}

func (c *ASBClient) RenewSandbox(
	ctx context.Context,
	sandboxID string,
	expiresAt time.Time,
	force bool,
) error {
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
	var query url.Values
	if force {
		query = url.Values{"force": []string{"true"}}
	}
	return c.doLifecycleJSON(
		ctx,
		"renew_sandbox",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/renew-expiration",
		query,
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
	// ASB explicitly recommends asynchronous SPIFFE attachment. A synchronous
	// request waits inside the control plane and can surface its transient CSI
	// 502 as a terminal HTTP 400 even though the attachment may still converge.
	// Employee identity is optional, so the launcher submits this request only
	// after the runner command and never gates task startup on its completion.
	query := url.Values{"sync": []string{"false"}}
	return c.doLifecycleJSONVia(
		ctx,
		c.lifecycleClient,
		"attach_agent_identity",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/identity/spiffe",
		query,
		grant,
		nil,
		http.StatusAccepted,
	)
}

func (c *ASBClient) AttachBUCIdentity(ctx context.Context, sandboxID string, grant ASBBUCIdentityGrant) error {
	if err := validateASBSandboxID(sandboxID); err != nil {
		return err
	}
	if strings.TrimSpace(grant.EmployeeID) == "" ||
		strings.TrimSpace(grant.WireGuardCredentials) == "" {
		return errors.New("ASB BUC identity grant is incomplete")
	}
	accessToken := strings.TrimSpace(grant.BUCAccessToken)
	refreshToken := strings.TrimSpace(grant.BUCRefreshToken)
	idToken := strings.TrimSpace(grant.BUCIDToken)
	if accessToken == "" || refreshToken == "" || idToken == "" {
		return errors.New("ASB BUC identity token trio is required")
	}
	if strings.TrimSpace(grant.OriginalSandboxID) != "" {
		return errors.New("ASB BUC identity attach does not inherit a source sandbox")
	}
	// Keep ASB's synchronous attach semantics. The caller classifies the known
	// false-negative post-attach HTTP 400 and then proves the effective employee
	// identity with an in-sandbox BUC probe.
	query := url.Values{"sync": []string{"true"}}
	return c.doLifecycleJSONVia(
		ctx,
		c.identityClient,
		"attach_buc_identity",
		http.MethodPost,
		"/sandboxes/"+sandboxID+"/identity/wireguard",
		query,
		grant,
		nil,
		http.StatusOK,
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
		requestErr := fmt.Errorf("ASB exec request failed: %w", err)
		return nil, withRuntimeStartUserDetail(requestErr, requestErr.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		encoded, _ := io.ReadAll(io.LimitReader(response.Body, asbMaxResponseBodyBytes))
		return nil, newASBHTTPError("exec", response, encoded)
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
	return c.doLifecycleJSONVia(
		ctx,
		c.lifecycleClient,
		operation,
		method,
		requestPath,
		query,
		input,
		output,
		acceptedStatusCodes...,
	)
}

func (c *ASBClient) doLifecycleJSONVia(
	ctx context.Context,
	httpClient *http.Client,
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
	if httpClient == nil {
		return errors.New("ASB HTTP client is unavailable")
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

	response, err := httpClient.Do(request)
	if err != nil {
		requestErr := fmt.Errorf("ASB %s request failed: %w", operation, err)
		return withRuntimeStartUserDetail(requestErr, requestErr.Error())
	}
	defer response.Body.Close()
	if !containsStatus(acceptedStatusCodes, response.StatusCode) {
		encoded, _ := io.ReadAll(io.LimitReader(response.Body, asbMaxResponseBodyBytes))
		return newASBHTTPError(operation, response, encoded)
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

func newASBHTTPError(operation string, response *http.Response, encoded []byte) error {
	requestID := response.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = response.Header.Get("X-Aone-Request-Id")
	}
	var payload struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Reason  string          `json:"reason"`
		Error   json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(encoded, &payload)
	if len(payload.Error) > 0 {
		var nested struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal(payload.Error, &nested) == nil {
			if payload.Code == "" {
				payload.Code = nested.Code
			}
			if payload.Message == "" {
				payload.Message = nested.Message
			}
			if payload.Reason == "" {
				payload.Reason = nested.Reason
			}
		} else if payload.Message == "" {
			var errorText string
			if json.Unmarshal(payload.Error, &errorText) == nil {
				payload.Message = errorText
			}
		}
	}
	if payload.Message == "" {
		payload.Message = payload.Reason
	}
	var identityReason *asbIdentityFailureReason
	if operation == "attach_buc_identity" {
		// Extract the diagnostic signature before truncating the long wgclient
		// exception. The root cause often follows the tunnel-check traceback.
		identityReason = asbIdentityReason(payload.Code + " " + payload.Message)
		payload.Message = sanitizeASBIdentityDetail(payload.Message)
	}
	return &ASBHTTPError{
		identityReason: identityReason,
		RetryAfter:     parseASBRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		Operation:      operation,
		StatusCode:     response.StatusCode,
		RequestID:      sanitizeASBDiagnosticValue(requestID, 128),
		ErrorCode:      sanitizeASBDiagnosticValue(payload.Code, 128),
		ErrorMessage:   sanitizeASBDiagnosticValue(payload.Message, 512),
	}
}

func parseASBRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds > 0 {
			return time.Duration(min(seconds, int64((24*time.Hour)/time.Second))) * time.Second
		}
		return 0
	}
	if retryAt, err := http.ParseTime(value); err == nil && retryAt.After(now) {
		return min(retryAt.Sub(now), 24*time.Hour)
	}
	return 0
}

func sanitizeASBDiagnosticValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit > 0 && len(value) > limit {
		value = value[:limit]
	}
	var cleaned strings.Builder
	for _, character := range value {
		if character >= 0x20 && character != 0x7f {
			cleaned.WriteRune(character)
		}
	}
	return strings.TrimSpace(cleaned.String())
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
