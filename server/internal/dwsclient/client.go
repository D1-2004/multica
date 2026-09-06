// Package dwsclient is the server-side DWS identity and history client.
// Coordinator last-N reads and Scene Memory range reads share this package
// so credentials stay per-call and never land in scene_memory.
package dwsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	MaxResponseBytes = 1024 * 1024
	DefaultCLIPath   = "dws"
)

// IsTimeout reports a cancelled or deadline-exceeded call, including wrapped CLI errors.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "context canceled")
}

func commandFailed(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("%s: %w", op, ctx.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return errors.New(op)
}

type Credential struct {
	UID      string
	ClientID string
	AuthCode string
}

type ListRequest struct {
	ConversationID string
	Before         time.Time
	Direction      string
	Limit          int
}

type CLI struct {
	Path         string
	ClientSecret string
}

func (c CLI) path() string {
	if strings.TrimSpace(c.Path) == "" {
		return DefaultCLIPath
	}
	return c.Path
}

func (c CLI) Exchange(ctx context.Context, configDir string, credential Credential) error {
	if strings.TrimSpace(c.ClientSecret) == "" {
		return errors.New("DWS client secret is not configured")
	}
	cmd := exec.CommandContext(ctx, c.path(),
		"auth", "exchange",
		"--code", credential.AuthCode,
		"--uid", credential.UID,
		"--format", "json",
	)
	cmd.Env = CommandEnv(configDir, map[string]string{
		"DWS_CLIENT_ID":     credential.ClientID,
		"DWS_CLIENT_SECRET": c.ClientSecret,
	})
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return commandFailed(ctx, "DWS AuthCode exchange failed", err)
	}
	return nil
}

func (c CLI) List(ctx context.Context, configDir string, req ListRequest) ([]byte, error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return nil, errors.New("DWS conversation id is required")
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if strings.TrimSpace(req.Direction) == "" {
		req.Direction = "older"
	}
	before := req.Before
	if before.IsZero() {
		before = time.Now()
	}
	queryTime := before.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04:05")
	cmd := exec.CommandContext(ctx, c.path(),
		"chat", "message", "list",
		"--group", req.ConversationID,
		"--time", queryTime,
		"--direction", req.Direction,
		"--limit", strconv.Itoa(req.Limit),
		"--format", "json",
	)
	cmd.Env = CommandEnv(configDir, nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if stdout.Len() > MaxResponseBytes {
		return nil, errors.New("DWS conversation history response is too large")
	}
	raw := stdout.Bytes()
	if runErr != nil {
		// dws often exits 1 with a success:false JSON envelope. Keep the
		// body so callers can log errorMsg instead of a blank CLI failure.
		if looksLikeJSONObject(raw) {
			return raw, nil
		}
		failed := commandFailed(ctx, "DWS conversation history query failed", runErr)
		if IsTimeout(failed) {
			return nil, failed
		}
		if msg := SafeMessage(stderr.String(), 80); msg != "" {
			return nil, fmt.Errorf("%s: %s", failed.Error(), msg)
		}
		return nil, failed
	}
	return raw, nil
}

func looksLikeJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

type Redeemer struct {
	BaseURL         string
	BaseURLProvider func() string
	Client          *http.Client
	UserAgent       string
}

type redeemResponse struct {
	OK        bool   `json:"ok"`
	ErrorCode string `json:"errorCode"`
	Identity  struct {
		Key      string `json:"key"`
		Type     string `json:"type"`
		UID      string `json:"uid"`
		ClientID string `json:"clientId"`
	} `json:"identity"`
	Credential struct {
		Type     string `json:"type"`
		AuthCode string `json:"authCode"`
	} `json:"credential"`
}

func (r Redeemer) Redeem(ctx context.Context, contextToken string) (Credential, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	if r.BaseURLProvider != nil {
		baseURL = strings.TrimRight(strings.TrimSpace(r.BaseURLProvider()), "/")
	}
	if baseURL == "" || strings.TrimSpace(contextToken) == "" {
		return Credential{}, errors.New("Agent Identity DWS redeem is not configured")
	}
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	body := strings.NewReader(`{"identityKey":"dws","credentialType":"DWS_AUTH_CODE"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/agent-identity/v1/credentials/redeem", body)
	if err != nil {
		return Credential{}, errors.New("build Agent Identity DWS redeem request")
	}
	ua := strings.TrimSpace(r.UserAgent)
	if ua == "" {
		ua = "dt-fde-multica/dwsclient"
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(contextToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return Credential{}, errors.New("Agent Identity DWS redeem request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil || len(raw) > MaxResponseBytes {
		return Credential{}, errors.New("read Agent Identity DWS redeem response")
	}
	var payload redeemResponse
	if json.Unmarshal(raw, &payload) != nil {
		return Credential{}, errors.New("decode Agent Identity DWS redeem response")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !payload.OK {
		return Credential{}, fmt.Errorf("Agent Identity DWS redeem rejected: %s", SafeCode(payload.ErrorCode))
	}
	credential := Credential{
		UID:      strings.TrimSpace(payload.Identity.UID),
		ClientID: strings.TrimSpace(payload.Identity.ClientID),
		AuthCode: strings.TrimSpace(payload.Credential.AuthCode),
	}
	if payload.Identity.Key != "dws" || payload.Identity.Type != "DWS_UID" ||
		payload.Credential.Type != "DWS_AUTH_CODE" || credential.ClientID == "" || credential.AuthCode == "" {
		return Credential{}, errors.New("Agent Identity returned an unexpected DWS credential")
	}
	if _, err := strconv.ParseUint(credential.UID, 10, 64); err != nil {
		return Credential{}, errors.New("Agent Identity returned an invalid DWS uid")
	}
	return credential, nil
}

func CommandEnv(configDir string, values map[string]string) []string {
	blocked := map[string]struct{}{
		"AGENT_IDENTITY_CONTEXT_TOKEN": {},
		"DWS_AUTH_CODE":                {},
		"DWS_CONFIG_DIR":               {},
		"DWS_CLIENT_ID":                {},
		"DWS_CLIENT_SECRET":            {},
		"DWS_UID":                      {},
		"MULTICA_AGENT_IDENTITY_DWS_CLIENT_SECRET": {},
	}
	env := make([]string, 0, len(os.Environ())+len(values)+1)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, skip := blocked[key]; !skip {
			env = append(env, item)
		}
	}
	env = append(env, "DWS_CONFIG_DIR="+filepath.Clean(configDir))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func SafeCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return "operation_failed"
	}
	for _, r := range raw {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return "operation_failed"
		}
	}
	return raw
}

// SafeMessage clips a DWS errorMsg for logs. Control characters are dropped
// so the value can sit in slog without becoming a second error code.
func SafeMessage(raw string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 80
	}
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(raw) {
		if r < 32 || r == 127 {
			continue
		}
		if n >= maxRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// HistoryRejected formats a DWS list envelope that is not success. Empty
// errorCode becomes operation_failed; errorMsg is attached when present so
// logs are not just the placeholder.
func HistoryRejected(errorCode, errorMsg string) error {
	code := SafeCode(errorCode)
	msg := SafeMessage(errorMsg, 80)
	if msg == "" {
		return fmt.Errorf("DWS conversation history query rejected: %s", code)
	}
	return fmt.Errorf("DWS conversation history query rejected: %s: %s", code, msg)
}
