package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	ListMessages(context.Context, string, string, time.Time, int) ([]byte, error)
}

// DWSHistoryConfig wires the server-side, per-request DWS history reader.
type DWSHistoryConfig struct {
	AgentIdentity         dwsContextIssuer
	BaseURL               string
	BaseURLProvider       func() string
	ClientSecret          string
	CLIPath               string
	MCPBaseURL            string
	CrossOrgRenewAgentIDs []string
	HTTPClient            *http.Client
}

// dwsSharedSessions opens directories on identities' shared SDK clients
// (dwsclient.Shared); ok is false while the dws CLI serves DingTalk calls.
type dwsSharedSessions interface {
	Open(context.Context, dwsclient.Identity, dwsclient.IdentityMint) (string, func(), bool, error)
	// Mint issues a credential the way the IdentityProvider chooses, for
	// the dws CLI transport.
	Mint(context.Context, dwsclient.Identity, dwsclient.IdentityMint) (dwsclient.Credential, error)
}

type dwsHistoryLoader struct {
	shared                dwsSharedSessions
	issuer                dwsContextIssuer
	redeemer              dwsCredentialRedeemer
	cli                   dwsHistoryCLI
	mkdir                 func(string, string) (string, error)
	remove                func(string) error
	crossOrgRenewAgentIDs map[string]bool
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
		shared: dwsclient.Shared{CLI: dwsclient.CLI{Path: cliPath, ClientSecret: strings.TrimSpace(cfg.ClientSecret),
			MCPBaseURL: strings.TrimSpace(cfg.MCPBaseURL)}},
		crossOrgRenewAgentIDs: authorizedHistoryRenewalAgents(cfg.CrossOrgRenewAgentIDs),
		issuer:                cfg.AgentIdentity,
		redeemer: &httpDWSCredentialRedeemer{
			baseURL:         strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
			baseURLProvider: cfg.BaseURLProvider,
			client:          client,
		},
		cli: &osDWSHistoryCLI{
			path:         cliPath,
			mcpBaseURL:   strings.TrimSpace(cfg.MCPBaseURL),
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
	if turn.HistoryBefore.IsZero() {
		return nil, errors.New("DWS history requires a fixed window cutoff")
	}

	dir, cleanup, err := l.openSession(ctx, turn, uid, orgID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	raw, err := l.cli.ListMessages(ctx, dir, conversationID, turn.HistoryBefore, dwsHistoryQueryLimit)
	if err != nil && l.crossOrgRenewAgentIDs[util.UUIDToString(turn.AgentID)] && dwsclient.IsCrossOrgPermissionDenied(err) {
		renewer, ok := l.cli.(interface {
			RenewCrossOrgRead(context.Context, string) error
		})
		if !ok {
			return nil, errors.New("DWS cross-org chat read renewal is unavailable")
		}
		if grantErr := renewer.RenewCrossOrgRead(ctx, dir); grantErr != nil {
			return nil, grantErr
		}
		// Retry this exact scoped read once. A second rejection remains a failure.
		raw, err = l.cli.ListMessages(ctx, dir, conversationID, turn.HistoryBefore, dwsHistoryQueryLimit)
	}
	if err != nil {
		return nil, err
	}
	return parseDWSHistory(raw, turn)
}

// openSession authenticates as the turn's DWS identity: on the identity's
// shared SDK client when the SDK transport is selected (minting only when
// no shared token exists), else with a credential exchanged for this call.
func (l *dwsHistoryLoader) openSession(ctx context.Context, turn Turn, uid, orgID string) (string, func(), error) {
	identity := dwsclient.Identity{AgentID: util.UUIDToString(turn.AgentID), UID: uid, OrgID: orgID}
	mint := func(ctx context.Context, id dwsclient.Identity) (dwsclient.Credential, error) {
		c, err := l.mint(ctx, turn, id.UID, id.OrgID)
		return dwsclient.Credential{UID: c.UID, ClientID: c.ClientID, AuthCode: c.AuthCode}, err
	}
	var credential dwsCredential
	if l.shared != nil {
		dir, cleanup, ok, err := l.shared.Open(ctx, identity, mint)
		if ok {
			return dir, cleanup, err
		}
		c, err := l.shared.Mint(ctx, identity, mint)
		if err != nil {
			return "", nil, err
		}
		credential = dwsCredential{UID: c.UID, ClientID: c.ClientID, AuthCode: c.AuthCode}
	} else {
		c, err := l.mint(ctx, turn, uid, orgID)
		if err != nil {
			return "", nil, err
		}
		credential = c
	}
	dir, err := l.mkdir("", "multica-inbound-dws-")
	if err != nil {
		return "", nil, errors.New("create isolated DWS history directory")
	}
	cleanup := func() { _ = l.remove(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", nil, errors.New("secure isolated DWS history directory")
	}
	if err := l.cli.Exchange(ctx, dir, credential); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// mint issues an Agent Identity context for this read and redeems it.
func (l *dwsHistoryLoader) mint(ctx context.Context, turn Turn, uid, orgID string) (dwsCredential, error) {
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
		return dwsCredential{}, fmt.Errorf("issue DWS history identity: %w", err)
	}
	return l.redeemer.Redeem(ctx, issued.ContextToken)
}

func authorizedHistoryRenewalAgents(ids []string) map[string]bool {
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			result[id] = true
		}
	}
	return result
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
	mcpBaseURL   string
}

func (c *osDWSHistoryCLI) RenewCrossOrgRead(ctx context.Context, configDir string) error {
	return dwsclient.CLI{Path: c.path}.RenewCrossOrgRead(ctx, configDir)
}

func (c *osDWSHistoryCLI) Exchange(ctx context.Context, configDir string, credential dwsCredential) error {
	return dwsclient.CLI{Path: c.path, ClientSecret: c.clientSecret, MCPBaseURL: c.mcpBaseURL}.Exchange(ctx, configDir, dwsclient.Credential{
		UID: credential.UID, ClientID: credential.ClientID, AuthCode: credential.AuthCode,
	})
}

func (c *osDWSHistoryCLI) ListMessages(ctx context.Context, configDir, conversationID string, before time.Time, limit int) ([]byte, error) {
	if before.IsZero() {
		return nil, errors.New("DWS history requires a fixed window cutoff")
	}
	return dwsclient.CLI{Path: c.path, ClientSecret: c.clientSecret, MCPBaseURL: c.mcpBaseURL}.List(ctx, configDir, dwsclient.ListRequest{
		ConversationID: conversationID,
		Before:         before,
		Direction:      "older",
		Limit:          limit,
	})
}

type dwsHistoryMessage struct {
	Content       string          `json:"content"`
	CreateTime    json.RawMessage `json:"createTime"`
	OpenMessageID string          `json:"openMessageId"`
	Sender        string          `json:"sender"`
	SenderUID     string          `json:"senderUid"`
	SenderID      string          `json:"senderId"`
	QuotedMessage *struct {
		Content       string `json:"content"`
		Sender        string `json:"sender"`
		SenderUID     string `json:"senderUid"`
		SenderID      string `json:"senderId"`
		OpenMessageID string `json:"openMessageId"`
	} `json:"quotedMessage"`
}

type dwsMessageListResponse struct {
	ContractVersion string          `json:"contractVersion"`
	Success         bool            `json:"success"`
	ErrorCode       string          `json:"errorCode"`
	ErrorMsg        string          `json:"errorMsg"`
	Messages        json.RawMessage `json:"messages"`
	Result          json.RawMessage `json:"result"`
}

func parseDWSHistory(raw []byte, turn Turn) ([]HistoryLine, error) {
	var payload dwsMessageListResponse
	if json.Unmarshal(raw, &payload) != nil {
		return nil, errors.New("decode DWS conversation history response")
	}
	messagesRaw := payload.Messages
	if len(messagesRaw) == 0 && len(payload.Result) > 0 && payload.Result[0] == '{' {
		var nested struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(payload.Result, &nested); err != nil {
			return nil, errors.New("decode nested DWS conversation history response")
		}
		messagesRaw = nested.Messages
	}
	var messages []dwsHistoryMessage
	if len(messagesRaw) > 0 && string(messagesRaw) != "null" {
		if err := json.Unmarshal(messagesRaw, &messages); err != nil {
			return nil, errors.New("decode DWS conversation history messages")
		}
	}
	if !payload.Success && len(messages) == 0 {
		return nil, dwsclient.HistoryRejected(payload.ErrorCode, payload.ErrorMsg)
	}
	if len(messagesRaw) == 0 || string(messagesRaw) == "null" {
		return nil, errors.New("DWS conversation history response is missing messages")
	}
	windowMessageIDs := make(map[string]struct{})
	if turn.EvidenceID != "" {
		windowMessageIDs[turn.EvidenceID] = struct{}{}
	}
	for _, utterance := range turn.Utterances {
		if utterance.EvidenceID != "" {
			windowMessageIDs[utterance.EvidenceID] = struct{}{}
		}
	}
	newestFirst := make([]HistoryLine, 0, dingtalkHistoryLimit)
	for _, message := range messages {
		if _, current := windowMessageIDs[message.OpenMessageID]; current {
			continue
		}
		timestamp, timestampRaw := parseHistoryTimestamp(message.CreateTime)
		// The DWS message-list contract formats display timestamps in Shanghai,
		// independently of the process timezone. Legacy untyped values stay unknown.
		if timestamp.IsZero() && payload.ContractVersion == "im.message-list.v1" {
			timestamp, _ = time.ParseInLocation("2006-01-02 15:04:05", timestampRaw, time.FixedZone("Asia/Shanghai", 8*60*60))
		}
		if !turn.HistoryBefore.IsZero() && !timestamp.IsZero() && !timestamp.Before(turn.HistoryBefore) {
			continue
		}
		// A configuration link the bot posted earlier (or a member quoted)
		// is a live bearer token: the model and the trace never see it.
		contentRaw := RedactConfigLinks(strings.TrimSpace(message.Content))
		content := clipRunes(contentRaw, 160)
		if content == "" {
			continue
		}
		role := clipRunes(strings.Join(strings.Fields(message.Sender), " "), 40)
		if role == "" {
			role = "dingtalk"
		}
		line := HistoryLine{
			Role: role, EvidenceID: message.OpenMessageID,
			Timestamp: timestamp, TimestampRaw: timestampRaw,
			SenderID:         firstNonEmpty(message.SenderUID, message.SenderID),
			ContentTruncated: utf8.RuneCountInString(contentRaw) > 160,
		}
		if message.QuotedMessage != nil {
			line.ReplyToEvidenceID = message.QuotedMessage.OpenMessageID
			line.ReplyToSenderID = firstNonEmpty(message.QuotedMessage.SenderUID, message.QuotedMessage.SenderID)
			quotedRaw := RedactConfigLinks(strings.TrimSpace(message.QuotedMessage.Content))
			quotedContent := clipRunes(quotedRaw, 160)
			line.ContentTruncated = line.ContentTruncated || utf8.RuneCountInString(quotedRaw) > 160
			if quotedContent != "" {
				quotedSender := clipRunes(strings.Join(strings.Fields(message.QuotedMessage.Sender), " "), 40)
				if quotedSender == "" {
					quotedSender = "dingtalk"
				}
				content += "\n  引用消息（" + quotedSender + "）：" + quotedContent
			}
		}
		line.Content = content
		newestFirst = append(newestFirst, line)
		if len(newestFirst) == dingtalkHistoryLimit {
			break
		}
	}
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst, nil
}

// parseHistoryTimestamp keeps the source value even when it cannot be placed
// on a timeline. A display time without a timezone is not an authorization
// ordering anchor and must never be interpreted using the server's timezone.
func parseHistoryTimestamp(raw json.RawMessage) (time.Time, string) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return time.Time{}, ""
	}
	if strings.HasPrefix(value, "\"") {
		var decoded string
		if json.Unmarshal(raw, &decoded) != nil {
			return time.Time{}, value
		}
		value = decoded
	}
	if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return timestamp, value
	}
	if len(value) == 13 {
		if millis, err := strconv.ParseInt(value, 10, 64); err == nil && millis > 0 {
			return time.UnixMilli(millis).UTC(), value
		}
	}
	if len(value) == 10 {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
			return time.Unix(seconds, 0).UTC(), value
		}
	}
	return time.Time{}, value
}
