package clicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/pkg/dws"
	apperrors "github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/errors"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/i18n"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/pkg/config"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/transport"
)

// gatewayEndpoint stands in for the MCP endpoint dws names in transport
// errors; transport.RedactURL keeps only its scheme and host. dws errors
// do not carry the URL, so the production gateway is assumed.
const gatewayEndpoint = "https://mcp-gw.dingtalk.com"

// CallFailure maps a gateway-call error that happened before any payload to
// the error dws reports, built with dws's own constructors:
//
//   - *dws.Error Kind http → transport.httpStatusError
//     (internal/transport/client.go:1227).
//   - Kind rpc → transport.jsonrpcEnvelopeError (internal/transport/client.go:1278).
//   - Kind tool (isError:true) → the runner's isError branch
//     (internal/app/runner.go:803-839): ServerDiag from the tool text and
//     newServerFailureAPIError with reason mcp_tool_error.
//   - Kind business with a gateway auth code (dws.CallRaw's token
//     rejection) → the untyped AUTH_TOKEN_EXPIRED of parseMCPToolTextResult
//     (internal/helpers/helpers.go:325); dws's message is the raw tool text,
//     dws keeps only the code. Other business errors (dws.Call's
//     success:false) are rebuilt as a payload and run through CheckPayload.
//   - Kind decode → callJSONRPC's protocol-response errors
//     (internal/transport/client.go:747-821).
//   - dws.ErrSessionExpired (refresh failed after a rejection) →
//     retryAuthRefreshRequired's auth_refresh_failed
//     (internal/app/auth_refresh_retry.go:128-151).
//   - dws.ErrInvalidRequest (nothing sent) → a validation error.
//   - Network errors and timeouts → doWithRetry's retry-exhausted branch
//     (internal/transport/client.go:933-957); a failed response read →
//     callJSONRPC's response_read_failed (internal/transport/client.go:737).
//
// Anything else is reported like a plain Go error (internal, exit 5).
func CallFailure(err error) *Failure {
	if err == nil {
		return nil
	}
	if errors.Is(err, dws.ErrSessionExpired) {
		var rejection error = err
		var gatewayErr *dws.Error
		if errors.As(err, &gatewayErr) {
			rejection = gatewayErr
		}
		// internal/app/auth_refresh_retry.go:145-151.
		combined := &authRefreshFailureError{rejection: rejection, refresh: err}
		return newFailure(apperrors.NewAuth(
			"automatic access token refresh failed",
			apperrors.WithOperation("auth/token/refresh"),
			apperrors.WithReason("auth_refresh_failed"),
			apperrors.WithHint("本地凭证已保留；可稍后重试，若持续失败请查看认证诊断日志。"),
			apperrors.WithCause(combined),
		))
	}
	if errors.Is(err, dws.ErrInvalidRequest) {
		return newFailure(apperrors.NewValidation(err.Error()))
	}
	var gatewayErr *dws.Error
	if errors.As(err, &gatewayErr) {
		return gatewayFailure(gatewayErr)
	}
	if strings.HasPrefix(err.Error(), "dws: ") && strings.Contains(err.Error(), ": read response: ") {
		return newFailure(responseReadFailure())
	}
	if isTransportFailure(err) {
		return newFailure(requestFailure(err))
	}
	return newFailure(err)
}

func gatewayFailure(e *dws.Error) *Failure {
	server := string(e.Server)
	switch e.Kind {
	case dws.KindHTTP:
		return newFailure(transport.HTTPStatusError("tools/call", gatewayEndpoint, e.Status, "", ""))
	case dws.KindRPC:
		code, _ := strconv.Atoi(strings.TrimSpace(e.Code))
		return newFailure(transport.JSONRPCEnvelopeError("tools/call", &transport.RPCError{Code: code, Message: e.Message}, "", ""))
	case dws.KindTool:
		// Decode the answer as dws's transport does (ToolCallResult.UnmarshalJSON).
		raw, _ := json.Marshal(map[string]any{
			"content": []map[string]any{{"type": "text", "text": e.Message}},
			"isError": true,
		})
		var callResult transport.ToolCallResult
		if err := callResult.UnmarshalJSON(raw); err != nil {
			return newFailure(err)
		}
		// internal/app/runner.go:803-839 without the PAT and edition hooks.
		diag := transport.ExtractServerDiagnosticsFromMap(callResult.Content)
		mcpErr := newServerFailureAPIError(
			extractMCPErrorMessage(callResult),
			"mcp_tool_error",
			"MCP tool returned a business error; check tool parameters and refer to skill documentation.",
			server,
			e.Tool,
			diag,
		)
		return newFailure(mcpErr)
	case dws.KindBusiness:
		if _, ok := getDWSGatewayErrorCode(map[string]any{"errorCode": e.Code}); ok {
			message := strings.TrimSpace(e.Message)
			if message == "" {
				message = e.Code
			}
			var err error = &CLIError{Code: CodeAuthTokenExpired, Message: message, Suggestion: openAuthExpiredSuggestion}
			switch e.Tool {
			case "list_conversation_message_v2", "list_messages_by_ids", "query_message_send_status":
				err = withProjectedChatOperation(server+"/"+e.Tool, err)
			}
			return newFailure(err)
		}
		payload := map[string]any{"success": false}
		if e.Code != "" {
			payload["errorCode"] = e.Code
		}
		if e.Message != "" {
			payload["errorMsg"] = e.Message
		}
		raw, _ := json.Marshal(payload)
		if _, fail := CheckPayload(server, e.Tool, raw); fail != nil {
			return fail
		}
		return newFailure(e)
	default: // dws.KindDecode
		return newFailure(decodeFailure(e.Message))
	}
}

