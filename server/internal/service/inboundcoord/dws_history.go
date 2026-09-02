package inboundcoord

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

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	dwsHistoryContextTTLSeconds = 120
	dwsHistoryQueryLimit        = dingtalkHistoryLimit + 1
	dwsHistoryMaxResponseBytes  = 1024 * 1024
)

// DingTalkHistoryLoader reads the authoritative DingTalk conversation before
// the short-loop LLM call. Implementations must isolate credentials per call.
type DingTalkHistoryLoader interface {
	Load(context.Context, Turn) ([]HistoryLine, error)
}

type dwsContextIssuer interface {
	CreateContext(context.Context, agentidentityhsf.CreateContextRequest) (agentidentityhsf.CreateContextResult, error)
}

type dwsCredential struct {
	UID      string
	ClientID string
	AuthCode string
}

type dwsCredentialRedeemer interface {
	Redeem(context.Context, string) (dwsCredential, error)
}

type dwsHistoryCLI interface {
	Exchange(context.Context, string, dwsCredential) error
	ListMessages(context.Context, string, string, int) ([]byte, error)
}

// DWSHistoryConfig wires the server-side, per-request DWS history reader.
type DWSHistoryConfig struct {
	AgentIdentity   dwsContextIssuer
	BaseURL         string
	BaseURLProvider func() string
	ClientSecret    string
	CLIPath         string
	HTTPClient      *http.Client
}

type dwsHistoryLoader struct {
	issuer   dwsContextIssuer
	redeemer dwsCredentialRedeemer
	cli      dwsHistoryCLI
	mkdir    func(string, string) (string, error)
	remove   func(string) error
}

// NewDWSHistoryLoader creates a loader that mints a separate Agent Identity
// context for every decision. It never consumes the task's sandbox token.
func NewDWSHistoryLoader(cfg DWSHistoryConfig) DingTalkHistoryLoader {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	cliPath := strings.TrimSpace(cfg.CLIPath)
	if cliPath == "" {
		cliPath = "dws"
	}
	return &dwsHistoryLoader{
		issuer: cfg.AgentIdentity,
		redeemer: &httpDWSCredentialRedeemer{
			baseURL:         strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
			baseURLProvider: cfg.BaseURLProvider,
			client:          client,
		},
		cli: &osDWSHistoryCLI{
			path:         cliPath,
			clientSecret: strings.TrimSpace(cfg.ClientSecret),
		},
		mkdir:  os.MkdirTemp,
		remove: os.RemoveAll,
	}
}

