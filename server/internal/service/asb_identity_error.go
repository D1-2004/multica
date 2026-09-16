package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const asbBUCProbeErrorPrefix = "multica_buc_probe_error="

type asbIdentityFailureReason struct {
	code   string
	detail string
}

// Only known diagnostic signatures are promoted from logs. Never publish raw
// lifecycle logs: they can include BUC tokens and WireGuard business credentials.
func asbIdentityReason(text string) *asbIdentityFailureReason {
	if strings.Contains(text, "BIZ_SERVICE_CONTAINER_TRUST_DEVICE_REGISTER_USAGE_EXCEEDS_LIMIT") {
		return &asbIdentityFailureReason{"ASB-BUC-TRUST-DEVICE-LIMIT", "ASB 企业身份绑定失败：可信设备注册使用量超过上限（BIZ_SERVICE_CONTAINER_TRUST_DEVICE_REGISTER_USAGE_EXCEEDS_LIMIT）。请联系阿里郎管理员核查可信设备额度。"}
	}
	if strings.Contains(strings.ToLower(text), "zt token not found") {
		return &asbIdentityFailureReason{"ASB-BUC-ZT-TOKEN-NOT-FOUND", "ASB 企业身份验证失败：zt token not found。"}
	}
	return nil
}

var asbIdentitySecretField = regexp.MustCompile(`(?i)(?:buc[._-]?(?:accessToken|refreshToken|idToken)|access[._-]?token|refresh[._-]?token|id[._-]?token|agent[._-]?token|wgclientCredentials|bizParams|authorization|cookie|api[._-]?key|token)["'\\]*\s*[=:]`)

func sanitizeASBIdentityDetail(text string) string {
	// Drop the suffix rather than attempting to parse nested credential objects
	// or escaped shell/JSON values echoed by an upstream service.
	if loc := asbIdentitySecretField.FindStringIndex(text); loc != nil {
		text = text[:loc[0]] + "[REDACTED CREDENTIAL]"
	}
	return sanitizeRuntimeStartUserDetail(text)
}

type asbIdentityStartError struct {
	cause  error
	code   string
	detail string
}

func (e *asbIdentityStartError) Error() string                  { return e.detail }
func (e *asbIdentityStartError) Unwrap() error                  { return e.cause }
func (e *asbIdentityStartError) runtimeStartUserDetail() string { return e.detail }

func asbIdentityReasonFromError(err error) *asbIdentityFailureReason {
	var httpErr *ASBHTTPError
	if errors.As(err, &httpErr) && httpErr.identityReason != nil {
		return httpErr.identityReason
	}
	return asbIdentityReason(runtimeStartUserDetailFromError(err))
}

func newASBIdentityStartError(cause error, reason *asbIdentityFailureReason) error {
	code := "ASB-BUC-IDENTITY-FAILED"
	detail := "ASB 企业身份绑定或验证失败。"
	if errors.Is(cause, ErrEnterpriseIdentityNeedsReauth) {
		code = "ASB-BUC-IDENTITY-MISMATCH"
		detail = "ASB 企业身份与绑定员工不匹配，请重新授权 Agent 企业身份后重试。"
	} else {
		if reason == nil {
			reason = asbIdentityReasonFromError(cause)
		}
		if reason != nil {
			code, detail = reason.code, reason.detail
		} else if upstream := runtimeStartUserDetailFromError(cause); upstream != "" {
			detail += " " + sanitizeASBIdentityDetail(upstream)
		}
	}
	var httpErr *ASBHTTPError
	if errors.As(cause, &httpErr) && !strings.Contains(detail, httpErr.Error()) {
		// Keep the transport/request reference even when the root cause replaces
		// a long HTTP message containing wgclient logs.
		detail += " " + httpErr.Error()
	}
	return &asbIdentityStartError{cause: cause, code: code, detail: sanitizeASBIdentityDetail(detail)}
}

func (l *ASBLauncher) asbIdentityStartError(ctx context.Context, sandboxID string, cause error) error {
	reason := asbIdentityReasonFromError(cause)
	if reason == nil && !errors.Is(cause, ErrEnterpriseIdentityNeedsReauth) {
		// The attach deadline may already have elapsed. Bound diagnostics
		// independently and collect before deleting this task's failed sandbox.
		diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		reason = l.Client.asbIdentityDiagnosticReason(diagnosticCtx, sandboxID)
	}
	return newASBIdentityStartError(cause, reason)
}

func (c *ASBClient) asbIdentityDiagnosticReason(ctx context.Context, sandboxID string) *asbIdentityFailureReason {
	if validateASBSandboxID(sandboxID) != nil {
		return nil
	}
	var response struct {
		SandboxID string `json:"sandboxId"`
		Kind      string `json:"kind"`
		Scope     string `json:"scope"`
		Delivery  string `json:"delivery"`
		Content   string `json:"content"`
	}
	if err := c.doLifecycleJSON(ctx, "identity_diagnostics", http.MethodGet,
		"/sandboxes/"+sandboxID+"/diagnostics/logs", url.Values{"scope": {"lifecycle"}}, nil, &response, http.StatusOK); err != nil {
		return nil
	}
	// Lifecycle diagnostics currently use inline delivery. Do not follow
	// arbitrary artifact URLs or forward the tenant API key to another host.
	if response.SandboxID != sandboxID || response.Kind != "logs" || response.Scope != "lifecycle" || response.Delivery != "inline" {
		return nil
	}
	return asbIdentityReason(response.Content)
}

func asbTaskBUCProbeError(result *ASBExecResult) error {
	cause := &asbEnterpriseCLIIdentityProbeError{stage: "buc"}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if !strings.HasPrefix(line, asbBUCProbeErrorPrefix) {
			continue
		}
		var diagnostic struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, asbBUCProbeErrorPrefix)), &diagnostic) != nil {
			continue
		}
		if reason := asbIdentityReason(diagnostic.Code + " " + diagnostic.Message); reason != nil {
			return withRuntimeStartUserDetail(cause, reason.detail)
		}
		return withRuntimeStartUserDetail(cause, "BUC 身份探测失败："+sanitizeASBIdentityDetail(fmt.Sprintf("code=%s; message=%s", diagnostic.Code, diagnostic.Message)))
	}
	return cause
}
