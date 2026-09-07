package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	dwsHistoryContextTTLSeconds = 120
	dwsHistoryQueryLimit        = dingtalkHistoryLimit + 1
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

func (r *httpDWSCredentialRedeemer) Redeem(ctx context.Context, contextToken string) (dwsCredential, error) {
	got, err := dwsclient.Redeemer{
		BaseURL:         r.baseURL,
		BaseURLProvider: r.baseURLProvider,
		Client:          r.client,
		UserAgent:       "dt-fde-multica/inbound-coordinator",
	}.Redeem(ctx, contextToken)
	if err != nil {
		return dwsCredential{}, err
	}
	return dwsCredential{UID: got.UID, ClientID: got.ClientID, AuthCode: got.AuthCode}, nil
}

type osDWSHistoryCLI struct {
	path         string
	clientSecret string
}

func (c *osDWSHistoryCLI) Exchange(ctx context.Context, configDir string, credential dwsCredential) error {
	return dwsclient.CLI{Path: c.path, ClientSecret: c.clientSecret}.Exchange(ctx, configDir, dwsclient.Credential{
		UID: credential.UID, ClientID: credential.ClientID, AuthCode: credential.AuthCode,
	})
}

func (c *osDWSHistoryCLI) ListMessages(ctx context.Context, configDir, conversationID string, limit int) ([]byte, error) {
	return dwsclient.CLI{Path: c.path, ClientSecret: c.clientSecret}.List(ctx, configDir, dwsclient.ListRequest{
		ConversationID: conversationID,
		Before:         time.Now().Add(time.Minute),
		Direction:      "older",
		Limit:          limit,
	})
}

type dwsHistoryMessage struct {
	Content       string `json:"content"`
	CreateTime    string `json:"createTime"`
	OpenMessageID string `json:"openMessageId"`
	Sender        string `json:"sender"`
	QuotedMessage *struct {
		Content string `json:"content"`
		Sender  string `json:"sender"`
	} `json:"quotedMessage"`
}

type dwsMessageListResponse struct {
	Success   bool                `json:"success"`
	ErrorCode string              `json:"errorCode"`
	ErrorMsg  string              `json:"errorMsg"`
	Messages  []dwsHistoryMessage `json:"messages"`
	Result    json.RawMessage     `json:"result"`
}

func parseDWSHistory(raw []byte, currentMessageID string) ([]HistoryLine, error) {
	var payload dwsMessageListResponse
	if json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("decode DWS conversation history response")
	}
	messages := payload.Messages
	if len(messages) == 0 && len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			Messages []dwsHistoryMessage `json:"messages"`
		}
		if json.Unmarshal(payload.Result, &nested) == nil {
			messages = nested.Messages
		}
	}
	if !payload.Success && len(messages) == 0 {
		return nil, dwsclient.HistoryRejected(payload.ErrorCode, payload.ErrorMsg)
	}
	currentMessageID = strings.TrimSpace(currentMessageID)
	newestFirst := make([]HistoryLine, 0, dingtalkHistoryLimit)
	for _, message := range messages {
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