func (l *dwsHistoryLoader) Load(ctx context.Context, turn Turn) ([]HistoryLine, error) {
	if l == nil || l.issuer == nil || l.redeemer == nil || l.cli == nil {
		return nil, errors.New("DWS history loader is not configured")
	}
	conversationID := strings.TrimSpace(turn.ConversationID)
	uid := strings.TrimSpace(turn.DWSUID)
	orgID := strings.TrimSpace(turn.DWSOrgID)
	if conversationID == "" || uid == "" || orgID == "" || !turn.AgentID.Valid {
		return nil, errors.New("DWS history identity or conversation is incomplete")
	}

	runID := "inbound-dws-" + uuid.NewString()
	issued, err := l.issuer.CreateContext(ctx, agentidentityhsf.CreateContextRequest{
		RequestID:   runID,
		TaskID:      runID,
		AgentID:     util.UUIDToString(turn.AgentID),
		RuntimeType: "SERVER",
		RuntimeID:   runID,
		Reason:      "Multica inbound coordinator DingTalk history",
		Source: map[string]string{
			"app":             "dt-fde-multica",
			"identity_source": "inbound_dws_history",
		},
		UID:        uid,
		OrgID:      orgID,
		TTLSeconds: dwsHistoryContextTTLSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("issue DWS history identity: %w", err)
	}
	credential, err := l.redeemer.Redeem(ctx, issued.ContextToken)
	if err != nil {
		return nil, err
	}

	dir, err := l.mkdir("", "multica-inbound-dws-")
	if err != nil {
		return nil, errors.New("create isolated DWS history directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = l.remove(dir)
		return nil, errors.New("secure isolated DWS history directory")
	}
	defer func() { _ = l.remove(dir) }()

	if err := l.cli.Exchange(ctx, dir, credential); err != nil {
		return nil, err
	}
	raw, err := l.cli.ListMessages(ctx, dir, conversationID, dwsHistoryQueryLimit)
	if err != nil {
		return nil, err
	}
	return parseDWSHistory(raw, turn.EvidenceID)
}

type httpDWSCredentialRedeemer struct {
	baseURL         string
	baseURLProvider func() string
	client          *http.Client
}

type dwsRedeemResponse struct {
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

func (r *httpDWSCredentialRedeemer) Redeem(ctx context.Context, contextToken string) (dwsCredential, error) {
	baseURL := r.baseURL
	if r.baseURLProvider != nil {
		baseURL = strings.TrimRight(strings.TrimSpace(r.baseURLProvider()), "/")
	}
	if baseURL == "" || strings.TrimSpace(contextToken) == "" {
		return dwsCredential{}, errors.New("Agent Identity DWS redeem is not configured")
	}
	body := strings.NewReader(`{"identityKey":"dws","credentialType":"DWS_AUTH_CODE"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/agent-identity/v1/credentials/redeem", body)
	if err != nil {
		return dwsCredential{}, errors.New("build Agent Identity DWS redeem request")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(contextToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dt-fde-multica/inbound-coordinator")
	resp, err := r.client.Do(req)
	if err != nil {
		return dwsCredential{}, errors.New("Agent Identity DWS redeem request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, dwsHistoryMaxResponseBytes+1))
	if err != nil || len(raw) > dwsHistoryMaxResponseBytes {
		return dwsCredential{}, errors.New("read Agent Identity DWS redeem response")
	}
	var payload dwsRedeemResponse
	if json.Unmarshal(raw, &payload) != nil {
		return dwsCredential{}, errors.New("decode Agent Identity DWS redeem response")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices || !payload.OK {
		return dwsCredential{}, fmt.Errorf("Agent Identity DWS redeem rejected: %s", safeDWSCode(payload.ErrorCode))
	}
	credential := dwsCredential{
		UID:      strings.TrimSpace(payload.Identity.UID),
		ClientID: strings.TrimSpace(payload.Identity.ClientID),
		AuthCode: strings.TrimSpace(payload.Credential.AuthCode),
	}
	if payload.Identity.Key != "dws" || payload.Identity.Type != "DWS_UID" ||
		payload.Credential.Type != "DWS_AUTH_CODE" || credential.ClientID == "" || credential.AuthCode == "" {
		return dwsCredential{}, errors.New("Agent Identity returned an unexpected DWS credential")
	}
	if _, err := strconv.ParseUint(credential.UID, 10, 64); err != nil {
		return dwsCredential{}, errors.New("Agent Identity returned an invalid DWS uid")
	}
	return credential, nil
}

type osDWSHistoryCLI struct {
	path         string
	clientSecret string
}

func (c *osDWSHistoryCLI) Exchange(ctx context.Context, configDir string, credential dwsCredential) error {
	if strings.TrimSpace(c.clientSecret) == "" {
		return errors.New("DWS client secret is not configured")
	}
	cmd := exec.CommandContext(ctx, c.path,
		"auth", "exchange",
		"--code", credential.AuthCode,
		"--uid", credential.UID,
		"--format", "json",
	)
	cmd.Env = dwsCommandEnv(configDir, map[string]string{
		"DWS_CLIENT_ID":     credential.ClientID,
		"DWS_CLIENT_SECRET": c.clientSecret,
	})
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("DWS AuthCode exchange failed")
	}
	return nil
}

func (c *osDWSHistoryCLI) ListMessages(ctx context.Context, configDir, conversationID string, limit int) ([]byte, error) {
	queryBefore := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Add(time.Minute).Format("2006-01-02 15:04:05")
	cmd := exec.CommandContext(ctx, c.path,
		"chat", "message", "list",
		"--group", conversationID,
		"--time", queryBefore,
		"--direction", "older",
		"--limit", strconv.Itoa(limit),
		"--format", "json",
	)
	cmd.Env = dwsCommandEnv(configDir, nil)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("DWS conversation history query failed")
	}
	if stdout.Len() > dwsHistoryMaxResponseBytes {
		return nil, errors.New("DWS conversation history response is too large")
	}
	return stdout.Bytes(), nil
}

func dwsCommandEnv(configDir string, values map[string]string) []string {
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

type dwsMessageListResponse struct {
	Success   bool   `json:"success"`
	ErrorCode string `json:"errorCode"`
	Result    struct {
		Messages []struct {
			Content       string `json:"content"`
			CreateTime    string `json:"createTime"`
			OpenMessageID string `json:"openMessageId"`
			Sender        string `json:"sender"`
			QuotedMessage *struct {
				Content string `json:"content"`
				Sender  string `json:"sender"`
			} `json:"quotedMessage"`
		} `json:"messages"`
	} `json:"result"`
}

func parseDWSHistory(raw []byte, currentMessageID string) ([]HistoryLine, error) {
	var payload dwsMessageListResponse
	if json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("decode DWS conversation history response")
	}
	if !payload.Success {
		return nil, fmt.Errorf("DWS conversation history query rejected: %s", safeDWSCode(payload.ErrorCode))
	}
	currentMessageID = strings.TrimSpace(currentMessageID)
	newestFirst := make([]HistoryLine, 0, dingtalkHistoryLimit)
	for _, message := range payload.Result.Messages {
		if currentMessageID != "" && strings.TrimSpace(message.OpenMessageID) == currentMessageID {
			continue
		}
		content := clipRunes(strings.TrimSpace(message.Content), 160)
		if content == "" {
			continue
		}
		role := clipRunes(strings.Join(strings.Fields(message.Sender), " "), 40)
		if role == "" {
			role = "dingtalk"
		}
		if message.QuotedMessage != nil {
			quotedContent := clipRunes(strings.TrimSpace(message.QuotedMessage.Content), 160)
			if quotedContent != "" {
				quotedSender := clipRunes(strings.Join(strings.Fields(message.QuotedMessage.Sender), " "), 40)
				if quotedSender == "" {
					quotedSender = "dingtalk"
				}
				content += "\n  引用消息（" + quotedSender + "）：" + quotedContent
			}
		}
		newestFirst = append(newestFirst, HistoryLine{
			Role:       role,
			Content:    content,
			EvidenceID: strings.TrimSpace(message.OpenMessageID),
		})
		if len(newestFirst) == dingtalkHistoryLimit {
			break
		}
	}
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst, nil
}

func safeDWSCode(raw string) string {
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
