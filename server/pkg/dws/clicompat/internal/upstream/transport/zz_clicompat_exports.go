// Not from dws: clicompat glue. Exposes unexported functions of this vendored
// package to clicompat, which reproduces their callers (CallTool, callJSONRPC,
// doWithRetry) around the dws client. The other files of this package are dws's,
// unmodified except for import paths.

package transport

// HTTPStatusError is httpStatusError (client.go).
func HTTPStatusError(method, endpoint string, statusCode int, snapshotPath, headerTraceID string) error {
	return httpStatusError(method, endpoint, statusCode, snapshotPath, headerTraceID)
}

// JSONRPCEnvelopeError is jsonrpcEnvelopeError (client.go).
func JSONRPCEnvelopeError(method string, rpcErr *RPCError, snapshotPath, headerTraceID string) error {
	return jsonrpcEnvelopeError(method, rpcErr, snapshotPath, headerTraceID)
}

// ValidateCallArguments is validateCallArguments (client.go).
func ValidateCallArguments(args map[string]any) error {
	return validateCallArguments(args)
}

// ClassifyRequestFailure is classifyRequestFailure (client.go).
func ClassifyRequestFailure(err error) (reason, hint string) {
	return classifyRequestFailure(err)
}

// IsTimeoutError is isTimeoutError (client.go).
func IsTimeoutError(err error) bool {
	return isTimeoutError(err)
}

// ReasonForMethod is reasonForMethod (client.go).
func ReasonForMethod(method, suffix string) string {
	return reasonForMethod(method, suffix)
}

// DiscoveryActions is discoveryActions (client.go).
func DiscoveryActions(snapshotPath string) []string {
	return discoveryActions(snapshotPath)
}

// NetworkActions is networkActions (client.go).
func NetworkActions(snapshotPath string) []string {
	return networkActions(snapshotPath)
}

// UnmarshalJSONUseNumber is unmarshalJSONUseNumber (client.go).
func UnmarshalJSONUseNumber(data []byte, out any) error {
	return unmarshalJSONUseNumber(data, out)
}
