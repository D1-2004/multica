package agentmessagerouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

var responseReceiptCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/([A-Za-z0-9_-]{1,128})/response-receipt$`)

// Durable intents are pinned to TargetIdentity. A live origin change must not
// move a queued write (and its credentials) to an unrelated Router deployment.
func (c *Client) responsePolicyClient() (*Client, error) {
	if c == nil || c.baseURL == nil {
		return nil, ErrNotConfigured
	}
	if c.baseURLProvider != nil {
		current, _, err := normalizeRouterBaseURL(c.baseURLProvider())
		if err != nil || current.String() != c.baseURL.String() {
			return nil, errors.New("agent message router response policy target changed; restart to bind the new target")
		}
	}
	pinned := *c
	pinned.baseURLProvider = nil
	return &pinned, nil
}

// SupportsResponsePolicy is intentionally separate from subscription writes so
// a rolling deployment cannot activate a mode unknown to an older Router.
func (c *Client) SupportsResponsePolicy(ctx context.Context) (bool, error) {
	pinned, err := c.responsePolicyClient()
	if err != nil {
		return false, err
	}
	response, err := pinned.do(ctx, http.MethodGet, "/api/subscriptions/response-policy-capabilities", nil)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, decodeRouterHTTPError(response.Body, response.StatusCode)
	}
	capabilities, err := decodeRouterResponse[struct {
		Versions []int    `json:"versions"`
		Modes    []string `json:"modes"`
	}](response.Body)
	if err != nil {
		return false, err
	}
	return slices.Contains(capabilities.Versions, protocol.DingTalkResponsePolicyVersion) &&
		slices.Contains(capabilities.Modes, protocol.DingTalkResponseModeCoordinator) &&
		slices.Contains(capabilities.Modes, protocol.DingTalkResponseModeLegacy), nil
}

func (c *Client) UpdateSubscriptionResponsePolicy(ctx context.Context, sourceID, agentID string, policy protocol.DingTalkResponsePolicy) (Subscription, error) {
	if !validRouterIdentifier(sourceID) || !isTrimmedNonEmpty(agentID) || !policy.Valid() {
		return Subscription{}, errors.New("agent message router response policy update is invalid")
	}
	body, err := json.Marshal(struct {
		AgentID        string                          `json:"agentId"`
		ResponsePolicy protocol.DingTalkResponsePolicy `json:"responsePolicy"`
	}{agentID, policy})
	if err != nil {
		return Subscription{}, err
	}
	pinned, err := c.responsePolicyClient()
	if err != nil {
		return Subscription{}, err
	}
	response, err := pinned.do(ctx, http.MethodPatch, "/api/subscriptions/"+url.PathEscape(sourceID)+"/response-policy", bytes.NewReader(body))
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
		result.ResponsePolicy == nil || *result.ResponsePolicy != policy {
		return Subscription{}, ErrRouterInvalidResponse
	}
	return result, nil
}

// NormalizeResponseReceiptCallback limits service-credential delivery to the
// configured Router and the response receipt endpoint. The returned path is
// suitable for durable storage and cannot override the Router's origin.
func (c *Client) NormalizeResponseReceiptCallback(raw string) (string, error) {
	if c == nil || c.baseURL == nil {
		return "", ErrNotConfigured
	}
	parsed, err := url.Parse(raw)
	if err != nil || raw != strings.TrimSpace(raw) || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Opaque != "" {
		return "", errors.New("agent message router response receipt callback is invalid")
	}
	path := parsed.Path
	if parsed.IsAbs() {
		base := c.baseURL
		if c.baseURLProvider != nil {
			base, _, err = normalizeRouterBaseURL(c.baseURLProvider())
			if err != nil {
				return "", err
			}
		}
		callbackOrigin, _, normalizeErr := normalizeRouterBaseURL(parsed.Scheme + "://" + parsed.Host)
		if normalizeErr != nil || callbackOrigin.Scheme != base.Scheme || callbackOrigin.Host != base.Host ||
			!strings.HasPrefix(path, base.Path+"/") {
			return "", errors.New("agent message router response receipt callback origin is invalid")
		}
		path = strings.TrimPrefix(path, base.Path)
	} else if parsed.Host != "" {
		return "", errors.New("agent message router response receipt callback origin is invalid")
	}
	if !responseReceiptCallbackPattern.MatchString(path) {
		return "", errors.New("agent message router response receipt callback path is invalid")
	}
	return path, nil
}

func (c *Client) SubmitResponseReceipt(ctx context.Context, callbackURL string, receipt protocol.DingTalkResponseReceipt) error {
	pinned, err := c.responsePolicyClient()
	if err != nil {
		return err
	}
	path, err := pinned.NormalizeResponseReceiptCallback(callbackURL)
	if err != nil {
		return err
	}
	if !isTrimmedNonEmpty(receipt.RequestID) || !isTrimmedNonEmpty(receipt.AgentID) ||
		!isTrimmedNonEmpty(receipt.ActionID) || receipt.OccurredAt <= 0 || !validResponseReceiptState(receipt.State) {
		return errors.New("agent message router response receipt is invalid")
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	response, err := pinned.do(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ExecutionResultDeliveryError{status: response.StatusCode}
	}
	ack, err := decodeRouterResponse[struct {
		DispatchTaskID string `json:"dispatchTaskId"`
		ActionID       string `json:"actionId"`
		State          string `json:"state"`
	}](response.Body)
	match := responseReceiptCallbackPattern.FindStringSubmatch(path)
	if err != nil || ack.DispatchTaskID != match[1] || ack.ActionID != receipt.ActionID || ack.State != receipt.State {
		return &ExecutionResultDeliveryError{status: response.StatusCode, code: "protocol_mismatch"}
	}
	return nil
}

func validResponseReceiptState(state string) bool {
	switch state {
	case "delivered", "silent", "failed", "cancelled", "unknown":
		return true
	default:
		return false
	}
}
