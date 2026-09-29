package a2aintegration

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// AuthorizationInterceptor applies client scopes only after the official SDK
// has parsed and classified the JSON-RPC call. This preserves protocol-native
// parse/method errors while keeping new SDK methods fail-closed until they are
// deliberately mapped here.
type AuthorizationInterceptor struct {
	a2asrv.PassthroughCallInterceptor
}

func (AuthorizationInterceptor) Before(
	ctx context.Context,
	callContext *a2asrv.CallContext,
	_ *a2asrv.Request,
) (context.Context, any, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return ctx, nil, a2a.ErrUnauthenticated
	}
	requiredScope := methodScope(callContext.Method())
	if requiredScope == "" || !hasScope(principal.Scopes, requiredScope) {
		return ctx, nil, a2a.ErrUnauthorized
	}
	if !principal.EndpointEnabled && (callContext.Method() == "SendMessage" || callContext.Method() == "SendStreamingMessage") {
		return ctx, nil, a2a.ErrUnauthorized
	}
	return ctx, nil, nil
}

// MethodPermitted applies the same scope and publication rules as
// AuthorizationInterceptor to a JSON-RPC method name, for request paths that
// must decide before the SDK runs (the cross-environment forwarder).
func MethodPermitted(method string, scopes []string, endpointEnabled bool) bool {
	requiredScope := methodScope(method)
	if requiredScope == "" || !hasScope(scopes, requiredScope) {
		return false
	}
	return endpointEnabled || !IsTaskCreatingMethod(method)
}

// IsTaskCreatingMethod reports whether a method admits a new task turn.
func IsTaskCreatingMethod(method string) bool {
	return method == "SendMessage" || method == "SendStreamingMessage"
}

func methodScope(method string) string {
	switch method {
	case "SendMessage", "SendStreamingMessage",
		"CreateTaskPushConfig", "DeleteTaskPushConfig":
		return "send"
	case "GetTask", "SubscribeToTask", "GetTaskPushConfig",
		"GetExtendedAgentCard":
		return "read"
	case "ListTasks", "ListTaskPushConfigs":
		return "list"
	case "CancelTask":
		return "cancel"
	default:
		return ""
	}
}

func hasScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}
