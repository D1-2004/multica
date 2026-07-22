package agentmessagerouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxRouterResponseBytes = 1 << 20

type ClientConfig struct {
	BaseURL           string
	ServiceCredential string
	HTTPClient        *http.Client
}

type Client struct {
	baseURL           *url.URL
	serviceCredential string
	httpClient        *http.Client
}

type BindingToken struct {
	BindingToken string    `json:"bindingToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type Subscription struct {
	SourceID    string               `json:"sourceId"`
	AgentID     string               `json:"agentId"`
	DispatchURL string               `json:"dispatchUrl"`
	Surface     SubscriptionSurface  `json:"surface"`
	Outbound    SubscriptionOutbound `json:"outbound"`
	Status      string               `json:"status"`
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
		"surface":               p.Surface,
		"outbound":              p.Outbound,
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
	ErrSubscriptionNotFound   = errors.New("agent message router subscription not found")
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
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" ||
		(baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("agent message router base url is invalid")
	}
	credential := strings.TrimSpace(config.ServiceCredential)
	if credential == "" || strings.ContainsAny(credential, " \t\r\n") {
		return nil, errors.New("agent message router service credential is required")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		baseURL:           baseURL,
		serviceCredential: credential,
		httpClient:        httpClient,
	}, nil
}

func (c *Client) IssueBindingToken(ctx context.Context, agentID, dispatchPath string) (BindingToken, error) {
	agentID = strings.TrimSpace(agentID)
	_, err := endpointIDFromDispatchPath(dispatchPath)
	if agentID == "" || err != nil {
		return BindingToken{}, errors.New("agent message router token issue request is invalid")
	}
	body, err := json.Marshal(map[string]string{
		"agentId":      agentID,
		"dispatchPath": dispatchPath,
	})
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
	if result.BindingToken == "" || result.ExpiresAt.IsZero() {
		return BindingToken{}, errors.New("agent message router token issue response is invalid")
	}
	return result, nil
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
		return Subscription{}, fmt.Errorf("agent message router subscription query failed with status %d", response.StatusCode)
	}
	result, err := decodeRouterResponse[Subscription](response.Body)
	if err != nil {
		return Subscription{}, err
	}
	if result.SourceID != sourceID || result.Status != "active" ||
		!isTrimmedNonEmpty(result.AgentID) || !isTrimmedNonEmpty(result.DispatchURL) ||
		!validSubscriptionSurface(result.Surface) || !validSubscriptionOutbound(result.Outbound) {
		return Subscription{}, errors.New("agent message router subscription response is invalid")
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

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return nil, errors.New("agent message router client is not configured")
	}
	target := *c.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, errors.New("create agent message router request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.serviceCredential)
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

func validSubscriptionSurface(surface SubscriptionSurface) bool {
	return surface.Type == "chat" || surface.Type == "issue"
}

func validSubscriptionOutbound(outbound SubscriptionOutbound) bool {
	return (outbound.Mode == "robot_sdk" || outbound.Mode == "dws") &&
		outbound.ReplyTo == "latest_message"
}
