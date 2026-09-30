package clicompat

import (
	"bytes"
	"errors"

	apperrors "github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/errors"
	"github.com/multica-ai/multica/server/pkg/dws/clicompat/internal/upstream/output"
)

// Failure is a failed dws command as its JSON error reports it. It wraps the
// error value dws itself would have produced (an internal/errors.Error, a
// helpers.CLIError or a plain error), and renders it with dws's own code.
// The exported fields are what dws reports for that error: a field dws
// leaves out (for example server_error_code of an untyped failure, or
// server_key of an HTTP failure) is empty here too.
type Failure struct {
	Category        string // "api", "auth", "validation", "discovery", "internal"
	Reason          string // e.g. "business_error", "invalid_request", "mcp_tool_error", "http_<status>"; "" for untyped failures
	ServerErrorCode string // only string codes, extracted like dws ServerDiag
	TraceID         string // trace_id|traceId|request_id|requestId, recursive like dws
	Message         string // dws's error message (errorMsg etc.; the raw tool text for untyped failures)
	ExitCode        int    // dws exit code: 1 api, 2 auth, 3 validation, 5 internal, 6 discovery
	Server          string // "chat"/"im": the server_key ("" when dws reports none)

	err error // the dws error value; nil for a Failure literal built by a caller
}

// newFailure describes a dws error value by the fields dws reports for it.
func newFailure(err error) *Failure {
	f := &Failure{err: err, ExitCode: apperrors.ExitCode(err), Message: err.Error(), Category: string(apperrors.CategoryInternal)}
	var typed *apperrors.Error
	var cliErr *CLIError
	switch {
	case errors.As(err, &typed):
		f.Category = string(typed.Category)
		f.Reason = typed.Reason
		f.ServerErrorCode = typed.ServerDiag.ServerErrorCode
		f.TraceID = typed.ServerDiag.TraceID
		f.Message = typed.Message
		f.Server = typed.ServerKey
	case errors.As(err, &cliErr):
		// dws's legacy JSON calls every untyped error "internal"; the category
		// kept here is the unified envelope's type for it (root.go:487).
		f.Category = errorTypeForExitCode(cliErr.ExitCode())
		f.Message = cliErr.Message
	}
	return f
}

// dwsError is the error dws would print. A Failure literal without a Reason
// becomes dws's untyped [MCP_TOOL_ERROR] (or [AUTH_TOKEN_EXPIRED] for the
// auth category); with a Reason, a typed error of its Category.
func (f *Failure) dwsError() error {
	if f.err != nil {
		return f.err
	}
	if f.Reason == "" {
		code := CodeMCPToolError
		if f.Category == "auth" {
			code = CodeAuthTokenExpired
		}
		return &CLIError{Code: code, Message: f.Message}
	}
	opts := []apperrors.Option{
		apperrors.WithReason(f.Reason),
		apperrors.WithServerKey(f.Server),
		apperrors.WithServerDiag(apperrors.ServerDiagnostics{TraceID: f.TraceID, ServerErrorCode: f.ServerErrorCode}),
	}
	switch apperrors.Category(f.Category) {
	case apperrors.CategoryAuth:
		return apperrors.NewAuth(f.Message, opts...)
	case apperrors.CategoryValidation:
		return apperrors.NewValidation(f.Message, opts...)
	case apperrors.CategoryDiscovery:
		return apperrors.NewDiscovery(f.Message, opts...)
	case apperrors.CategoryInternal:
		return apperrors.NewInternal(f.Message, opts...)
	default:
		return apperrors.NewAPI(f.Message, opts...)
	}
}

// Error is the message dws prints for the failure (err.Error() of the dws
// error: "[CODE] message (operation: op)\n  hint: …" for an untyped one).
func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	return f.dwsError().Error()
}

// LegacyJSON is the legacy error envelope dws prints on stderr:
// printExecutionError (internal/app/root.go:695) → apperrors.PrintJSON
// (internal/errors/errors.go:399), dws's own code. Ends with a newline.
func (f *Failure) LegacyJSON() []byte {
	if f == nil {
		return nil
	}
	var buf bytes.Buffer
	_ = apperrors.PrintJSON(&buf, f.dwsError())
	return buf.Bytes()
}

// UnifiedJSON is the unified failure envelope `chat message send` prints on
// stdout: the failure branch of ExecuteWithTelemetry
// (internal/app/root.go:293-296) — errorInfoFromExecutionError and
// output.FailureWithExitCode — emitted by emitJSON. Ends with a newline.
func (f *Failure) UnifiedJSON() []byte {
	if f == nil {
		return nil
	}
	err := f.dwsError()
	result := output.FailureWithExitCode(errorInfoFromExecutionError(err), apperrors.ExitCode(err))
	return emitJSON(result)
}

// emitJSON is the --format json branch of output.EmitResult
// (internal/output/emitter.go:93-151): validate the result, redact the
// envelope (emitResult and renderEnvelope both do), render it with
// WriteJSON (renderEnvelopeInto → WriteFiltered → Write → WriteJSON with no
// --fields/--jq). A result dws cannot emit becomes the internal error the
// root prints instead (internal/app/root.go:298).
func emitJSON(result output.CommandResult) []byte {
	env, err := output.EnvelopeFromResult(result)
	if err != nil {
		return (&Failure{err: apperrors.NewInternal("emit failure result: "+err.Error(), apperrors.WithCause(err))}).LegacyJSON()
	}
	env = output.RedactEnvelope(output.RedactEnvelope(env))
	var buf bytes.Buffer
	if err := output.WriteJSON(&buf, env); err != nil {
		return (&Failure{err: err}).LegacyJSON()
	}
	return buf.Bytes()
}
