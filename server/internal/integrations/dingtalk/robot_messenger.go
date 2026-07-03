package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RobotMessenger delivers outbound bot messages through the DingTalk robot
// message APIs, authenticated per installation with the app's own
// credentials (the scan-to-create device flow minted them):
//
//   - group chats: POST /v1.0/robot/groupMessages/send with the
//     openConversationId (the callback's conversationId)
//   - direct chats: POST /v1.0/robot/oToMessages/batchSend with the
//     recipient's staff userId
//
// robotCode equals the app's clientId for these org-internal apps. Access
// tokens are cached per clientId (~2h TTL) so a chatty session does not
// re-mint on every reply.
type RobotMessenger struct {
	openAPIBase string
	httpClient  *http.Client

	mu     sync.Mutex
	tokens map[string]tokenCache
}

// NewRobotMessenger constructs the messenger. base empty defaults to
// https://api.dingtalk.com; client nil defaults to a 30s-timeout client.
func NewRobotMessenger(base string, client *http.Client) *RobotMessenger {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultOpenAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &RobotMessenger{openAPIBase: base, httpClient: client, tokens: make(map[string]tokenCache)}
}

// RobotTarget addresses one outbound message. Exactly one field is set:
// OpenConversationID for a group chat, UserStaffID for a direct chat.
type RobotTarget struct {
	OpenConversationID string
	UserStaffID        string
}

// SendMarkdown delivers text (agent replies are Markdown-ish; DingTalk's
// sampleMarkdown renders the common subset). The title — required by the
// msgKey — is the first non-empty line, truncated; it shows in the
// conversation list preview.
func (m *RobotMessenger) SendMarkdown(ctx context.Context, creds channelCredentials, target RobotTarget, text string) error {
	msgParam, err := json.Marshal(map[string]string{
		"title": markdownTitle(text),
		"text":  text,
	})
	if err != nil {
		return fmt.Errorf("dingtalk robot: marshal msgParam: %w", err)
	}
	body := map[string]any{
		"robotCode": creds.ClientID,
		"msgKey":    "sampleMarkdown",
		"msgParam":  string(msgParam),
	}
	var path string
	switch {
	case target.OpenConversationID != "":
		path = "/v1.0/robot/groupMessages/send"
		body["openConversationId"] = target.OpenConversationID
	case target.UserStaffID != "":
		path = "/v1.0/robot/oToMessages/batchSend"
		body["userIds"] = []string{target.UserStaffID}
	default:
		return fmt.Errorf("dingtalk robot: empty target")
	}
	token, err := m.accessToken(ctx, creds)
	if err != nil {
		return err
	}
	return m.post(ctx, path, token, body)
}

func (m *RobotMessenger) post(ctx context.Context, path, token string, body map[string]any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("dingtalk robot: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.openAPIBase+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("dingtalk robot: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dingtalk robot: http do: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{
			Status:  resp.StatusCode,
			Code:    "robot_send_failed",
			Message: strings.TrimSpace(truncate(string(respBody), 256)),
		}
	}
	return nil
}

// accessToken exchanges (and caches) the app access token for creds.
func (m *RobotMessenger) accessToken(ctx context.Context, creds channelCredentials) (string, error) {
	m.mu.Lock()
	if cached, ok := m.tokens[creds.ClientID]; ok && cached.value != "" && time.Until(cached.expiresAt) > time.Minute {
		m.mu.Unlock()
		return cached.value, nil
	}
	m.mu.Unlock()

	body, err := json.Marshal(map[string]string{
		"appKey":    creds.ClientID,
		"appSecret": creds.ClientSecret,
	})
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: marshal token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.openAPIBase+"/v1.0/oauth2/accessToken", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: new token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: token request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &APIError{Status: resp.StatusCode, Code: "token_failed", Message: strings.TrimSpace(truncate(string(respBody), 256))}
	}
	var tokenResp struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", fmt.Errorf("dingtalk robot: decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", &APIError{Code: "empty_access_token", Message: "DingTalk returned no access token"}
	}
	ttl := tokenResp.ExpireIn
	if ttl <= 0 {
		ttl = 7200
	}
	m.mu.Lock()
	m.tokens[creds.ClientID] = tokenCache{value: tokenResp.AccessToken, expiresAt: time.Now().Add(time.Duration(ttl) * time.Second)}
	m.mu.Unlock()
	return tokenResp.AccessToken, nil
}

// markdownTitle derives the conversation-list preview title from the reply
// body: first non-empty line, markdown heading markers stripped, capped.
func markdownTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#*- "))
		if line != "" {
			return truncateRunes(line, 60)
		}
	}
	return "Multica"
}

// truncateRunes caps s at n runes (multi-byte safe, unlike truncate).
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
