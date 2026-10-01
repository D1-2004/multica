package agentmessagerouter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Native DWS subscriptions make Multica its own router: an execution
// identity's IM events enter the same DispatchCommand v2 pipeline as Router
// deliveries, under dispatch tasks Multica names itself. Their callbacks
// never leave the process. Replies are managed responses (the frozen
// response_route and response_action outbox), so the callback "Router" only
// acknowledges.

// NativeDispatchTaskPrefix marks the dispatch tasks Multica creates for
// native subscription events. The wire never supplies it.
const NativeDispatchTaskPrefix = "dwsn-"

var nativeTargetIdentity = fmt.Sprintf("router-target:v1:sha256:%x", sha256.Sum256([]byte("multica-dws-native:v1")))

var nativeCallbackPattern = regexp.MustCompile(`^/api/v1/dispatch-tasks/dwsn-[a-f0-9]{40}/(execution-result|execution-update|response-receipt)$`)

// NativeTargetIdentity is the completion target of native dispatches. It has
// the Router target form, so the callback outboxes route it unchanged.
func NativeTargetIdentity() string { return nativeTargetIdentity }

// IsNativeDispatchCallback reports whether a callback path belongs to a
// native dispatch task.
func IsNativeDispatchCallback(callbackPath string) bool {
	return nativeCallbackPattern.MatchString(callbackPath)
}

// NativeDispatchTaskID names the dispatch task of one native message. It is
// stable across redeliveries of the same message.
func NativeDispatchTaskID(agentID, orgID, conversationID, messageID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{agentID, orgID, conversationID, messageID}, "\x00")))
	return NativeDispatchTaskPrefix + hex.EncodeToString(sum[:20])
}

// ExecutionCallbackClient submits execution callbacks for one completion
// target. *Client is the Router; *NativeCallbackClient serves native
// dispatches.
type ExecutionCallbackClient interface {
	TargetIdentity() string
	SubmitExecutionResult(context.Context, string, ExecutionResultRequest) (*DWSDelivery, error)
	SubmitExecutionUpdate(context.Context, string, ExecutionUpdateRequest) (*DWSDelivery, error)
}

// NativeRouteLookup reports whether a managed response route is frozen for a
// callback path.
type NativeRouteLookup func(ctx context.Context, callbackPath string) (bool, error)

// NativeCallbackClient acknowledges native execution callbacks. By the time
// it is called, ResponseActions has already handed any reply to the managed
// response outbox, so it never returns a DWS delivery plan.
type NativeCallbackClient struct {
	routes NativeRouteLookup
}

func NewNativeCallbackClient(routes NativeRouteLookup) *NativeCallbackClient {
	return &NativeCallbackClient{routes: routes}
}

func (c *NativeCallbackClient) TargetIdentity() string {
	if c == nil {
		return ""
	}
	return nativeTargetIdentity
}

func (c *NativeCallbackClient) SubmitExecutionResult(ctx context.Context, callbackPath string, result ExecutionResultRequest) (*DWSDelivery, error) {
	silent := strings.TrimSpace(result.ResultMessage) == "" || (result.ShouldReply != nil && !*result.ShouldReply)
	return nil, c.acknowledge(ctx, callbackPath, executionResultCallbackPattern, silent)
}

func (c *NativeCallbackClient) SubmitExecutionUpdate(ctx context.Context, callbackPath string, update ExecutionUpdateRequest) (*DWSDelivery, error) {
	return nil, c.acknowledge(ctx, callbackPath, executionUpdateCallbackPattern, strings.TrimSpace(update.ResultMessage) == "")
}

// acknowledge accepts a callback whose reply was handed to the managed
// outbox. Without a frozen route a reply would be lost silently, so only a
// callback with nothing to say (a duplicate proactive message's silence) is
// accepted; anything else is dead-lettered where operators see it.
func (c *NativeCallbackClient) acknowledge(ctx context.Context, callbackPath string, pattern *regexp.Regexp, silent bool) error {
	if !IsNativeDispatchCallback(callbackPath) || !pattern.MatchString(callbackPath) {
		return &dwsDeliveryPermanentError{"invalid_native_callback"}
	}
	if c == nil || c.routes == nil {
		return errors.New("native response routes are unavailable")
	}
	found, err := c.routes(ctx, callbackPath)
	if err != nil {
		return err
	}
	if found || silent {
		return nil
	}
	return &dwsDeliveryPermanentError{"native_route_missing"}
}

// CompletionNotifiers wakes every completion worker; each claims only the
// outbox rows of its own target.
type CompletionNotifiers []*CompletionWorker

func (n CompletionNotifiers) NotifyTaskCompletion() {
	for _, w := range n {
		w.NotifyTaskCompletion()
	}
}

func (n CompletionNotifiers) NotifyTaskExecutionUpdate() {
	for _, w := range n {
		w.NotifyTaskExecutionUpdate()
	}
}
