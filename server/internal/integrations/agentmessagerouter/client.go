package agentmessagerouter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxRouterResponseBytes          = 1 << 20
	maxBindingDisplayNameCodePoints = 256
)

type ClientConfig struct {
	BaseURL           string
	BaseURLProvider   func() string
	ServiceCredential string
	HTTPClient        *http.Client
}

type Client struct {
	baseURL           *url.URL
	baseURLProvider   func() string
	targetIdentity    string
	serviceCredential string
	httpClient        *http.Client
}

type BindingToken struct {
	BindingToken string    `json:"bindingToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type WorkspaceDescriptor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AgentDescriptor struct {
	AgentID      string              `json:"agentId"`
	Name         string              `json:"name"`
	Workspace    WorkspaceDescriptor `json:"workspace"`
	DispatchPath string              `json:"dispatchPath"`
}

type Subscription struct {
	SourceID    string               `json:"sourceId"`
	AgentID     string               `json:"agentId"`
	DispatchURL string               `json:"dispatchUrl"`
	Surface     SubscriptionSurface  `json:"surface"`
	Outbound    SubscriptionOutbound `json:"outbound"`
	Status      string               `json:"status"`
}

type DigitalEmployeeBindingKey struct {
	AgentID         string   `json:"agentId"`
	Platform        string   `json:"platform"`
	TenantID        string   `json:"tenantId"`
	AccountID       string   `json:"accountId"`
	ExpectedDomains []string `json:"expectedDomains,omitempty"`
}

type DigitalEmployeeBindingCheck struct {
	AgentID        string `json:"agentId"`
	Platform       string `json:"platform"`
	TenantID       string `json:"tenantId"`
	AccountID      string `json:"accountId"`
	Status         string `json:"status"`
	CurrentAgentID string `json:"currentAgentId,omitempty"`
}

type digitalEmployeeBindingCheckResponse struct {
	Bindings []DigitalEmployeeBindingCheck `json:"bindings"`
}

type DigitalEmployeeBindingUnbindResult struct {
	Status string `json:"status"`
}

type DigitalEmployeeSourceIdentity struct {
	SourceID   string `json:"sourceId"`
	Platform   string `json:"platform"`
	TenantID   string `json:"tenantId"`
	AccountID  string `json:"accountId"`
	SourceType string `json:"sourceType"`
	Domain     string `json:"domain"`
}

type DigitalEmployeeSourceIdentityResult struct {
	Sources          []DigitalEmployeeSourceIdentity `json:"sources"`
	MissingSourceIDs []string                        `json:"missingSourceIds"`
}

type SubscriptionSurface struct {
	Type string `json:"type"`
}

type SubscriptionOutbound struct {
	Mode    string `json:"mode"`
	ReplyTo string `json:"replyTo"`
}

type AgentDeliveryTarget struct {
	AgentID     string `json:"agentId"`
	DispatchURL string `json:"dispatchUrl"`
}

type RobotRegistration struct {
	TenantID               string               `json:"tenantId,omitempty"`
	RobotCode              string               `json:"robotCode"`
	ClientID               string               `json:"clientId"`
	ClientSecret           string               `json:"clientSecret"`
	AgentID                string               `json:"agentId"`
	DispatchURL            string               `json:"dispatchUrl"`
	Surface                SubscriptionSurface  `json:"surface"`
	Outbound               SubscriptionOutbound `json:"outbound"`
	ReplaceExistingBinding bool                 `json:"replaceExistingBinding,omitempty"`
}

type CreateSubscriptionParams struct {
	TenantID           string
	AccountID          string
	AgentID            string
	DispatchURL        string
	BindingToken       string
	SubscriptionConfig map[string]any
	Surface            SubscriptionSurface
	Outbound           SubscriptionOutbound
	ReplaceExisting    bool
}

func (c *Client) CreateHTTPCallbackSubscription(ctx context.Context, p CreateSubscriptionParams) (Subscription, error) {
	if !validSubscriptionSurface(p.Surface) || !validSubscriptionOutbound(p.Outbound) {
		return Subscription{}, errors.New("agent message router subscription dispatch policy is invalid")
	}
	body, err := json.Marshal(map[string]any{
		"source": map[string]any{
			"platform": "dingtalk", "domain": "channel",
			"tenantId":           strings.TrimSpace(p.TenantID),
			"accountId":          strings.TrimSpace(p.AccountID),
			"subscriptionConfig": p.SubscriptionConfig,
		},
		"agent": map[string]string{
			"agentId":     strings.TrimSpace(p.AgentID),
			"dispatchUrl": strings.TrimSpace(p.DispatchURL),
		},
		"surface":                p.Surface,
		"outbound":               p.Outbound,
		"bindingToken":           strings.TrimSpace(p.BindingToken),
		"enabledDomains":         []string{"channel"},
		"replaceExistingBinding": p.ReplaceExisting,
	})
	if err != nil {
		return Subscription{}, errors.New("encode HTTP callback subscription request")
	}
	response, err := c.do(ctx, http.MethodPost, "/api/subscriptions", bytes.NewReader(body))
	if err != nil {
		return Subscription{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Subscription{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[Subscription](response.Body)
	if err != nil {
		return Subscription{}, err
	}
	if result.Status != "active" || !isTrimmedNonEmpty(result.SourceID) ||
		result.AgentID != strings.TrimSpace(p.AgentID) || result.DispatchURL != strings.TrimSpace(p.DispatchURL) ||
		result.Surface != p.Surface || result.Outbound != p.Outbound {
		return Subscription{}, errors.New("agent message router subscription response is invalid")
	}
	return result, nil
}

type routerResponse[T any] struct {
	Success bool   `json:"success"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    *T     `json:"data"`
}