// decodeFailure is callJSONRPC's error for a response dws could not read
// (internal/transport/client.go:747-821), request.Method "tools/call", no
// snapshot and no response trace header.
func decodeFailure(message string) error {
	method, endpoint, snapshotPath, headerTraceID := "tools/call", gatewayEndpoint, "", ""
	switch {
	case strings.Contains(message, "too large"):
		return apperrors.NewDiscovery(
			fmt.Sprintf("JSON-RPC %s response exceeds safety limit of %d bytes", method, config.MaxResponseBodySize),
			apperrors.WithOperation(method),
			apperrors.WithReason(transport.ReasonForMethod(method, "response_too_large")),
		)
	case strings.Contains(message, "no result"):
		return apperrors.NewDiscovery(
			fmt.Sprintf("JSON-RPC %s returned an empty result payload", method),
			apperrors.WithOperation(method),
			apperrors.WithReason(transport.ReasonForMethod(method, "empty_result")),
			apperrors.WithHint(i18n.T("服务返回了空结果；请稍后重试。")),
			apperrors.WithActions(transport.DiscoveryActions(snapshotPath)...),
			apperrors.WithSnapshot(snapshotPath),
		)
	default:
		return apperrors.NewDiscovery(
			fmt.Sprintf("unexpected protocol response from %s", transport.RedactURL(endpoint)),
			apperrors.WithOperation(method),
			apperrors.WithReason(transport.ReasonForMethod(method, "invalid_response")),
			apperrors.WithHint(i18n.T("MCP 服务返回了无法解析的协议响应；检查服务版本或上游代理。")),
			apperrors.WithActions(transport.DiscoveryActions(snapshotPath)...),
			apperrors.WithSnapshot(snapshotPath),
			apperrors.WithTraceID(headerTraceID),
		)
	}
}

// responseReadFailure is callJSONRPC's failed body read
// (internal/transport/client.go:737-746).
func responseReadFailure() error {
	request := struct{ Method string }{Method: "tools/call"}
	headerTraceID := ""
	return apperrors.NewDiscovery(
		"failed to read JSON-RPC response",
		apperrors.WithOperation(request.Method),
		apperrors.WithReason(transport.ReasonForMethod(request.Method, "response_read_failed")),
		apperrors.WithHint(i18n.T("检查服务连通性后重试；如持续失败，请确认 MCP 服务响应正常。")),
		apperrors.WithActions(transport.DiscoveryActions("")...),
		apperrors.WithTraceID(headerTraceID),
	)
}

// isTransportFailure reports an error net/http returned for the request,
// which dws passes through as "dws: <server>/<tool>: <err>".
func isTransportFailure(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || os.IsTimeout(err) {
		return true
	}
	return strings.HasPrefix(err.Error(), "dws: ") && errors.Unwrap(err) != nil
}

// requestFailure is doWithRetry's retry-exhausted branch
// (internal/transport/client.go:933-957), operation "tools/call", lastErr the
// net/http error dws wrapped.
func requestFailure(err error) error {
	operation, endpoint := "tools/call", gatewayEndpoint
	lastErr := err
	if strings.HasPrefix(err.Error(), "dws: ") {
		if inner := errors.Unwrap(err); inner != nil {
			lastErr = inner
		}
	}
	reason, hint := transport.ClassifyRequestFailure(lastErr)
	category := apperrors.CategoryDiscovery
	actions := transport.DiscoveryActions("")
	if operation == "tools/call" {
		category = apperrors.CategoryAPI
		actions = transport.NetworkActions("")
	}
	opts := []apperrors.Option{
		apperrors.WithOperation(operation),
		apperrors.WithReason(reason),
		apperrors.WithRetryable(!transport.IsTimeoutError(lastErr)),
		apperrors.WithHint(hint),
		apperrors.WithActions(actions...),
		apperrors.WithCause(&transport.CallError{
			Stage: transport.CallStageRequest,
			Cause: lastErr,
		}),
	}
	message := fmt.Sprintf("request to %s failed: %v", transport.RedactURL(endpoint), lastErr)
	if category == apperrors.CategoryAPI {
		return apperrors.NewAPI(message, opts...)
	}
	return apperrors.NewDiscovery(message, opts...)
}
