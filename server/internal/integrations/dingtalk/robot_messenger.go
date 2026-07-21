package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// RobotMessenger is the per-installation DingTalk server-API client,
// authenticated with each app's own credentials (the scan-to-create
// device flow minted them). It delivers outbound bot messages:
//
//   - group chats: POST /v1.0/robot/groupMessages/send with the
//     openConversationId (the callback's conversationId)
//   - direct chats: POST /v1.0/robot/oToMessages/batchSend with the
//     recipient's staff userId
//
// plus the "processing" emotion reactions on inbound messages
// (/v1.0/robot/emotion/reply|recall) and the legacy-oapi directory
// lookup the identity auto-binder needs (topapi/v2/user/get).
//
// robotCode and clientId are distinct platform identifiers: clientId mints
// access tokens while robotCode addresses the bot. Access tokens are cached
// per clientId (~2h TTL) so a chatty session does not
// re-mint on every reply; the v1.0 token doubles as the legacy oapi
// access_token (same token pool, mirroring Client.postLegacy).
type RobotMessenger struct {
	openAPIBase string
	oapiBase    string
	httpClient  *http.Client

	mu     sync.Mutex
	tokens map[string]tokenCache
}

// NewRobotMessenger constructs the messenger. openAPIBase empty defaults
// to https://api.dingtalk.com, oapiBase empty to https://oapi.dingtalk.com;
// client nil defaults to a 30s-timeout client.
func NewRobotMessenger(openAPIBase, oapiBase string, client *http.Client) *RobotMessenger {
	openAPIBase = strings.TrimRight(strings.TrimSpace(openAPIBase), "/")
	if openAPIBase == "" {
		openAPIBase = defaultOpenAPIBase
	}
	oapiBase = strings.TrimRight(strings.TrimSpace(oapiBase), "/")
	if oapiBase == "" {
		oapiBase = defaultOAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &RobotMessenger{openAPIBase: openAPIBase, oapiBase: oapiBase, httpClient: client, tokens: make(map[string]tokenCache)}
}

// RobotTarget addresses one outbound message. Exactly one field is set:
// OpenConversationID for a group chat, UserStaffID for a direct chat.
type RobotTarget struct {
	OpenConversationID string
	UserStaffID        string
	// ReplyToOpenMsgID optionally asks DingTalk to associate the outbound
	// message with the inbound message. It is kept as a domain locator; no
	// Multica issue/session identifier is sent to DingTalk.
	ReplyToOpenMsgID string
}

// SendMarkdown delivers text (agent replies are Markdown-ish; DingTalk's
// sampleMarkdown renders the common subset). The title — required by the
// msgKey — is the first non-empty line, truncated; it shows in the
// conversation list preview.
func (m *RobotMessenger) SendMarkdown(ctx context.Context, creds channelCredentials, target RobotTarget, text string) error {
	if strings.TrimSpace(creds.RobotCode) == "" {
		return fmt.Errorf("dingtalk robot: robot_code is required")
	}
	msgParam, err := json.Marshal(map[string]string{
		"title": markdownTitle(text),
		"text":  text,
	})
	if err != nil {
		return fmt.Errorf("dingtalk robot: marshal msgParam: %w", err)
	}
	body := map[string]any{
		"robotCode": creds.RobotCode,
		"msgKey":    "sampleMarkdown",
		"msgParam":  string(msgParam),
	}
	var path string
	switch {
	case target.OpenConversationID != "":
		path = "/v1.0/robot/groupMessages/send"
		body["openConversationId"] = target.OpenConversationID
		if target.ReplyToOpenMsgID != "" {
			body["openMsgId"] = target.ReplyToOpenMsgID
		}
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

// EmotionTarget addresses one inbound message for an emotion reaction. The
// Stream callback's authoritative RobotCode travels with the target because
// historical Stream installations did not persist it.
type EmotionTarget struct {
	OpenConversationID string
	OpenMsgID          string
	RobotCode          string
}

// Processing emotion payload. The emotionId/backgroundId pair is the
// publicly shipped "🤔思考中" text emotion the official DingTalk OpenClaw
// connector hardcodes for every org — proven to render without any
// per-org emotion registration.
const (
	processingEmotionType       = 2 // text emotion
	processingEmotionID         = "2659900"
	processingEmotionName       = "🤔思考中"
	processingEmotionBackground = "im_bg_1"
)

// AddEmotionReply attaches the "processing" text emotion to an inbound
// message (POST /v1.0/robot/emotion/reply).
func (m *RobotMessenger) AddEmotionReply(ctx context.Context, creds channelCredentials, target EmotionTarget) error {
	return m.postEmotion(ctx, creds, "/v1.0/robot/emotion/reply", target)
}

// RecallEmotionReply removes the "processing" text emotion again
// (POST /v1.0/robot/emotion/recall). Recall is addressed by the same
// (conversation, message, emotion) triple — there is no reaction id.
func (m *RobotMessenger) RecallEmotionReply(ctx context.Context, creds channelCredentials, target EmotionTarget) error {
	return m.postEmotion(ctx, creds, "/v1.0/robot/emotion/recall", target)
}

func (m *RobotMessenger) postEmotion(ctx context.Context, creds channelCredentials, path string, target EmotionTarget) error {
	if strings.TrimSpace(creds.RobotCode) == "" {
		return fmt.Errorf("dingtalk robot: robot_code is required")
	}
	if target.OpenConversationID == "" || target.OpenMsgID == "" {
		return fmt.Errorf("dingtalk robot: empty emotion target")
	}
	token, err := m.accessToken(ctx, creds)
	if err != nil {
		return err
	}
	return m.post(ctx, path, token, map[string]any{
		"robotCode":          creds.RobotCode,
		"openMsgId":          target.OpenMsgID,
		"openConversationId": target.OpenConversationID,
		"emotionType":        processingEmotionType,
		"emotionName":        processingEmotionName,
		"textEmotion": map[string]string{
			"emotionId":    processingEmotionID,
			"emotionName":  processingEmotionName,
			"text":         processingEmotionName,
			"backgroundId": processingEmotionBackground,
		},
	})
}

// LookupUserUnionID resolves an org staff userid to the corp's unionid
// (and org-profile email when exposed) via the legacy directory API
// (POST {oapi}/topapi/v2/user/get?access_token=…). The v1.0 app access
// token is valid on the legacy host — same token pool.
func (m *RobotMessenger) LookupUserUnionID(ctx context.Context, creds channelCredentials, staffID string) (unionID, email string, err error) {
	staffID = strings.TrimSpace(staffID)
	if staffID == "" {
		return "", "", fmt.Errorf("dingtalk robot: empty staff id")
	}
	token, err := m.accessToken(ctx, creds)
	if err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(map[string]string{"userid": staffID, "language": "zh_CN"})
	if err != nil {
		return "", "", fmt.Errorf("dingtalk robot: marshal user get: %w", err)
	}
	endpoint := m.oapiBase + "/topapi/v2/user/get?access_token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", fmt.Errorf("dingtalk robot: new user get request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("dingtalk robot: user get: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", &APIError{Status: resp.StatusCode, Code: "user_get_failed", Message: strings.TrimSpace(truncate(string(respBody), 256))}
	}
	var envelope legacyEnvelope
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", "", fmt.Errorf("dingtalk robot: decode user get envelope: %w", err)
	}
	if !envelope.OK() {
		return "", "", &APIError{Code: envelope.CodeString(), Message: envelope.ErrMsg}
	}
	var result struct {
		UnionID  string `json:"unionid"`
		Email    string `json:"email"`
		OrgEmail string `json:"org_email"`
	}
	if len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, &result); err != nil {
			return "", "", fmt.Errorf("dingtalk robot: decode user get result: %w", err)
		}
	}
	if result.UnionID == "" {
		return "", "", &APIError{Code: "empty_unionid", Message: "DingTalk returned no unionid"}
	}
	email = strings.TrimSpace(result.Email)
	if email == "" {
		email = strings.TrimSpace(result.OrgEmail)
	}
	return result.UnionID, email, nil
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

// resolveMessageFileURL exchanges an attachment callback's short-lived
// download code for the HTTP(S) URL consumed immediately by the importer.
// Neither value is included in returned errors.
func (m *RobotMessenger) resolveMessageFileURL(ctx context.Context, creds channelCredentials, downloadCode string) (string, error) {
	downloadCode = strings.TrimSpace(downloadCode)
	if downloadCode == "" {
		return "", errors.New("dingtalk robot: message file download code is empty")
	}
	token, err := m.accessToken(ctx, creds)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]string{
		"robotCode":    creds.ClientID,
		"downloadCode": downloadCode,
	})
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: marshal message file request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.openAPIBase+"/v1.0/robot/messageFiles/download", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: new message file request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-acs-dingtalk-access-token", token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: message file request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("dingtalk robot: read message file response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", &APIError{Status: resp.StatusCode, Code: "message_file_download_failed", Message: "DingTalk rejected the message file request"}
	}
	var result struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", errors.New("dingtalk robot: decode message file response")
	}
	resolved, err := url.Parse(strings.TrimSpace(result.DownloadURL))
	if err != nil || (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" {
		return "", errors.New("dingtalk robot: message file response has no valid HTTP URL")
	}
	return resolved.String(), nil
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