type routerErrorResponse struct {
	Code string `json:"code"`
}

var (
	ErrRouterAPI              = errors.New("agent message router returned a business error")
	ErrRouterInvalidResponse  = errors.New("agent message router response is invalid")
	ErrSubscriptionNotFound   = errors.New("agent message router subscription not found")
	ErrSubscriptionDrift      = errors.New("agent message router subscription does not match the requested source")
	ErrDeliveryTargetNotFound = errors.New("agent message router delivery target not found")
)

type RouterAPIError struct {
	Code                 string
	subscriptionNotFound bool
}

func (e *RouterAPIError) Error() string {
	if e == nil || e.Code == "" {
		return ErrRouterAPI.Error()
	}
	return "agent message router returned a business error (" + e.Code + ")"
}

func (e *RouterAPIError) Is(target error) bool {
	if target == ErrRouterAPI {
		return true
	}
	if target == ErrDeliveryTargetNotFound {
		return e != nil && e.Code == "delivery_target_not_found"
	}
	return target == ErrSubscriptionNotFound && e != nil && e.subscriptionNotFound
}

func NewClient(config ClientConfig) (*Client, error) {
	rawBaseURL := config.BaseURL
	if config.BaseURLProvider != nil {
		rawBaseURL = config.BaseURLProvider()
	}
	baseURL, canonicalTarget, err := normalizeRouterBaseURL(rawBaseURL)
	if err != nil {
		return nil, err
	}
	credential := strings.TrimSpace(config.ServiceCredential)
	if credential == "" || strings.ContainsAny(credential, " \t\r\n") {
		return nil, errors.New("agent message router service credential is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	} else {
		cloned := *httpClient
		httpClient = &cloned
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	targetDigest := sha256.Sum256([]byte(canonicalTarget))
	return &Client{
		baseURL:           baseURL,
		baseURLProvider:   config.BaseURLProvider,
		targetIdentity:    fmt.Sprintf("router-target:v1:sha256:%x", targetDigest),
		serviceCredential: credential,
		httpClient:        httpClient,
	}, nil
}

func normalizeRouterBaseURL(raw string) (*url.URL, string, error) {
	baseURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" ||
		baseURL.RawPath != "" {
		return nil, "", errors.New("agent message router base url is invalid")
	}
	scheme := strings.ToLower(baseURL.Scheme)
	hostname := strings.ToLower(baseURL.Hostname())
	port := baseURL.Port()
	if hostname == "" {
		return nil, "", errors.New("agent message router base url is invalid")
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	basePath := strings.TrimRight(baseURL.Path, "/")
	if basePath == "/" {
		basePath = ""
	}
	if basePath != "" &&
		(!strings.HasPrefix(basePath, "/") || pathpkg.Clean(basePath) != basePath) {
		return nil, "", errors.New("agent message router base url is invalid")
	}
	baseURL.Scheme = scheme
	baseURL.Host = host
	baseURL.Path = basePath
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	canonicalTarget := scheme + "://" + host + basePath
	return baseURL, canonicalTarget, nil
}

func (c *Client) TargetIdentity() string {
	if c == nil {
		return ""
	}
	return c.targetIdentity
}

func (c *Client) IssueBindingToken(ctx context.Context, descriptor AgentDescriptor) (BindingToken, error) {
	descriptor, err := normalizeAgentDescriptor(descriptor)
	if err != nil {
		return BindingToken{}, fmt.Errorf("agent message router token issue request is invalid: %w", err)
	}
	body, err := json.Marshal(descriptor)
	if err != nil {
		return BindingToken{}, errors.New("encode account binding token request")
	}
	response, err := c.do(ctx, http.MethodPost, "/api/account-binding-tokens", bytes.NewReader(body))
	if err != nil {
		return BindingToken{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return BindingToken{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeDirectRouterResponse[BindingToken](response.Body)
	if err != nil {
		return BindingToken{}, err
	}
	if !isTrimmedNonEmpty(result.BindingToken) || result.ExpiresAt.IsZero() {
		return BindingToken{}, errors.New("agent message router token issue response is invalid")
	}
	return result, nil
}

func (c *Client) CheckDigitalEmployeeBindings(
	ctx context.Context,
	bindings []DigitalEmployeeBindingKey,
) ([]DigitalEmployeeBindingCheck, error) {
	if len(bindings) == 0 {
		return []DigitalEmployeeBindingCheck{}, nil
	}
	for _, binding := range bindings {
		if !validDigitalEmployeeBindingKey(binding, true) {
			return nil, errors.New("agent message router digital employee binding check request is invalid")
		}
	}
	body, err := json.Marshal(map[string]any{"bindings": bindings})
	if err != nil {
		return nil, errors.New("encode agent message router digital employee binding check request")
	}
	response, err := c.do(ctx, http.MethodPost, "/api/digital-employee-bindings/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[digitalEmployeeBindingCheckResponse](response.Body)
	if err != nil {
		return nil, err
	}
	if len(result.Bindings) != len(bindings) {
		return nil, ErrRouterInvalidResponse
	}
	for index, item := range result.Bindings {
		requested := bindings[index]
		if item.AgentID != requested.AgentID || item.Platform != requested.Platform ||
			item.TenantID != requested.TenantID || item.AccountID != requested.AccountID ||
			!validDigitalEmployeeBindingCheck(item) {
			return nil, ErrRouterInvalidResponse
		}
	}
	return result.Bindings, nil
}

func (c *Client) UnbindDigitalEmployeeBinding(
	ctx context.Context,
	binding DigitalEmployeeBindingKey,
) (DigitalEmployeeBindingUnbindResult, error) {
	binding.ExpectedDomains = nil
	if !validDigitalEmployeeBindingKey(binding, false) {
		return DigitalEmployeeBindingUnbindResult{}, errors.New("agent message router digital employee unbind request is invalid")
	}
	body, err := json.Marshal(binding)
	if err != nil {
		return DigitalEmployeeBindingUnbindResult{}, errors.New("encode agent message router digital employee unbind request")
	}
	response, err := c.do(ctx, http.MethodPost, "/api/digital-employee-bindings/unbind", bytes.NewReader(body))
	if err != nil {
		return DigitalEmployeeBindingUnbindResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return DigitalEmployeeBindingUnbindResult{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[DigitalEmployeeBindingUnbindResult](response.Body)
	if err != nil {
		return DigitalEmployeeBindingUnbindResult{}, err
	}
	if result.Status != "unbound" && result.Status != "ownership_changed" && result.Status != "inconsistent" {
		return DigitalEmployeeBindingUnbindResult{}, ErrRouterInvalidResponse
	}
	return result, nil
}

func (c *Client) GetDigitalEmployeeSourceIdentities(
	ctx context.Context,
	sourceIDs []string,
) (DigitalEmployeeSourceIdentityResult, error) {
	if len(sourceIDs) == 0 {
		return DigitalEmployeeSourceIdentityResult{}, nil
	}
	requested := make(map[string]struct{}, len(sourceIDs))
	unique := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if !validRouterIdentifier(sourceID) {
			return DigitalEmployeeSourceIdentityResult{}, errors.New("agent message router source identity request is invalid")
		}
		if _, exists := requested[sourceID]; exists {
			continue
		}
		requested[sourceID] = struct{}{}
		unique = append(unique, sourceID)
	}
	body, err := json.Marshal(map[string]any{"sourceIds": unique})
	if err != nil {
		return DigitalEmployeeSourceIdentityResult{}, errors.New("encode agent message router source identity request")
	}
	response, err := c.do(ctx, http.MethodPost, "/api/digital-employee-bindings/source-identities", bytes.NewReader(body))
	if err != nil {
		return DigitalEmployeeSourceIdentityResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return DigitalEmployeeSourceIdentityResult{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[DigitalEmployeeSourceIdentityResult](response.Body)
	if err != nil {
		return DigitalEmployeeSourceIdentityResult{}, err
	}
	accounted := make(map[string]struct{}, len(unique))
	for _, source := range result.Sources {
		if _, exists := requested[source.SourceID]; !exists || !validRouterIdentifier(source.SourceID) {
			return DigitalEmployeeSourceIdentityResult{}, ErrRouterInvalidResponse
		}
		if _, duplicate := accounted[source.SourceID]; duplicate {
			return DigitalEmployeeSourceIdentityResult{}, ErrRouterInvalidResponse
		}
		accounted[source.SourceID] = struct{}{}
	}
	for _, sourceID := range result.MissingSourceIDs {
		if _, exists := requested[sourceID]; !exists || !validRouterIdentifier(sourceID) {
			return DigitalEmployeeSourceIdentityResult{}, ErrRouterInvalidResponse
		}
		if _, duplicate := accounted[sourceID]; duplicate {
			return DigitalEmployeeSourceIdentityResult{}, ErrRouterInvalidResponse
		}
		accounted[sourceID] = struct{}{}
	}
	if len(accounted) != len(unique) {
		return DigitalEmployeeSourceIdentityResult{}, ErrRouterInvalidResponse
	}
	return result, nil
}

func normalizeAgentDescriptor(descriptor AgentDescriptor) (AgentDescriptor, error) {
	descriptor.AgentID = strings.TrimSpace(descriptor.AgentID)
	descriptor.Workspace.ID = strings.TrimSpace(descriptor.Workspace.ID)
	descriptor.DispatchPath = strings.TrimSpace(descriptor.DispatchPath)
	workspaceID, workspaceIDErr := uuid.Parse(descriptor.Workspace.ID)
	_, dispatchPathErr := endpointIDFromDispatchPath(descriptor.DispatchPath)
	if descriptor.AgentID == "" || workspaceIDErr != nil ||
		workspaceID.String() != descriptor.Workspace.ID || dispatchPathErr != nil {
		return AgentDescriptor{}, errors.New("agent binding descriptor identifiers are invalid")
	}
	var err error
	descriptor.Name, err = normalizeBindingDisplayName("agent name", descriptor.Name)
	if err != nil {
		return AgentDescriptor{}, err
	}
	descriptor.Workspace.Name, err = normalizeBindingDisplayName("workspace name", descriptor.Workspace.Name)
	if err != nil {
		return AgentDescriptor{}, err
	}
	return descriptor, nil
}

func normalizeBindingDisplayName(field, value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("agent binding descriptor has invalid %s: empty", field)
	}
	if !utf8.ValidString(normalized) {
		return "", fmt.Errorf("agent binding descriptor has invalid %s: invalid UTF-8", field)
	}
	if utf8.RuneCountInString(normalized) > maxBindingDisplayNameCodePoints {
		return "", fmt.Errorf("agent binding descriptor has invalid %s: exceeds 256 code points", field)
	}
	for _, codePoint := range normalized {
		if codePoint <= '\u001f' || (codePoint >= '\u007f' && codePoint <= '\u009f') {
			return "", fmt.Errorf("agent binding descriptor has invalid %s: contains a control character", field)
		}
	}
	return normalized, nil
}

func (c *Client) GetAgentDeliveryTarget(ctx context.Context, agentID string) (AgentDeliveryTarget, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return AgentDeliveryTarget{}, errors.New("agent message router delivery target agent id is required")
	}
	response, err := c.do(ctx, http.MethodGet, "/api/agent-delivery-targets/"+url.PathEscape(agentID), nil)
	if err != nil {
		return AgentDeliveryTarget{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return AgentDeliveryTarget{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeDirectRouterResponse[AgentDeliveryTarget](response.Body)
	if err != nil {
		return AgentDeliveryTarget{}, err
	}
	if result.AgentID != agentID || !isTrimmedNonEmpty(result.DispatchURL) {
		return AgentDeliveryTarget{}, errors.New("agent message router delivery target response is invalid")
	}
	return result, nil
}

func (c *Client) RegisterRobot(ctx context.Context, registration RobotRegistration) (Subscription, error) {
	registration.TenantID = strings.TrimSpace(registration.TenantID)
	if !isTrimmedNonEmpty(registration.RobotCode) || !isTrimmedNonEmpty(registration.ClientID) ||
		!isTrimmedNonEmpty(registration.ClientSecret) || !isTrimmedNonEmpty(registration.AgentID) ||
		!isTrimmedNonEmpty(registration.DispatchURL) || !validSubscriptionSurface(registration.Surface) ||
		!validSubscriptionOutbound(registration.Outbound) {
		return Subscription{}, errors.New("agent message router robot registration is invalid")
	}
	body, err := json.Marshal(registration)
	if err != nil {
		return Subscription{}, errors.New("encode agent message router robot registration")
	}
	response, err := c.do(ctx, http.MethodPost, "/api/subscriptions/robots", bytes.NewReader(body))
	if err != nil {
		return Subscription{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Subscription{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[Subscription](response.Body)
	if err != nil {
		return Subscription{}, err
	}
	if !isTrimmedNonEmpty(result.SourceID) || result.AgentID != registration.AgentID ||
		result.DispatchURL != registration.DispatchURL || result.Surface != registration.Surface ||
		result.Outbound != registration.Outbound || result.Status != "active" {
		return Subscription{}, errors.New("agent message router robot registration response is invalid")
	}
	return result, nil
}

func (c *Client) GetSubscription(ctx context.Context, sourceID string) (Subscription, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return Subscription{}, errors.New("agent message router subscription source id is required")
	}
	response, err := c.do(ctx, http.MethodGet, "/api/subscriptions/"+url.PathEscape(sourceID), nil)
	if err != nil {
		return Subscription{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusNotFound {
			return Subscription{}, ErrSubscriptionNotFound
		}
		return Subscription{}, fmt.Errorf("agent message router subscription query failed with status %d", response.StatusCode)
	}
	result, err := decodeRouterResponse[Subscription](response.Body)
	if err != nil {
		if errors.Is(err, ErrRouterAPI) {
			return Subscription{}, err
		}
		return Subscription{}, ErrRouterInvalidResponse
	}
	if !isTrimmedNonEmpty(result.SourceID) || !isTrimmedNonEmpty(result.AgentID) || !isTrimmedNonEmpty(result.DispatchURL) ||
		!validSubscriptionSurface(result.Surface) || !validSubscriptionOutbound(result.Outbound) {
		return Subscription{}, ErrRouterInvalidResponse
	}
	if result.SourceID != sourceID || result.Status != "active" {
		return result, ErrSubscriptionDrift
	}
	return result, nil
}

func (c *Client) UpdateSubscriptionSurface(
	ctx context.Context,
	sourceID, agentID, surfaceType string,
) (Subscription, error) {
	sourceID = strings.TrimSpace(sourceID)
	agentID = strings.TrimSpace(agentID)
	surface := SubscriptionSurface{Type: strings.TrimSpace(surfaceType)}
	if sourceID == "" || agentID == "" || !validSubscriptionSurface(surface) {
		return Subscription{}, errors.New("agent message router subscription surface update is invalid")
	}
	body, err := json.Marshal(map[string]any{
		"agentId": agentID,
		"surface": surface,
	})
	if err != nil {
		return Subscription{}, errors.New("encode agent message router subscription surface update")
	}
	response, err := c.do(
		ctx,
		http.MethodPatch,
		"/api/subscriptions/"+url.PathEscape(sourceID)+"/surface",
		bytes.NewReader(body),
	)
	if err != nil {
		return Subscription{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Subscription{}, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[Subscription](response.Body)
	if err != nil {
		return Subscription{}, err
	}
	if result.SourceID != sourceID || result.AgentID != agentID || result.Status != "active" ||
		!isTrimmedNonEmpty(result.DispatchURL) || result.Surface != surface ||
		!validSubscriptionOutbound(result.Outbound) {
		return Subscription{}, errors.New("agent message router subscription surface response is invalid")
	}
	return result, nil
}

func (c *Client) DeleteSubscription(ctx context.Context, sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return errors.New("agent message router subscription source id is required")
	}
	response, err := c.do(ctx, http.MethodDelete, "/api/subscriptions/"+url.PathEscape(sourceID), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("agent message router subscription delete failed with status %d", response.StatusCode)
	}
	result, err := decodeRouterResponse[Subscription](response.Body)
	if err != nil {
		return err
	}
	if result.SourceID != sourceID || result.Status != "inactive" {
		return errors.New("agent message router subscription delete response is invalid")
	}
	if (result.AgentID == "") != (result.DispatchURL == "") {
		return errors.New("agent message router subscription delete response is invalid")
	}
	if result.AgentID != "" && (!isTrimmedNonEmpty(result.AgentID) || !isTrimmedNonEmpty(result.DispatchURL)) {
		return errors.New("agent message router subscription delete response is invalid")
	}
	return nil
}

func (c *Client) DeleteDigitalEmployeeSubscriptions(ctx context.Context, agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent message router digital employee agent id is required")
	}
	response, err := c.do(
		ctx,
		http.MethodDelete,
		"/api/subscriptions/digital-employees/"+url.PathEscape(agentID),
		nil,
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	result, err := decodeRouterResponse[[]Subscription](response.Body)
	if err != nil {
		return err
	}
	for _, subscription := range result {
		if !isTrimmedNonEmpty(subscription.SourceID) || subscription.AgentID != agentID ||
			subscription.Status != "inactive" {
			return errors.New("agent message router digital employee delete response is invalid")
		}
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.doWithBearer(ctx, method, path, c.serviceCredential, body)
}

func (c *Client) doWithBearer(
	ctx context.Context,
	method string,
	path string,
	bearer string,
	body io.Reader,
) (*http.Response, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return nil, errors.New("agent message router client is not configured")
	}
	baseURL := c.baseURL
	if c.baseURLProvider != nil {
		resolved, _, err := normalizeRouterBaseURL(c.baseURLProvider())
		if err != nil {
			return nil, err
		}
		baseURL = resolved
	}
	target := *baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, errors.New("create agent message router request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, errors.New("agent message router request failed")
	}
	return response, nil
}

func decodeRouterResponse[T any](reader io.Reader) (T, error) {
	var zero T
	body, err := readRouterResponseBody(reader)
	if err != nil {
		return zero, errors.New("agent message router response is invalid")
	}
	var response routerResponse[T]
	if err := json.Unmarshal(body, &response); err != nil {
		return zero, errors.New("agent message router response is invalid")
	}
	if !response.Success {
		return zero, newRouterAPIError(response.Code, response.Message)
	}
	if response.Code != "success" || response.Data == nil {
		return zero, errors.New("agent message router response is invalid")
	}
	return *response.Data, nil
}

func decodeDirectRouterResponse[T any](reader io.Reader) (T, error) {
	var response T
	body, err := readRouterResponseBody(reader)
	if err != nil || json.Unmarshal(body, &response) != nil {
		var zero T
		return zero, errors.New("agent message router response is invalid")
	}
	return response, nil
}

func decodeRouterHTTPError(reader io.Reader, status int) error {
	body, err := readRouterResponseBody(reader)
	if err == nil {
		var response routerErrorResponse
		if json.Unmarshal(body, &response) == nil {
			return &RouterAPIError{Code: safeRouterErrorCode(response.Code)}
		}
	}
	return fmt.Errorf("agent message router request failed with status %d", status)
}

func readRouterResponseBody(reader io.Reader) ([]byte, error) {
	limited := io.LimitReader(reader, maxRouterResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > maxRouterResponseBytes {
		return nil, errors.New("agent message router response is invalid")
	}
	return body, nil
}

func newRouterAPIError(code, message string) error {
	safeCode := safeRouterErrorCode(code)
	return &RouterAPIError{
		Code:                 safeCode,
		subscriptionNotFound: safeCode == "business_error" && message == "subscription_not_found",
	}
}

func safeRouterErrorCode(code string) string {
	if len(code) == 0 || len(code) > 64 {
		return "unknown"
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return "unknown"
		}
	}
	return code
}

func isTrimmedNonEmpty(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func validRouterIdentifier(value string) bool {
	return isTrimmedNonEmpty(value) && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func validDigitalEmployeeBindingKey(binding DigitalEmployeeBindingKey, requireDomains bool) bool {
	if !validRouterIdentifier(binding.AgentID) || binding.Platform != "dingtalk" ||
		!validRouterIdentifier(binding.TenantID) || !validRouterIdentifier(binding.AccountID) {
		return false
	}
	if !requireDomains {
		return len(binding.ExpectedDomains) == 0
	}
	_, err := normalizeBindingDomains(binding.ExpectedDomains)
	return len(binding.ExpectedDomains) > 0 && err == nil
}

func validDigitalEmployeeBindingCheck(result DigitalEmployeeBindingCheck) bool {
	if !validRouterIdentifier(result.AgentID) || result.Platform != "dingtalk" ||
		!validRouterIdentifier(result.TenantID) || !validRouterIdentifier(result.AccountID) {
		return false
	}
	switch result.Status {
	case "valid", "unbound", "inconsistent":
		return result.CurrentAgentID == ""
	case "bound_to_other_agent":
		return validRouterIdentifier(result.CurrentAgentID) && result.CurrentAgentID != result.AgentID
	default:
		return false
	}
}

func validSubscriptionSurface(surface SubscriptionSurface) bool {
	return surface.Type == DingTalkSurfaceChat ||
		surface.Type == DingTalkSurfaceIssue ||
		surface.Type == DingTalkSurfaceAuto
}

func validSubscriptionOutbound(outbound SubscriptionOutbound) bool {
	return (outbound.Mode == "robot_sdk" || outbound.Mode == "dws") &&
		outbound.ReplyTo == "latest_message"
}
