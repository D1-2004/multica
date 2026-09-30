package clicompat

import (
	"bytes"
	"encoding/json"

	apperrors "github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/errors"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/jsonutil"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/transport"
)

// The open-source edition's suggestions: authExpiredSuggestion and
// notLoggedInSuggestion (internal/helpers/helpers.go:849,857) with
// edition.Get().IsEmbedded false (pkg/edition is not vendored: it imports
// cobra).
const (
	openAuthExpiredSuggestion = "Re-authenticate: dws auth login"
	openNotLoggedInSuggestion = "请先登录：dws auth login"
)

// CheckPayload applies dws's post-processing to a tool's JSON text payload,
// running dws's own code in dws's order (contracts §0.3 steps 2–7, minus PAT
// flows):
//
//  1. The payload must be one JSON object. dws's transport keeps a text block
//     only when it decodes as an object (internal/transport/client.go:391)
//     and the adapter re-encodes a missing object as "null"
//     (internal/app/tool_caller_adapter.go:203), so plain text (which
//     dws.CallRaw hands back as a JSON string), an array or an empty text
//     all become "null" with no failure, exactly what dws's commands then
//     render. Only a zero-length payload is reported, as dws's
//     empty_tool_response (internal/helpers/chat.go:273).
//  2. The runner (internal/app/runner.go:855-880): detectBusinessError with
//     ServerDiag and the server-failure classifier, then success:true
//     stamped when absent.
//  3. The helper classification of the re-encoded text: parseMCPToolTextResult
//     (internal/helpers/helpers.go:305) for most commands, the helper print
//     path (internal/helpers/helpers.go:552-583) for the tools of
//     `chat data-auth cross-org` and `chat message update-a2ui-card`, and
//     withProjectedChatOperation (internal/helpers/chat.go:261) for the
//     projected reads.
//
// One deliberate difference: dws decodes the tool text with plain
// encoding/json (internal/transport/client.go:392), which rounds integers
// above 2^53 and respells numbers such as 1.50; here the object is decoded
// with UseNumber so the returned text keeps every number as sent.
//
// On success it returns the object as the dws adapter re-encodes it
// (jsonutil.Marshal: compact, keys sorted, not HTML-escaped). server and tool
// are the server_key and the tool name.
func CheckPayload(server, tool string, payload []byte) ([]byte, *Failure) {
	operation := server + "/" + tool
	if len(bytes.TrimSpace(payload)) == 0 {
		// writeProjectedChatPayload (internal/helpers/chat.go:277-285).
		return nil, newFailure(apperrors.NewAPI("MCP read tool returned no non-empty text content",
			apperrors.WithOperation(operation),
			apperrors.WithOrigin("mcp"),
			apperrors.WithFailureStage("response_validation"),
			apperrors.WithRetryable(true),
			apperrors.WithReason("empty_tool_response"),
		))
	}
	var content map[string]any
	if transport.UnmarshalJSONUseNumber(payload, &content) != nil {
		content = nil
	}

	// internal/app/runner.go:855-880 with callResult.Content = content and
	// invocation.CanonicalProduct/Tool = server/tool.
	if bizErr := detectBusinessError(content); bizErr != "" {
		diag := transport.ExtractServerDiagnosticsFromMap(content)
		classifiedErr := newServerFailureAPIError(
			bizErr,
			"business_error",
			"The API returned a business-level error. Check required parameters and values.",
			server,
			tool,
			diag,
		)
		return nil, newFailure(classifiedErr)
	}
	if content != nil {
		if _, has := content["success"]; !has {
			content["success"] = true
		}
	}

	// convertResult (internal/app/tool_caller_adapter.go:203).
	data, _ := jsonutil.Marshal(content)
	text := string(data)
	var err error
	if tool == "chat_permission_grant" || tool == "update_a2ui_card" {
		err = classifyPrintPathText(text)
	} else {
		err = classifyToolText(text)
		switch tool {
		case "list_conversation_message_v2", "list_messages_by_ids", "query_message_send_status":
			if err != nil {
				err = withProjectedChatOperation(operation, err)
			}
		}
	}
	if err != nil {
		return nil, newFailure(err)
	}
	return data, nil
}

// classifyToolText is the text branch of parseMCPToolTextResult
// (internal/helpers/helpers.go:317-349), statements verbatim; PAT
// classification is not reproduced.
func classifyToolText(text string) error {
	var errBody map[string]any
	if json.Unmarshal([]byte(text), &errBody) == nil {
		if _, ok := getDWSGatewayErrorCode(errBody); ok {
			return &CLIError{
				Code:       CodeAuthTokenExpired,
				Message:    text,
				Suggestion: openAuthExpiredSuggestion,
			}
		}
		if isNotLoggedInError(errBody) {
			return &CLIError{
				Code:       CodeAuthNotConfigured,
				Message:    "当前未登录",
				Suggestion: openNotLoggedInSuggestion,
			}
		}
		if isBusinessError(errBody) {
			return &CLIError{
				Code:       CodeMCPToolError,
				Message:    text,
				Suggestion: suggestForBusinessError(errBody),
			}
		}
	}
	return nil
}

// classifyPrintPathText is the classification in callMCPToolInternalOptsContext
// (internal/helpers/helpers.go:552-583), statements verbatim; the OA approval
// special case and PAT classification do not apply.
func classifyPrintPathText(text string) error {
	var errBody map[string]any
	if json.Unmarshal([]byte(text), &errBody) == nil {
		if errBody == nil {
			return nil
		}
		if _, ok := getDWSGatewayErrorCode(errBody); ok {
			return &CLIError{Code: CodeAuthTokenExpired, Message: text, Suggestion: openAuthExpiredSuggestion}
		}
		if isNotLoggedInError(errBody) {
			return &CLIError{Code: CodeAuthNotConfigured, Message: "当前未登录", Suggestion: openNotLoggedInSuggestion}
		}
		if isBusinessError(errBody) {
			message := businessErrorDisplayMessage(errBody, text)
			return &CLIError{Code: CodeMCPToolError, Message: message, Suggestion: suggestForBusinessError(errBody)}
		}
	}
	return nil
}
