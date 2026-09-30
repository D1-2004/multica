package dws

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Initialization failures, matched with errors.Is on the error from New.
var (
	// ErrInvalidConfig: the Config or AuthCode is unusable. Nothing was sent.
	ErrInvalidConfig = errors.New("invalid config")
	// ErrAuthCodeRejected: DingTalk refused the code (unknown, expired or
	// already used). Retrying with the same code cannot succeed.
	ErrAuthCodeRejected = errors.New("auth code rejected")
	// ErrExchangeUnavailable: the token endpoint could not be reached or
	// failed (network, 5xx, rate limit).
	ErrExchangeUnavailable = errors.New("token exchange unavailable")
	// ErrIdentityUnverified: a token was issued but a test call with it failed.
	ErrIdentityUnverified = errors.New("identity not verified")
	// ErrIdentityMismatch: the token belongs to someone other than expected.
	ErrIdentityMismatch = errors.New("identity mismatch")
)

// ErrSessionExpired: the Client's token expired and could not be refreshed.
// The Client is unusable; create a new one from a new auth code.
var ErrSessionExpired = errors.New("dws session expired")

// ErrInvalidRequest marks a caller mistake such as a missing id or empty
// text. Nothing was sent to DingTalk.
var ErrInvalidRequest = errors.New("invalid request")

func invalid(msg string) error { return fmt.Errorf("dws: %s: %w", msg, ErrInvalidRequest) }

// InitStage is the step of New that failed.
type InitStage string

const (
	// StageConfig: Config or AuthCode validation. Nothing was sent.
	StageConfig InitStage = "config"
	// StageExchange: trading the auth code for a token.
	StageExchange InitStage = "exchange"
	// StageVerify: confirming the new token with a test call.
	StageVerify InitStage = "verify"
)

// InitError is a failed New. It never carries the code, token or secret.
type InitError struct {
	Stage InitStage
	// Code is the provider's error code, e.g. invalidParameter.authCode.notFound.
	Code string
	// CodeSpent reports that the auth code may have been consumed: retry
	// with a new code, never the same one. It is false only when the code
	// provably never reached DingTalk.
	CodeSpent bool
	// Temporary reports a transient failure: retrying (with a new code when
	// CodeSpent) can succeed.
	Temporary bool

	kind  error
	cause error
}

func (e *InitError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "dws: init failed at %s: %v", e.Stage, e.kind)
	if e.Code != "" {
		b.WriteString(" (" + e.Code + ")")
	}
	if e.cause != nil {
		b.WriteString(": " + e.cause.Error())
	}
	return b.String()
}

// Unwrap exposes both the sentinel (ErrAuthCodeRejected, …) and the cause.
func (e *InitError) Unwrap() []error {
	out := []error{e.kind}
	if e.cause != nil {
		out = append(out, e.cause)
	}
	return out
}

// ErrorKind says which layer rejected a tool call.
type ErrorKind string

const (
	// KindHTTP is a non-2xx gateway response, e.g. 400 for a missing token.
	KindHTTP ErrorKind = "http"
	// KindRPC is a JSON-RPC error object.
	KindRPC ErrorKind = "rpc"
	// KindTool is a tools/call result with isError=true.
	KindTool ErrorKind = "tool"
	// KindBusiness is a tool payload with success=false.
	KindBusiness ErrorKind = "business"
	// KindDecode is a response this package could not parse.
	KindDecode ErrorKind = "decode"
)

// Error is a rejected or unreadable tool call. It never carries the access
// token or request arguments.
type Error struct {
	Server  Server
	Tool    string
	Kind    ErrorKind
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "dws %s/%s: %s", e.Server, e.Tool, e.Kind)
	if e.Status != 0 {
		fmt.Fprintf(&b, " %d", e.Status)
	}
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// authCodes are the DWS business codes for an expired or rejected token.
var authCodes = map[string]bool{
	"DWS_SERVICE_UNAUTHORIZED": true,
	"DWS_AUTH_SERVICE_FAILED":  true,
	"TOKEN_VERIFIED_FAILED":    true,
	"USER_TOKEN_ILLEGAL":       true,
}

// IsAuth reports a tool call the gateway refused because of the token. Such
// a call was not executed, so it is safe to retry once with a fresh token.
func IsAuth(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return true
	}
	// The gateway answers 400 with this text when no usable token was sent.
	if strings.Contains(e.Message, "Missing service_id or access_key") {
		return true
	}
	return authCodes[e.Code]
}

// clip bounds provider text copied into an error.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
